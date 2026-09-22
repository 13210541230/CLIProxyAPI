package accountpool

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Path constants for the account-pool Management API (relative to /v0/management).
const (
	// PluginSegment is the plugin-relative prefix shared with the audit routes.
	PluginSegment     = "/enterprise-access-audit"
	Prefix            = "/account-pool"
	SnapshotPath      = Prefix
	PolicyPath        = Prefix + "/policy"
	ConcurrencyPath   = Prefix + "/concurrency-limits"
	DiagnosticsPath   = Prefix + "/state"
	ExclusiveProvider = ProviderCodex
)

// RoutePrefix returns the full account-pool route prefix under the given CPA management base path.
func RoutePrefix(basePath string) string {
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	if basePath == "" {
		basePath = "/v0/management"
	}
	return basePath + PluginSegment + Prefix
}

// Options carries construction settings for the Service.
type Options struct {
	DataDir string
	Reserve time.Duration
	MaxWait time.Duration
	// MaxBusy is the number of consecutive queue timeouts before a session
	// fails over to another in-pool account. Zero uses the engine default.
	MaxBusy int
	Enabled bool
}

// Service routes codex scheduler picks by caller pool binding and enforces
// per-account concurrency. Unbound callers delegate to the host builtin.
type Service struct {
	mu      sync.RWMutex
	policy  Policy
	ready   bool
	lastErr string

	persist *persistence
	engine  *Engine
	enabled bool
}

// New creates a Service with the given options. Options.Enabled gates
// scheduler participation only; admission and per-account live stats are
// always active so concurrency limits work with or without pools.
func New(opts Options) *Service {
	service := &Service{
		persist: newPersistence(opts.DataDir),
		engine:  NewEngine(opts.Reserve, opts.MaxWait, sessionKey),
		enabled: opts.Enabled,
	}
	if opts.MaxBusy > 0 {
		service.engine.SetMaxBusy(opts.MaxBusy)
	}
	return service
}

// Enabled reports whether account-pool scheduling is active.
func (s *Service) Enabled() bool {
	if s == nil {
		return false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.enabled
}

// Reload loads the persisted policy and limits after (re)configuration.
func (s *Service) Reload() error {
	if s == nil {
		return nil
	}
	p, errLoadPolicy := s.persist.loadPolicy()
	if errLoadPolicy != nil {
		return errLoadPolicy
	}
	limits, errLoadLimits := s.persist.loadLimits()
	if errLoadLimits != nil {
		return errLoadLimits
	}
	s.configureLimits(limits)
	s.mu.Lock()
	if p.Version > 0 {
		s.policy = p
		s.ready = true
		s.lastErr = ""
	} else {
		s.policy = Policy{}
		s.ready = false
	}
	s.mu.Unlock()
	return nil
}

// EnabledWithDataDir is a convenience for tests: enable and re-root the service.
func (s *Service) EnabledWithDataDir(enabled bool, dataDir string) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.enabled = enabled
	s.persist = newPersistence(dataDir)
	s.mu.Unlock()
	_ = s.Reload()
}

// Apply normalizes, validates, persists, and activates a policy snapshot.
func (s *Service) Apply(raw []byte) (Status, error) {
	if s == nil {
		return Status{}, errors.New("account pool service is unavailable")
	}
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Status{}, fmt.Errorf("decode account pool policy: %w", err)
	}
	if envelope.Policy.Version == 0 && len(raw) > 0 {
		var direct Policy
		if err := json.Unmarshal(raw, &direct); err == nil {
			envelope.Policy = direct
		}
	}
	next, errNormalize := Normalize(envelope.Policy)
	if errNormalize != nil {
		s.setError(errNormalize)
		return Status{}, errNormalize
	}
	if next.Hash == "" {
		// The management UI publishes without a precomputed digest; the plugin
		// canonicalizes it so version/conflict matching stays authoritative.
		next.Hash = Hash(next)
	} else if errValidate := ValidateHash(next); errValidate != nil {
		s.setError(errValidate)
		return Status{}, errValidate
	}
	s.mu.RLock()
	current := s.policy
	ready := s.ready
	s.mu.RUnlock()
	if ready && next.Version < current.Version {
		err := fmt.Errorf("stale account pool policy version %d; active version is %d", next.Version, current.Version)
		s.setError(err)
		return Status{}, err
	}
	if ready && next.Version == current.Version {
		if next.Hash != current.Hash {
			err := errors.New("conflicting account pool policy for the active version")
			s.setError(err)
			return Status{}, err
		}
		return s.status(), nil
	}
	if errPersist := s.persist.savePolicy(next); errPersist != nil {
		s.setError(errPersist)
		return Status{}, errPersist
	}
	s.mu.Lock()
	s.policy = next
	s.ready = true
	s.lastErr = ""
	s.mu.Unlock()
	return s.status(), nil
}

