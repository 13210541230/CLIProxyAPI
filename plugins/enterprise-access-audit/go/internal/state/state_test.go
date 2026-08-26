package state

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

func stateConfig(t *testing.T, interval time.Duration) config.Config {
	t.Helper()
	cfg, err := config.Normalize(config.Config{DataDir: t.TempDir(), RetentionDays: 30, DefaultAuditEnabled: true, MaxTextBytes: 1024, CleanupInterval: interval}, t.TempDir())
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	return cfg
}

func TestReconfigureWaitsForReaderBeforeClosingRetiredStore(t *testing.T) {
	manager := New()
	if err := manager.Configure(context.Background(), stateConfig(t, time.Hour)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	lease, err := manager.Acquire()
	if err != nil {
		t.Fatalf("Acquire() error = %v", err)
	}
	finished := make(chan error, 1)
	go func() { finished <- manager.Configure(context.Background(), stateConfig(t, time.Hour)) }()
	select {
	case err := <-finished:
		t.Fatalf("reconfigure completed while reader held lease: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	lease.Release()
	if err := <-finished; err != nil {
		t.Fatalf("reconfigure error = %v", err)
	}
	if err := manager.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestConcurrentReadWriteAndShutdown(t *testing.T) {
	manager := New()
	if err := manager.Configure(context.Background(), stateConfig(t, time.Second)); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	var wait sync.WaitGroup
	for i := 0; i < 8; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for j := 0; j < 50; j++ {
				_ = manager.WithStore(context.Background(), func(_ *store.Store) error { return nil })
			}
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		for i := 0; i < 5; i++ {
			_ = manager.Configure(context.Background(), stateConfig(t, time.Second))
		}
	}()
	wait.Wait()
	if err := manager.Shutdown(); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := manager.WithStore(context.Background(), func(_ *store.Store) error { return nil }); err != ErrUnavailable {
		t.Fatalf("WithStore after shutdown = %v, want ErrUnavailable", err)
	}
}
