package auth

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const (
	transientFailureThreshold = 3
	transientFailureWindow    = time.Minute
)

type transientFailureSample struct {
	at  time.Time
	seq uint64
}

type transientFailureHistory struct {
	samples     [transientFailureThreshold]transientFailureSample
	lastSuccess uint64
}

// record counts completed independent requests, not individual upstream attempts.
func (h *transientFailureHistory) record(now time.Time, seq uint64) bool {
	if seq <= h.lastSuccess {
		return false
	}
	var next [transientFailureThreshold]transientFailureSample
	n := 0
	duplicate := false
	for _, sample := range h.samples {
		if !sample.at.IsZero() && now.Sub(sample.at) < transientFailureWindow && sample.seq > h.lastSuccess {
			next[n] = sample
			n++
			duplicate = duplicate || sample.seq == seq
		}
	}
	if duplicate {
		h.samples = next
		return false
	}
	if n == len(next) {
		for i := 1; i < n; i++ {
			next[i-1] = next[i]
		}
		n--
	}
	next[n] = transientFailureSample{at: now, seq: seq}
	h.samples = next
	return n+1 >= transientFailureThreshold
}

func (h *transientFailureHistory) success(seq uint64) {
	if seq > h.lastSuccess {
		h.lastSuccess = seq
	}
	for i, sample := range h.samples {
		if sample.seq <= h.lastSuccess {
			h.samples[i] = transientFailureSample{}
		}
	}
}

// mergeTransientFailureHistories preserves live evidence when model aliases or
// restored cooldown layers are combined. The dispatch sequence deduplicates
// samples shared by both snapshots.
func mergeTransientFailureHistories(a, b transientFailureHistory) transientFailureHistory {
	merged := transientFailureHistory{lastSuccess: a.lastSuccess}
	if b.lastSuccess > merged.lastSuccess {
		merged.lastSuccess = b.lastSuccess
	}
	var samples [transientFailureThreshold * 2]transientFailureSample
	for i := range a.samples {
		samples[i] = a.samples[i]
		samples[i+transientFailureThreshold] = b.samples[i]
	}
	for i := 1; i < len(samples); i++ {
		for j := i; j > 0 && samples[j].at.Before(samples[j-1].at); j-- {
			samples[j], samples[j-1] = samples[j-1], samples[j]
		}
	}
	for _, sample := range samples {
		if !sample.at.IsZero() {
			merged.record(sample.at, sample.seq)
		}
	}
	return merged
}

func restoreModelStateShadow(state, shadow *ModelState) {
	history := state.transientFailures
	*state = *shadow.cloneShallow()
	state.transientFailures = mergeTransientFailureHistories(history, state.transientFailures)
}

// A stale replace update may change metadata but must not roll back newer
// availability results for the same credential.
func preserveCurrentAuthAvailability(updated, current *Auth) {
	latest := current.Clone()
	updated.ModelStates = latest.ModelStates
	updated.Unavailable = latest.Unavailable
	updated.Status = latest.Status
	updated.StatusMessage = latest.StatusMessage
	updated.NextRetryAfter = latest.NextRetryAfter
	updated.NextRefreshAfter = latest.NextRefreshAfter
	updated.LastError = latest.LastError
	updated.Quota = latest.Quota
	updated.RejectedAccessToken = latest.RejectedAccessToken
	updated.cooldownRecordedSeq = latest.cooldownRecordedSeq
}

// Auth updates may carry stale snapshots. Only the currently published auth is
// authoritative for this process-local history.
func reconcileTransientFailures(updated, current *Auth, preserve bool) {
	for model, state := range updated.ModelStates {
		if state == nil {
			continue
		}
		var history transientFailureHistory
		if preserve {
			if latest := existingModelState(current, model); latest != nil {
				history = latest.transientFailures
			}
		}
		state.transientFailures = history
	}
	if !preserve {
		return
	}
	for model, state := range current.ModelStates {
		if state != nil && state.transientFailures != (transientFailureHistory{}) {
			ensureModelState(updated, model).transientFailures = state.transientFailures
		}
	}
}

type transientRequestKey struct {
	authID string
	model  string
}

type transientPendingResult struct {
	result  Result
	headers http.Header
}

type transientRequestScope struct {
	mu        sync.Mutex
	pending   map[transientRequestKey]transientPendingResult
	hints     map[string]time.Time
	finished  bool
	streaming bool
}

type transientRequestScopeKey struct{}
type transientSettlementKey struct{}

func (m *Manager) withTransientRequestScope(ctx context.Context) (context.Context, *transientRequestScope) {
	scope := &transientRequestScope{}
	return context.WithValue(ctx, transientRequestScopeKey{}, scope), scope
}