// PutLimits replaces the per-account concurrency limits and persists them.
func (s *Service) PutLimits(items []AccountLimit) error {
	if s == nil {
		return errors.New("account pool service is unavailable")
	}
	persisted := make(map[string]AccountLimit, len(items))
	for _, item := range items {
		authID := strings.TrimSpace(item.AuthID)
		if authID == "" {
			continue
		}
		if item.Limit < 0 || item.Limit > 1000 {
			return fmt.Errorf("account limit for %q must be between 0 and 1000", authID)
		}
		if item.Limit15s < 0 || item.Limit15s > 100000 {
			return fmt.Errorf("account window limit for %q must be between 0 and 100000", authID)
		}
		window := item.WindowSeconds
		if window <= 0 {
			window = 15
		}
		item.WindowSeconds = window
		persisted[authID] = item
	}
	if errPersist := s.persist.saveLimits(persisted); errPersist != nil {
		return errPersist
	}
	s.configureLimits(persisted)
	return nil
}

// LimitsView returns the persisted per-account limits for diagnostics.
func (s *Service) LimitsView() map[string]AccountLimit {
	limits, err := s.persist.loadLimits()
	if err != nil || limits == nil {
		return map[string]AccountLimit{}
	}
	return limits
}

// Pick implements the codex scheduler pick.
//
// The host routes every codex scheduler request to this plugin when account-pool
// scheduling is enabled (exclusive-scheduler-providers). The plugin therefore
// must always answer for codex: with no policy configured it acts as a
// pass-through global selector (mirroring the pre-pool behavior), bound callers
// are restricted to their pool members, and unbound callers are also handled
// globally in-process (the host builtin is not reachable under exclusivity).
func (s *Service) Pick(request SchedulerPickRequest) SchedulerPickResponse {
	if s == nil || !s.enabled {
		return notHandled()
	}
	if !requestIsCodex(request) {
		return notHandled()
	}
	s.mu.RLock()
	p := s.policy
	ready := s.ready
	s.mu.RUnlock()

	// Empty policy: nothing to enforce. Pass codex through to the global
	// selector so the service stays usable before any pool is configured.
	if !ready {
		return s.pickGlobally(request)
	}

	callerHash := callerHashOf(request)
	if callerHash == "" {
		return reject("identity_missing", http.StatusBadRequest, false, "caller hash is required for Codex pool routing")
	}
	poolID, bound := BindingPool(p, callerHash)
	if !bound {
		// Unbound caller: select globally from all candidates.
		return s.pickGlobally(request)
	}
	pool, ok := PoolByID(p, poolID)
	if !ok || !pool.Enabled {
		return reject("account_pool_disabled", http.StatusServiceUnavailable, true, "assigned account pool is disabled or missing")
	}
	members := EnabledMembers(p, poolID)
	if len(members) == 0 {
		return reject("account_pool_unavailable", http.StatusServiceUnavailable, true, "assigned account pool has no enabled members")
	}
	allowed := make(map[string]struct{}, len(members))
	for _, member := range members {
		allowed[member.AuthID] = struct{}{}
	}
	filtered := make([]schedulerAuthCandidate, 0, len(request.Candidates))
	for _, candidate := range request.Candidates {
		if _, ok := allowed[candidate.ID]; ok {
			filtered = append(filtered, candidate)
		}
	}
	if len(filtered) == 0 {
		return reject("account_pool_unavailable", http.StatusServiceUnavailable, true, "assigned account pool has no eligible candidates")
	}
	scoped := request
	scoped.Candidates = filtered
	if authID := s.engine.Pick(scoped); authID != "" {
		return selected(authID)
	}
	return reject("account_pool_unavailable", http.StatusServiceUnavailable, true, "assigned account pool could not admit a candidate")
}

