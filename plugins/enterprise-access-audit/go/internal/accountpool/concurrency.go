package accountpool

import (
	"strings"
	"sync"
	"time"
)

const (
	// sessionKeyBound keeps derived session keys bounded and printable.
	sessionKeyBound = 128
	// tokenBudgetPerAuth bounds tracked window bookkeeping per account.
	tokenBudgetPerAuth = 1 << 20
)

// Limit configures per-account concurrency admission. Zero Max and Max15s mean
// the account is not concurrency-managed: it is picked and reserved but never gated.
type Limit struct {
	Max    int
	Max15s int
	Window time.Duration
}

// candidateLoad describes the admission pressure of one auth candidate.
type candidateLoad struct {
	AuthID   string
	Pressure uint64
	Load     uint64
}

// Engine owns reservation, session stickiness, pressure selection, and bounded
// admission for pool accounts. All state is guarded by one mutex.
type Engine struct {
	mu sync.Mutex

	limits    map[string]Limit
	active    map[string]int
	waiting   map[string]int
	requests  map[string]admissionRecord
	reserved  map[string][]time.Time
	sessions  map[string]string // sessionKey -> account auth id
	retries   map[string]int    // sessionKey -> consecutive retry count
	window    map[string][]time.Time
	epoch     uint64
	cursor    uint64
	maxRetry  int

	now   func() time.Time
	sleep func(time.Duration)
	cfg   engineOptions
}

type engineOptions struct {
	Reserve time.Duration
	MaxWait time.Duration
	Session func(schedulerPickRequest) string
}

type admissionRecord struct {
	AuthID     string
	SessionKey string
}

// NewEngine creates an engine with the given options.
func NewEngine(reserve, maxWait time.Duration, sessionFn func(schedulerPickRequest) string) *Engine {
	if reserve <= 0 {
		reserve = 10 * time.Second
	}
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	const maxRetryDefault = 3
	return &Engine{
		limits:   map[string]Limit{},
		active:   map[string]int{},
		waiting:  map[string]int{},
		requests: map[string]admissionRecord{},
		reserved: map[string][]time.Time{},
		sessions: map[string]string{},
		retries:  map[string]int{},
		window:   map[string][]time.Time{},
		now:      time.Now,
		sleep:    time.Sleep,
		maxRetry: maxRetryDefault,
		cfg:      engineOptions{Reserve: reserve, MaxWait: maxWait, Session: sessionFn},
	}
}

// SetMaxRetry overrides the maximum consecutive retry count before releasing a
// sticky session. The default is 3.
func (e *Engine) SetMaxRetry(n int) {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if n < 1 {
		n = 1
	}
	e.maxRetry = n
}

// Configure atomically replaces limits and invalidates in-flight waiters.
func (e *Engine) Configure(limits map[string]Limit) {
	if e == nil {
		return
	}
	e.mu.Lock()
	next := make(map[string]Limit, len(limits))
	for authID, limit := range limits {
		if strings.TrimSpace(authID) != "" {
			next[authID] = limit
		}
	}
	e.limits = next
	e.epoch++
	e.mu.Unlock()
}

// SetClock overrides the internal clock and sleep for deterministic tests. It
// must be called before any concurrent access.
func (e *Engine) SetClock(now func() time.Time, sleep func(time.Duration)) {
	if e == nil {
		return
	}
	e.mu.Lock()
	if now != nil {
		e.now = now
	}
	if sleep != nil {
		e.sleep = sleep
	}
	e.mu.Unlock()
}

