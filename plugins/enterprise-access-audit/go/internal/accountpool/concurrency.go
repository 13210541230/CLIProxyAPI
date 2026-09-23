package accountpool

import (
	"container/heap"
	"strings"
	"sync"
	"time"
)

const (
	// sessionKeyBound keeps derived session keys bounded and printable.
	sessionKeyBound = 128
	// tokenBudgetPerAuth bounds tracked window bookkeeping per account.
	tokenBudgetPerAuth = 1 << 20
	// defaultSessionIdleTTL is how long a session may stay silent before its
	// binding expires and re-selection becomes allowed. Mirrors the product
	// rule: a bound session never changes OAuth accounts while it keeps
	// talking; only an idle gap longer than this releases the binding.
	defaultSessionIdleTTL = 2 * time.Hour
)

// sessionBinding tracks which account a session is pinned to plus the last
// time the session carried a request (the idle-expiry clock).
type sessionBinding struct {
	AuthID   string
	LastSeen time.Time
}

type sessionExpiryEntry struct {
	key      string
	lastSeen time.Time
	index    int
}

type sessionExpiryQueue []*sessionExpiryEntry

func (q sessionExpiryQueue) Len() int { return len(q) }
func (q sessionExpiryQueue) Less(i, j int) bool {
	return q[i].lastSeen.Before(q[j].lastSeen)
}
func (q sessionExpiryQueue) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index, q[j].index = i, j
}
func (q *sessionExpiryQueue) Push(value any) {
	entry := value.(*sessionExpiryEntry)
	entry.index = len(*q)
	*q = append(*q, entry)
}
func (q *sessionExpiryQueue) Pop() any {
	old := *q
	last := len(old) - 1
	entry := old[last]
	old[last] = nil
	entry.index = -1
	*q = old[:last]
	return entry
}

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

	limits             map[string]Limit
	active             map[string]int
	waiting            map[string]int
	requests           map[string]admissionRecord
	reserved           map[string][]time.Time
	sessions           map[string]sessionBinding // sessionKey -> binding + last request time
	sessionExpiry      sessionExpiryQueue        // ordered by last request time for bounded idle cleanup
	sessionExpiryByKey map[string]*sessionExpiryEntry
	busyRejections     map[string]int // sessionKey -> consecutive queue timeouts
	window             map[string][]time.Time
	epoch              uint64
	cursor             uint64
	maxBusy            int
	sessionIdleTTL     time.Duration

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
	return &Engine{
		limits:             map[string]Limit{},
		active:             map[string]int{},
		waiting:            map[string]int{},
		requests:           map[string]admissionRecord{},
		reserved:           map[string][]time.Time{},
		sessions:           map[string]sessionBinding{},
		sessionExpiryByKey: map[string]*sessionExpiryEntry{},
		busyRejections:     map[string]int{},
		window:             map[string][]time.Time{},
		now:                time.Now,
		sleep:              time.Sleep,
		maxBusy:            3,
		sessionIdleTTL:     defaultSessionIdleTTL,
		cfg:                engineOptions{Reserve: reserve, MaxWait: maxWait, Session: sessionFn},
	}
}

// SetMaxBusy overrides the consecutive queue-timeout budget before PickPool
// defers a pool session to the api-key provider layer. Values below 1 ignored.
func (e *Engine) SetMaxBusy(n int) {
	if e == nil || n < 1 {
		return
	}
	e.mu.Lock()
	e.maxBusy = n
	e.mu.Unlock()
}

// SetSessionIdleTTL overrides the idle gap that expires a session binding.
// Values below one second are ignored.
func (e *Engine) SetSessionIdleTTL(d time.Duration) {
	if e == nil || d < time.Second {
		return
	}
	e.mu.Lock()
	e.sessionIdleTTL = d
	e.mu.Unlock()
}