// pickGlobally selects a candidate from the full request candidate set using
// the same load-aware engine as the pool path. It mirrors the host builtin's
// fill-first behavior so codex stays usable before or without pool binding.
func (s *Service) pickGlobally(request SchedulerPickRequest) SchedulerPickResponse {
	if authID := s.engine.Pick(request); authID != "" {
		return selected(authID)
	}
	return reject("account_pool_unavailable", http.StatusServiceUnavailable, true, "no candidates available for Codex scheduling")
}

// AdmitIntercept gates an already-selected request at the after-auth stage.
//
// Concurrency accounting is deliberately independent of account-pool
// scheduling: per-account limits and live counters apply to every request
// whose executor published a selected auth id, whether or not pool
// scheduling is enabled and whether or not the caller is pool-bound.
// It returns nil when the request may proceed; otherwise a termination result.
func (s *Service) AdmitIntercept(requestID string, metadata map[string]any) *AdmitResult {
	if s == nil || requestID == "" {
		return nil
	}
	authID := strings.TrimSpace(metadataString(metadata, metadataSelectedAuthID))
	if authID == "" {
		return nil
	}
	code, status, retryable, admitted := s.engine.Admit(requestID, authID)
	if admitted {
		return nil
	}
	return admissionRejected(code, status, retryable)
}

// Complete releases an admitted request at terminal state.
func (s *Service) Complete(requestID string) {
	if s == nil || requestID == "" {
		return
	}
	s.engine.Complete(requestID)
}

// StateSnapshot returns per-auth concurrency diagnostics.
func (s *Service) StateSnapshot(authID string) StateSnapshot {
	return s.engine.Snapshot(authID)
}

// Status returns the current policy status.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return policyStatus(s.policy, s.ready, s.lastErr)
}

// Policy returns the active policy snapshot.
func (s *Service) Policy() Policy {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Clone(s.policy)
}

// Snapshot returns the combined {policy, status} used by management routes.
func (s *Service) Snapshot() map[string]any {
	return map[string]any{"policy": s.Policy(), "status": s.Status()}
}

// HandleManagement dispatches one authenticated account-pool management request.
func (s *Service) HandleManagement(req managementRequest) managementResponse {
	if s == nil {
		return jsonResponse(http.StatusServiceUnavailable, map[string]any{"error": "account pool service is unavailable"})
	}
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	path := normalizeManagementPath(req.Path)
	switch {
	case method == http.MethodGet && (path == SnapshotPath || path == PolicyPath):
		return jsonResponse(http.StatusOK, s.Snapshot())
	case (method == http.MethodPut || method == http.MethodPost) && path == PolicyPath:
		status, err := s.Apply(req.Body)
		if err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error(), "status": status})
		}
		return jsonResponse(http.StatusOK, map[string]any{"status": status})
	case method == http.MethodGet && path == ConcurrencyPath:
		return jsonResponse(http.StatusOK, map[string]any{"items": s.LimitsView()})
	case method == http.MethodPut && path == ConcurrencyPath:
		var body struct {
			Items []AccountLimit `json:"items"`
		}
		if err := json.Unmarshal(req.Body, &body); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "decode concurrency limits: " + err.Error()})
		}
		if err := s.PutLimits(body.Items); err != nil {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": err.Error()})
		}
		return jsonResponse(http.StatusOK, map[string]any{"items": s.LimitsView()})
	case method == http.MethodGet && path == DiagnosticsPath:
		authID := strings.TrimSpace(req.Query.Get("authId"))
		if authID == "" {
			return jsonResponse(http.StatusBadRequest, map[string]any{"error": "authId is required"})
		}
		return jsonResponse(http.StatusOK, map[string]any{"authId": authID, "state": s.StateSnapshot(authID)})
	default:
		return jsonResponse(http.StatusNotFound, map[string]any{"error": "not found"})
	}
}