func transientScope(ctx context.Context) *transientRequestScope {
	if ctx == nil {
		return nil
	}
	scope, _ := ctx.Value(transientRequestScopeKey{}).(*transientRequestScope)
	return scope
}

func transientKey(result Result) transientRequestKey {
	model := canonicalModelKey(result.Model)
	if model == "" {
		model = canonicalModelKey(result.RouteModel)
	}
	return transientRequestKey{authID: result.AuthID, model: model}
}

func (s *transientRequestScope) stage(ctx context.Context, result Result) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finished {
		return false
	}
	if s.pending == nil {
		s.pending = make(map[transientRequestKey]transientPendingResult)
	}
	s.pending[transientKey(result)] = transientPendingResult{result: result, headers: internallogging.GetResponseHeaders(ctx)}
	return true
}

func (s *transientRequestScope) recordHint(authID string, until time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hints == nil {
		s.hints = make(map[string]time.Time)
	}
	if until.After(s.hints[authID]) {
		s.hints[authID] = until
	}
}

func (s *transientRequestScope) activeHintExclusions(now time.Time) map[string]time.Time {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var excluded map[string]time.Time
	for authID, until := range s.hints {
		if !until.After(now) {
			delete(s.hints, authID)
			continue
		}
		if excluded == nil {
			excluded = make(map[string]time.Time)
		}
		excluded[authID] = until
	}
	return excluded
}

func (s *transientRequestScope) discard(result Result) {
	s.mu.Lock()
	delete(s.pending, transientKey(result))
	s.mu.Unlock()
}

func (s *transientRequestScope) attachStream() {
	s.mu.Lock()
	s.streaming = true
	s.mu.Unlock()
}

func (s *transientRequestScope) hasStream() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.streaming
}

func (m *Manager) isCodexOAuth(id string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	auth := m.auths[id]
	return auth != nil && strings.EqualFold(auth.Provider, "codex") && auth.AuthKind() == AuthKindOAuth
}

func (s *transientRequestScope) finish(ctx context.Context, m *Manager) {
	s.mu.Lock()
	if s.finished {
		s.mu.Unlock()
		return
	}
	s.finished = true
	pending := s.pending
	s.pending = nil
	s.mu.Unlock()
	ctx = context.WithValue(context.WithoutCancel(ctx), transientSettlementKey{}, true)
	for _, item := range pending {
		attemptCtx := internallogging.WithFreshResponseHeadersHolder(ctx)
		internallogging.SetResponseHeaders(attemptCtx, item.headers)
		m.MarkResult(attemptCtx, item.result)
	}
}

func codexRetryAfter(ctx context.Context) *time.Duration {
	return codexRetryAfterHeaders(internallogging.GetResponseHeaders(ctx))
}

func codexRetryAfterHeaders(headers http.Header) *time.Duration {
	raw := strings.TrimSpace(headers.Get("Retry-After"))
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if seconds <= 0 || seconds > int64((1<<63-1)/time.Second) {
			return nil
		}
		delay := time.Duration(seconds) * time.Second
		return &delay
	}
	if deadline, err := http.ParseTime(raw); err == nil {
		if delay := time.Until(deadline); delay > 0 {
			return &delay
		}
	}
	return nil
}

func isCodexTurnRateLimit(result Result) bool {
	if result.Error == nil || statusCodeFromResult(result.Error) != http.StatusTooManyRequests || result.CredentialScope {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(result.Error.Code), "turn_rate_limit_exceeded") {
		return true
	}
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	return json.Unmarshal([]byte(result.Error.Message), &body) == nil && strings.EqualFold(body.Error.Code, "turn_rate_limit_exceeded")
}

type codexRetryAfterHintError struct {
	cause error
	until time.Time
}

func (e *codexRetryAfterHintError) Error() string { return e.cause.Error() }
func (e *codexRetryAfterHintError) Unwrap() error { return e.cause }
func (e *codexRetryAfterHintError) RetryAfter() *time.Duration {
	remaining := time.Until(e.until)
	if remaining <= 0 {
		return nil
	}
	return &remaining
}

