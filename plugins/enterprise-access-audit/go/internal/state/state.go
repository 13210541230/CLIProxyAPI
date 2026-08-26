package state

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

var ErrUnavailable = errors.New("enterprise access audit plugin unavailable")

// Manager owns the active store and coordinates cleanup with lifecycle changes.
type Manager struct {
	lifecycleMu sync.Mutex
	mu          sync.RWMutex
	active      *activeState
	closed      bool
}

type activeState struct {
	cleanupMu   sync.Mutex
	store       *store.Store
	stop        chan struct{}
	cleanupDone chan struct{}
	interval    time.Duration
}

// Lease keeps the manager read lock for the complete operation using the store.
type Lease struct {
	manager *Manager
	state   *activeState
	done    bool
}

// New returns an unconfigured lifecycle-safe state manager.
func New() *Manager {
	return &Manager{}
}

// Configure opens a new state, stops cleanup for the retired state, and closes it safely.
func (m *Manager) Configure(ctx context.Context, cfg config.Config) error {
	if m == nil {
		return ErrUnavailable
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	newStore, errOpen := store.Open(ctx, cfg)
	if errOpen != nil {
		return fmt.Errorf("open enterprise access audit state: %w", errOpen)
	}
	if _, errCleanup := newStore.CleanupExpired(ctx, time.Now()); errCleanup != nil {
		_ = newStore.Close()
		return fmt.Errorf("cleanup expired enterprise access audit records on startup: %w", errCleanup)
	}
	newState := &activeState{store: newStore, stop: make(chan struct{}), cleanupDone: make(chan struct{}), interval: cfg.CleanupInterval}

	m.mu.RLock()
	closed := m.closed
	retired := m.active
	m.mu.RUnlock()
	if closed {
		_ = newStore.Close()
		return ErrUnavailable
	}
	if retired != nil {
		retired.cleanupMu.Lock()
		close(retired.stop)
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		if retired != nil {
			retired.cleanupMu.Unlock()
		}
		_ = newStore.Close()
		return ErrUnavailable
	}
	m.active = newState
	var closeErr error
	if retired != nil {
		<-retired.cleanupDone
		closeErr = retired.store.Close()
	}
	m.mu.Unlock()
	if retired != nil {
		retired.cleanupMu.Unlock()
	}
	go m.cleanupLoop(newState)
	if closeErr != nil {
		return fmt.Errorf("close retired enterprise access audit store: %w", closeErr)
	}
	return nil
}

// Shutdown stops cleanup, clears active state, and closes the retired store under write ownership.
func (m *Manager) Shutdown() error {
	if m == nil {
		return nil
	}
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return nil
	}
	retired := m.active
	m.mu.RUnlock()
	if retired != nil {
		retired.cleanupMu.Lock()
		close(retired.stop)
	}
	m.mu.Lock()
	m.active = nil
	m.closed = true
	if retired != nil {
		<-retired.cleanupDone
		if errClose := retired.store.Close(); errClose != nil {
			m.mu.Unlock()
			retired.cleanupMu.Unlock()
			return errClose
		}
	}
	m.mu.Unlock()
	if retired != nil {
		retired.cleanupMu.Unlock()
	}
	return nil
}

// Acquire obtains a read lease for one complete store operation.
func (m *Manager) Acquire() (*Lease, error) {
	if m == nil {
		return nil, ErrUnavailable
	}
	m.mu.RLock()
	if m.closed || m.active == nil || m.active.store == nil {
		m.mu.RUnlock()
		return nil, ErrUnavailable
	}
	return &Lease{manager: m, state: m.active}, nil
}

// Store returns the leased store. It must not be used after Release.
func (l *Lease) Store() *store.Store {
	if l == nil || l.done || l.state == nil {
		return nil
	}
	return l.state.store
}

// Release ends the read lease and is idempotent.
func (l *Lease) Release() {
	if l == nil || l.done || l.manager == nil {
		return
	}
	l.done = true
	l.manager.mu.RUnlock()
}

// WithStore executes one operation while holding the active-state read lock.
func (m *Manager) WithStore(ctx context.Context, operation func(*store.Store) error) error {
	lease, errAcquire := m.Acquire()
	if errAcquire != nil {
		return errAcquire
	}
	defer lease.Release()
	if operation == nil || lease.Store() == nil {
		return ErrUnavailable
	}
	return operation(lease.Store())
}

// CleanupNow performs cleanup under the same read lease as all other store operations.
func (m *Manager) CleanupNow(ctx context.Context, now time.Time) (int64, error) {
	var removed int64
	errCleanup := m.WithStore(ctx, func(active *store.Store) error {
		var err error
		removed, err = active.CleanupExpired(ctx, now)
		return err
	})
	return removed, errCleanup
}

func (m *Manager) cleanupLoop(current *activeState) {
	defer close(current.cleanupDone)
	interval := current.interval
	if interval <= 0 {
		interval = time.Hour
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-current.stop:
			return
		case now := <-ticker.C:
			if !current.cleanupMu.TryLock() {
				continue
			}
			select {
			case <-current.stop:
				current.cleanupMu.Unlock()
				return
			default:
			}
			// Cleanup takes the manager read lock for the complete store operation.
			m.mu.RLock()
			if m.closed || m.active != current {
				m.mu.RUnlock()
				current.cleanupMu.Unlock()
				return
			}
			_, _ = current.store.CleanupExpired(context.Background(), now)
			m.mu.RUnlock()
			current.cleanupMu.Unlock()
		}
	}
}