// SetTimings updates reservation and admission-wait windows in place.
// Existing in-flight state (active, waiters, sessions) is preserved so a
// hot reconfigure never drops counters or unbinds sessions.
func (e *Engine) SetTimings(reserve, maxWait time.Duration) {
	if e == nil {
		return
	}
	if reserve <= 0 {
		reserve = 10 * time.Second
	}
	if maxWait <= 0 {
		maxWait = 30 * time.Second
	}
	e.mu.Lock()
	e.cfg.Reserve = reserve
	e.cfg.MaxWait = maxWait
	e.mu.Unlock()
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
// Pick selects an account for lenient layers (global/unbound OAuth routing and
// the provider api-key layer): a session sticks while its account stays
// eligible, and silently rebinds when the account leaves the candidate set.
// Idle sessions expire after sessionIdleTTL.
func (e *Engine) Pick(request schedulerPickRequest) string {
	authID, _ := e.pick(request, false)
	return authID
}

// PickPool is the strict selector for pool-bound sessions. A binding survives
// every failure: the bound account leaving the candidates (cooled, removed,
// disabled) or a persistent busy streak yields a coded decline so the caller
// can defer to the api-key provider layer — never a sibling OAuth account.
// Only an idle gap longer than sessionIdleTTL releases the binding.
func (e *Engine) PickPool(request schedulerPickRequest) (string, string) {
	return e.pick(request, true)
}

func (e *Engine) pick(request schedulerPickRequest, strict bool) (string, string) {
	if e == nil || len(request.Candidates) == 0 {
		return "", ""
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
		return "", ""
	}

	sessionKey := ""
	if e.cfg.Session != nil {
		sessionKey = e.cfg.Session(request)
	}
	if sessionKey != "" {
		if binding, ok := e.sessions[sessionKey]; ok && binding.AuthID != "" {
			// The request just proved the session is alive: refresh the idle
			// clock before any decline so an active session never expires.
			binding.LastSeen = now
			e.rememberSessionLocked(sessionKey, binding)
			if _, eligible := seen[binding.AuthID]; eligible {
				streak := e.busyRejections[sessionKey]
				saturated := streak >= e.maxBusy && e.saturatedLocked(binding.AuthID, now)
				if strict {
					// Persistent saturation under strict pool routing defers to
					// the api-key provider; the binding itself is kept. Once the
					// account drains, the session probes it again and a successful
					// admission clears the streak.
					if saturated {
						e.mu.Unlock()
						return "", "account_busy"
					}
					e.reserved[binding.AuthID] = append(e.reserved[binding.AuthID], now)
					e.mu.Unlock()
					return binding.AuthID, ""
				}
				if saturated {
					// Lenient layers (api-key entries, unbound global OAuth) keep
					// the original hysteresis: a persistent saturated streak
					// releases the binding so the session reaches a free account.
					e.deleteSessionLocked(sessionKey)
					// Fall through to load-balanced rebind below.
				} else {
					e.reserved[binding.AuthID] = append(e.reserved[binding.AuthID], now)
					e.mu.Unlock()
					return binding.AuthID, ""
				}
			} else if strict {
				// Bound account left the candidates: decline instead of hopping
				// to a sibling account (single-account realism).
				e.mu.Unlock()
				return "", "account_pool_unavailable"
			}
			// Lenient layers rebind silently when their account disappears.
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
		e.rememberSessionLocked(sessionKey, sessionBinding{AuthID: selected, LastSeen: now})
	}
	e.mu.Unlock()
	return selected, ""
}

// saturatedLocked reports whether the account currently has no admission
// budget left, using the same conditions Admit queues on.
func (e *Engine) saturatedLocked(authID string, now time.Time) bool {
	limit := e.limitOf(authID)
	if limit.Max > 0 && e.active[authID] >= limit.Max {
		return true
	}
	if limit.Max15s > 0 && e.windowCountLocked(authID, now) >= limit.Max15s {
		return true
	}
	return false
}

// Touch refreshes the idle clock of an existing session binding without
// selecting anything, so a session whose requests are currently being served
// by the deferred api-key layer still counts as active. An already-expired
// binding is dropped so the next selection treats the session as fresh.
func (e *Engine) Touch(request schedulerPickRequest) {
	if e == nil {
		return
	}
	sessionKey := ""
	if e.cfg.Session != nil {
		sessionKey = e.cfg.Session(request)
	}
	if sessionKey == "" {
		return
	}
	now := e.now().UTC()
	e.mu.Lock()
	defer e.mu.Unlock()
	binding, ok := e.sessions[sessionKey]
	if !ok || binding.AuthID == "" {
		return
	}
	if binding.LastSeen.Before(now.Add(-e.sessionIdleTTL)) {
		e.deleteSessionLocked(sessionKey)
		return
	}
	binding.LastSeen = now
	e.rememberSessionLocked(sessionKey, binding)
}

// Admit enters bounded admission for an already-selected auth. It blocks up to
// MaxWait when the account is saturated, then rejects retryably. admitted is
// true when the request may proceed (empty rejection). Transient rejections
// keep the session on its account; only a persistent streak of rejections
// (maxBusy consecutive, reset by any success) fails the session over in-pool.
func (e *Engine) Admit(requestID, authID, sessionKey string) (code string, status int, retryable bool, admitted bool) {
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
		// The concurrency budget governs executing requests only. Counting
		// queued waiters would block admission once the queue reaches the limit,
		// leaving an idle account unable to drain its queue until timeouts fire.
		busyConcurrent := limit.Max > 0 && e.active[authID] >= limit.Max
		busyWindow := limit.Max15s > 0 && windowUse >= limit.Max15s
		if !busyConcurrent && !busyWindow {
			e.active[authID]++
			if limit.Max15s > 0 {
				e.appendWindowLocked(authID, now)
			}
			// The caller's own namespaced session key is provided by the service;
			// never scan the shared sessions map (it can hold other callers'
			// sessions bound to this account and misattribute streaks).
			if _, tracked := e.sessions[sessionKey]; sessionKey != "" && tracked {
				e.busyRejections[sessionKey] = 0 // a success clears the failure streak
			}
			e.requests[requestID] = admissionRecord{AuthID: authID, SessionKey: sessionKey}
			e.mu.Unlock()
			return "", 0, false, true
		}

		if !now.Before(deadline) {
			// Count one full queue timeout for the caller's session. The
			// binding is never released here anymore: a session stays pinned
			// to its account through any number of failures (single-account
			// realism). The streak only tells PickPool to defer the OAuth
			// layer to the api-key provider while the account stays saturated.
			if _, tracked := e.sessions[sessionKey]; sessionKey != "" && tracked {
				e.busyRejections[sessionKey]++
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
		busyConcurrent := limit.Max > 0 && e.active[authID] >= limit.Max
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

func (e *Engine) rememberSessionLocked(key string, binding sessionBinding) {
	e.sessions[key] = binding
	if entry := e.sessionExpiryByKey[key]; entry != nil {
		entry.lastSeen = binding.LastSeen
		heap.Fix(&e.sessionExpiry, entry.index)
		return
	}
	entry := &sessionExpiryEntry{key: key, lastSeen: binding.LastSeen}
	e.sessionExpiryByKey[key] = entry
	heap.Push(&e.sessionExpiry, entry)
}

func (e *Engine) deleteSessionLocked(key string) {
	if entry := e.sessionExpiryByKey[key]; entry != nil {
		heap.Remove(&e.sessionExpiry, entry.index)
		delete(e.sessionExpiryByKey, key)
	}
	delete(e.sessions, key)
	delete(e.busyRejections, key)
}

func (e *Engine) pruneLocked(now time.Time) {
	cutoff := now.Add(-e.cfg.Reserve)
	// The heap keeps idle cleanup proportional to expired sessions instead
	// of scanning every binding while the engine lock is held.
	sessionCutoff := now.Add(-e.sessionIdleTTL)
	for len(e.sessionExpiry) > 0 && e.sessionExpiry[0].lastSeen.Before(sessionCutoff) {
		e.deleteSessionLocked(e.sessionExpiry[0].key)
	}
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