// withCodexRetryAfterHint preserves an HTTP response hint across attempt
// contexts so that all retry layers respect the same upstream deadline.
func withCodexRetryAfterHint(ctx context.Context, err error, provider string, auth *Auth) error {
	if err == nil || auth == nil || !strings.EqualFold(provider, "codex") || auth.AuthKind() != AuthKindOAuth {
		return err
	}
	if !isCodexRetryHintApplicable(err) {
		return err
	}
	retryAfter := retryAfterFromErrorForAuth(err, provider, auth)
	if retryAfter == nil || *retryAfter <= 0 {
		retryAfter = codexRetryAfter(ctx)
	}
	if retryAfter == nil || *retryAfter <= 0 {
		return err
	}
	until := time.Now().Add(*retryAfter)
	if scope := transientScope(ctx); scope != nil {
		scope.recordHint(auth.ID, until)
	}
	return &codexRetryAfterHintError{cause: err, until: until}
}

func isCodexRetryHintApplicable(err error) bool {
	rerr := resultErrorFromError(err)
	return rerr != nil && !isApplyPatchToolArgumentFault(err) && !shouldSkipCredentialCooldown(rerr) && !isModelSupportResultError(rerr) &&
		(isCodexTransientStatus(rerr) || isCodexTurnRateLimit(Result{Error: rerr}))
}

func hasCodexRetryAfterHint(err error, provider string, auth *Auth) bool {
	if auth == nil || !strings.EqualFold(provider, "codex") || auth.AuthKind() != AuthKindOAuth || !isCodexRetryHintApplicable(err) {
		return false
	}
	retryAfter := retryAfterFromErrorForAuth(err, provider, auth)
	return retryAfter != nil && *retryAfter > 0
}

// retryHintAfterPoolRejection only waits after the exclusive scheduler has
// rejected a retry without selecting any new credential. Selection runs again
// after the hint expires; this never pins or bypasses the scheduler.
func (m *Manager) retryHintAfterPoolRejection(ctx context.Context, opts cliproxyexecutor.Options, err, upstreamErr error, model string, attempt, defaultRetry int, attempted map[string]struct{}, budget time.Duration) (time.Duration, bool) {
	if m == nil || ctx == nil || attempt <= 0 || len(attempted) != 0 || budget <= 0 || ctx.Err() != nil || m.HomeEnabled() || !m.pluginSchedulerExcludesFastPath("codex") || !hasUpstreamExecutionAttempt(upstreamErr) {
		return 0, false
	}
	var selectionErr *Error
	if !errors.As(err, &selectionErr) || selectionErr.Code != "account_pool_unavailable" || !selectionErr.Retryable {
		return 0, false
	}
	hints := transientScope(ctx).activeHintExclusions(time.Now())
	eligibility := authSelectionEligibilityForRequest(ctx, opts)
	eligibility.retryHintExcluded = nil
	pinned := pinnedAuthIDFromMetadata(opts.Metadata)
	var shortest time.Duration
	found := false
	for authID := range hints {
		if pinned != "" && pinned != authID {
			continue
		}
		wait, ok := m.closestCooldownWaitWithHints([]string{"codex"}, model, attempt-1, eligibility, authID, defaultRetry, statusCodeFromError(upstreamErr), map[string]struct{}{authID: {}}, hints)
		if ok && wait > 0 && wait <= budget && (!found || wait < shortest) {
			shortest, found = wait, true
		}
	}
	return shortest, found
}

func retryAfterFromErrorForAuth(err error, provider string, auth *Auth) *time.Duration {
	if retryAfter := retryAfterFromError(err); retryAfter != nil {
		return retryAfter
	}
	if auth == nil || !strings.EqualFold(provider, "codex") || auth.AuthKind() != AuthKindOAuth ||
		!isCodexTransientStatus(resultErrorFromError(err)) {
		return nil
	}
	type headersProvider interface {
		Headers() http.Header
	}
	var hp headersProvider
	if errors.As(err, &hp) && hp != nil {
		return codexRetryAfterHeaders(hp.Headers())
	}
	return nil
}

func isCodexTransientFailure(result Result) bool {
	if result.Success || result.Error == nil || result.CredentialScope || result.Error.Code == ErrorCodeForceCooldown || shouldSkipCredentialCooldown(result.Error) || isModelSupportResultError(result.Error) {
		return false
	}
	if result.RetryAfter != nil && *result.RetryAfter > 0 {
		return false
	}
	if !strings.EqualFold(result.Provider, "codex") {
		return false
	}
	if isCodexTurnRateLimit(result) {
		return result.RetryAfter == nil || *result.RetryAfter <= 0
	}
	if result.Error.Code == "empty_stream" {
		return true
	}
	return isCodexTransientStatus(result.Error)
}

func isCodexTransientStatus(err *Error) bool {
	switch statusCodeFromResult(err) {
	case http.StatusRequestTimeout, http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout, 520, 521, 522, 523, 524, 525, 526:
		return true
	default:
		return false
	}
}