func (s *Service) configureLimits(limits map[string]AccountLimit) {
	converted := make(map[string]Limit, len(limits))
	for authID, item := range limits {
		window := item.WindowSeconds
		if window <= 0 {
			window = 15
		}
		converted[authID] = Limit{Max: item.Limit, Max15s: item.Limit15s, Window: time.Duration(window) * time.Second}
	}
	s.engine.Configure(converted)
}

func (s *Service) setError(err error) {
	s.mu.Lock()
	s.lastErr = err.Error()
	s.mu.Unlock()
}

func (s *Service) status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return policyStatus(s.policy, s.ready, s.lastErr)
}

func policyStatus(p Policy, ready bool, lastErr string) Status {
	return Status{
		Version:         p.Version,
		Hash:            p.Hash,
		Ready:           ready,
		LastError:       lastErr,
		ConfiguredPools: len(p.Pools),
		BoundCallers:    len(p.Bindings),
	}
}

// normalizeManagementPath strips the CPA management/resource base prefixes.
func normalizeManagementPath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/v0/management") {
		path = strings.TrimPrefix(path, "/v0/management")
	} else if strings.HasPrefix(path, "/v0/resource/plugins/") {
		segments := strings.SplitN(strings.TrimPrefix(path, "/v0/resource/plugins/"), "/", 2)
		if len(segments) == 2 {
			path = "/" + segments[1]
		}
	}
	// Strip any plugin-id prefix (with or without the /plugins segment) so the
	// handler matches the short /account-pool paths.
	path = strings.TrimPrefix(path, "/plugins"+PluginSegment)
	path = strings.TrimPrefix(path, PluginSegment)
	return strings.TrimRight(path, "/")
}

func callerHashOf(request schedulerPickRequest) string {
	return strings.ToLower(strings.TrimSpace(metadataString(request.Options.Metadata, metadataKeyHash)))
}

func requestIsCodex(request schedulerPickRequest) bool {
	provider := strings.ToLower(strings.TrimSpace(request.Provider))
	if provider != "" && provider != "mixed" {
		return provider == ExclusiveProvider
	}
	for _, candidate := range request.Providers {
		if strings.ToLower(strings.TrimSpace(candidate)) == ExclusiveProvider {
			return true
		}
	}
	return false
}

// SchedulerPickUnavailable is returned when the account-pool service is not loaded.
func SchedulerPickUnavailable() SchedulerPickResponse {
	return reject("policy_unavailable", http.StatusServiceUnavailable, true, "account pool service is unavailable")
}

func selected(authID string) SchedulerPickResponse {
	return schedulerPickResponse{Decision: "selected", AuthID: authID, Handled: true}
}
func notHandled() SchedulerPickResponse {
	return SchedulerPickResponse{}
}

func reject(code string, status int, retryable bool, reason string) SchedulerPickResponse {
	return SchedulerPickResponse{Decision: "reject", ErrorCode: code, HTTPStatus: status, Retryable: retryable, Reason: reason, Handled: true}
}

func admissionRejected(code string, status int, retryable bool) *AdmitResult {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":      "account_busy",
		"code":      code,
		"message":   "account credential is temporarily saturated",
		"retryable": retryable,
	}})
	return &AdmitResult{Terminate: true, StatusCode: status, Body: body}
}

func jsonResponse(status int, payload any) managementResponse {
	body, err := json.Marshal(payload)
	if err != nil {
		body = []byte(`{"error":"encode response"}`)
		status = http.StatusInternalServerError
	}
	headers := http.Header{"Content-Type": []string{"application/json"}}
	return managementResponse{StatusCode: status, Headers: headers, Body: body}
}