// Pick selects the least-pressure candidate with reservation and session
// stickiness. It returns "" when no candidate is eligible.
func (e *Engine) Pick(request schedulerPickRequest) string {
	if e == nil || len(request.Candidates) == 0 {
		return ""
	}
	now := e.now().UTC()
	e.mu.Lock()
	e.pruneLocked(now)

	loads := make([]candidateLoad, 0, len(request.Candidates))
	seen := make(map[string]struct{}, len(request.Candidates))
	for _, candidate := range request.Candidates {
		authID := strings.TrimSpace(candidate.ID)
		if authID == "" {
			continue
		}
		if _, duplicate := seen[authID]; duplicate {
			continue
		}
		seen[authID] = struct{}{}
		limit := e.limitOf(authID)
		reserved := len(e.reserved[authID])
		activeLoad := e.active[authID] + e.waiting[authID] + reserved
		windowUse := e.windowCountLocked(authID, now)
		load := uint64(activeLoad + windowUse)
		var pressure uint64
		if limit.Max > 0 {
			pressure = uint64(activeLoad) * uint64(1_000_000) / uint64(limit.Max)
		}
		if limit.Max15s > 0 {
			if windowPressure := uint64(windowUse) * uint64(1_000_000) / uint64(limit.Max15s); windowPressure > pressure {
				pressure = windowPressure
			}
		}
		loads = append(loads, candidateLoad{AuthID: authID, Pressure: pressure, Load: load})
	}
	if len(loads) == 0 {
		e.mu.Unlock()
		return ""
	}

	sessionKey := ""
	if e.cfg.Session != nil {
		sessionKey = e.cfg.Session(request)
	}
	if sessionKey != "" {
		if sticky := e.sessions[sessionKey]; sticky != "" {
			// Strict session stickiness: always reserve the sticky account.
			// Admit handles queueing when saturated; concurrent requests within
			// the same session are guaranteed to land on the same account.
			e.reserved[sticky] = append(e.reserved[sticky], now)
			e.retries[sessionKey] = 0 // reset consecutive failures
			e.mu.Unlock()
			return sticky
		}
	}

	// New session: load-balance across equal-pressure candidates.
	best := loads[0]
	for _, candidate := range loads {
		if candidate.Pressure < best.Pressure || candidate.Pressure == best.Pressure && candidate.Load < best.Load {
			best = candidate
		}
	}
	equal := make([]string, 0, len(loads))
	for _, candidate := range loads {
		if candidate.Pressure == best.Pressure && candidate.Load == best.Load {
			equal = append(equal, candidate.AuthID)
		}
	}
	var selected string
	if len(equal) == 1 {
		selected = equal[0]
	} else {
		selected = equal[e.cursor%uint64(len(equal))]
		e.cursor++
	}
	e.reserved[selected] = append(e.reserved[selected], now)
	if sessionKey != "" {
		e.sessions[sessionKey] = selected
	}
	e.mu.Unlock()
	return selected
}

// Admit enters bounded admission for an already-selected auth. It blocks up to
// MaxWait when the account is saturated, then rejects retryably. admitted is
// true when the request may proceed (empty rejection).
//
// When the session key is available via metadata, consecutive retryable failures
// release the sticky session so the next Pick can load-balance to another account.
func (e *Engine) Admit(requestID, authID string) (code string, status int, retryable bool, admitted bool) {
	if e == nil {
		return "account_pool_unavailable", 503, true, false
	}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return "", 0, false, true // no account identity to gate
	}
	now := e.now().UTC()
	deadline := now.Add(e.cfg.MaxWait)

	for {
		e.mu.Lock()
		e.pruneLocked(now)
		limit := e.limitOf(authID)

		if record, exists := e.requests[requestID]; exists {
			if record.AuthID == authID {
				e.mu.Unlock()
				return "", 0, false, true // duplicate admission for the same request
			}
			e.releaseAuthLocked(record.AuthID)
			delete(e.requests, requestID)
		}
		e.consumeReservationLocked(authID, now)

		windowUse := 0
		if limit.Max15s > 0 {
			windowUse = e.windowCountLocked(authID, now)
		}
		busyConcurrent := limit.Max > 0 && e.active[authID]+e.waiting[authID] >= limit.Max
		busyWindow := limit.Max15s > 0 && windowUse >= limit.Max15s
		if !busyConcurrent && !busyWindow {
			e.active[authID]++
			if limit.Max15s > 0 {
				e.appendWindowLocked(authID, now)
			}
			// Store session key in the admission record for retry tracking.
			sessKey := ""
			for sk, sa := range e.sessions {
				if sa == authID {
					sessKey = sk
					break
				}
			}
			e.requests[requestID] = admissionRecord{AuthID: authID, SessionKey: sessKey}
			e.mu.Unlock()
			return "", 0, false, true
		}

		if !now.Before(deadline) {
			// Determine the session key for retry tracking.
			sk := ""
			if record, exists := e.requests[requestID]; exists {
				sk = record.SessionKey
			}
			if sk == "" {
				// Fallback: find session bound to this authID.
				for sessKey, sessAuth := range e.sessions {
					if sessAuth == authID {
						sk = sessKey
						break
					}
				}
			}
			if sk != "" {
				e.retries[sk]++
				if e.retries[sk] > e.maxRetry {
					delete(e.sessions, sk)
					delete(e.retries, sk)
				}
			}
			e.mu.Unlock()
			return "account_busy", 503, true, false
		}
		e.waiting[authID]++
		waiterEpoch := e.epoch
		e.mu.Unlock()

		e.waitForSlot(authID, waiterEpoch, deadline)
		now = e.now().UTC()
	}
}

func (e *Engine) waitForSlot(authID string, waiterEpoch uint64, deadline time.Time) {
	for {
		e.mu.Lock()
		if e.epoch != waiterEpoch {
			e.lowerWaitingLocked(authID)
			e.mu.Unlock()
			return
		}
		now := e.now()
		limit := e.limitOf(authID)
		windowUse := 0
		if limit.Max15s > 0 {
			windowUse = e.windowCountLocked(authID, now)
		}
		busyConcurrent := limit.Max > 0 && e.active[authID]+e.waiting[authID] >= limit.Max
		busyWindow := limit.Max15s > 0 && windowUse >= limit.Max15s
		slotFree := !busyConcurrent && !busyWindow
		e.mu.Unlock()
		if slotFree || !now.Before(deadline) {
			e.mu.Lock()
			e.lowerWaitingLocked(authID)
			e.mu.Unlock()
			return
		}
		remaining := deadline.Sub(now)
		if remaining > 50*time.Millisecond {
			remaining = 50 * time.Millisecond
		}
		e.sleep(remaining)
	}
}

// Complete releases an admitted request when it ends.
func (e *Engine) Complete(requestID string) {
	if e == nil || requestID == "" {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if record, exists := e.requests[requestID]; exists {
		delete(e.requests, requestID)
		e.releaseAuthLocked(record.AuthID)
	}
}

// StateSnapshot returns per-auth diagnostics.
type StateSnapshot struct {
	Active    int   `json:"active"`
	Waiting   int   `json:"waiting"`
	Reserved  int   `json:"reserved"`
	WindowHit int   `json:"windowHit"`
	Limit     Limit `json:"-"`
}

// Snapshot returns per-auth concurrency diagnostics for management display.
func (e *Engine) Snapshot(authID string) StateSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.now()
	e.pruneLocked(now)
	return StateSnapshot{
		Active:    e.active[authID],
		Waiting:   e.waiting[authID],
		Reserved:  len(e.reserved[authID]),
		WindowHit: e.windowCountLocked(authID, now),
		Limit:     e.limitOf(authID),
	}
}

// limitOf returns the configured limit or an empty (ungated) limit when the
// account is not explicitly managed.
func (e *Engine) limitOf(authID string) Limit {
	return e.limits[authID]
}

func (e *Engine) consumeReservationLocked(authID string, now time.Time) {
	queue := e.reserved[authID]
	for len(queue) > 0 && queue[0].Before(now) {
		queue = queue[1:]
	}
	if len(queue) == 0 {
		return
	}
	e.reserved[authID] = queue[1:]
}

func (e *Engine) releaseAuthLocked(authID string) {
	if e.active[authID] > 0 {
		e.active[authID]--
	}
}

func (e *Engine) lowerWaitingLocked(authID string) {
	if e.waiting[authID] > 0 {
		e.waiting[authID]--
	}
}

func (e *Engine) pruneLocked(now time.Time) {
	cutoff := now.Add(-e.cfg.Reserve)
	for authID, queue := range e.reserved {
		if len(queue) == 0 {
			continue
		}
		kept := queue[:0]
		for _, reservedAt := range queue {
			if !reservedAt.Before(cutoff) {
				kept = append(kept, reservedAt)
			}
		}
		if len(kept) == 0 {
			delete(e.reserved, authID)
		} else {
			e.reserved[authID] = kept
		}
	}
}

func (e *Engine) windowCountLocked(authID string, now time.Time) int {
	window := e.limits[authID].Window
	if window <= 0 {
		window = 15 * time.Second
	}
	cutoff := now.Add(-window)
	queue := e.window[authID]
	count := 0
	kept := queue[:0]
	for _, at := range queue {
		if at.Before(cutoff) {
			continue
		}
		kept = append(kept, at)
		count++
	}
	if len(kept) == 0 {
		delete(e.window, authID)
	} else {
		e.window[authID] = kept
	}
	return count
}

func (e *Engine) appendWindowLocked(authID string, now time.Time) {
	queue := e.window[authID]
	if len(queue) >= tokenBudgetPerAuth {
		queue = queue[len(queue)-tokenBudgetPerAuth/2:]
	}
	e.window[authID] = append(queue, now)
}
