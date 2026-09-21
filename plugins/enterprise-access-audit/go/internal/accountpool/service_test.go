package accountpool

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustPolicy(t *testing.T, pools []Pool, members []Member, bindings []Binding) Policy {
	t.Helper()
	p := Policy{Version: 1, Provider: ProviderCodex, Pools: pools, Members: members, Bindings: bindings}
	normalized, err := Normalize(p)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	normalized.Hash = Hash(normalized)
	return normalized
}

func TestNormalizeRejectsInvalidPolicy(t *testing.T) {
	cases := []struct {
		name string
		p    Policy
	}{{
		name: "version zero",
		p:    Policy{Version: 0, Provider: ProviderCodex},
	}, {
		name: "non codex provider",
		p:    Policy{Version: 1, Provider: "claude"},
	}, {
		name: "empty pool id",
		p:    Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{Name: "x"}}},
	}, {
		name: "duplicate pool",
		p:    Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{ID: "a", Name: "A"}, {ID: "a", Name: "B"}}},
	}, {
		name: "member unknown pool",
		p:    Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{ID: "a", Name: "A"}}, Members: []Member{{PoolID: "zz", AuthID: "auth"}}},
	}, {
		name: "duplicate member",
		p:    Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{ID: "a", Name: "A", Enabled: true}}, Members: []Member{{PoolID: "a", AuthID: "auth", Enabled: true}, {PoolID: "a", AuthID: "auth", Enabled: true}}},
	}, {
		name: "binding unknown pool",
		p:    Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{ID: "a", Name: "A"}}, Bindings: []Binding{{APIKeyHash: "abcd1234", PoolID: "zz"}}},
	}, {
		name: "duplicate binding",
		p:    Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{ID: "a", Name: "A"}}, Bindings: []Binding{{APIKeyHash: "abcd1234", PoolID: "a"}, {APIKeyHash: "abcd1234", PoolID: "a"}}},
	}}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Normalize(tc.p); err == nil {
				t.Fatalf("Normalize(%q) expected error", tc.name)
			}
		})
	}
}

func TestHashValidation(t *testing.T) {
	p := Policy{Version: 1, Provider: ProviderCodex, Pools: []Pool{{ID: "a", Name: "A"}}}
	p, _ = Normalize(p)
	p.Hash = Hash(p)
	if err := ValidateHash(p); err != nil {
		t.Fatalf("ValidateHash() error = %v", err)
	}
	p.Hash = strings.Repeat("0", 64)
	if err := ValidateHash(p); err == nil {
		t.Fatalf("ValidateHash() expected error for tampered hash")
	}
}

func TestPolicyLookups(t *testing.T) {
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}, {ID: "ops", Name: "Ops", Enabled: true}},
		[]Member{{PoolID: "eng", AuthID: "auth-a", Priority: 0, Enabled: true}, {PoolID: "eng", AuthID: "auth-b", Priority: 5, Enabled: true}, {PoolID: "eng", AuthID: "auth-disabled", Priority: 1, Enabled: true}, {PoolID: "ops", AuthID: "auth-c", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}},
	)
	for i := range p.Members {
		if p.Members[i].AuthID == "auth-disabled" {
			p.Members[i].Enabled = false
		}
	}
	if pool, ok := PoolByID(p, "eng"); !ok || pool.Name != "Engineering" {
		t.Fatalf("PoolByID(eng) = %+v, %v", pool, ok)
	}
	members := EnabledMembers(p, "eng")
	if len(members) != 2 || members[0].AuthID != "auth-a" {
		t.Fatalf("EnabledMembers(eng) = %+v", members)
	}
	if poolID, bound := BindingPool(p, "ABCD1234"); !bound || poolID != "eng" {
		t.Fatalf("BindingPool() = %q, %v", poolID, bound)
	}
	if _, bound := BindingPool(p, "deadbeef"); bound {
		t.Fatalf("BindingPool() unexpectedly bound")
	}
}

func TestServiceApplyPersistsAndRejectsStale(t *testing.T) {
	dir := t.TempDir()
	svc := New(Options{DataDir: dir, Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	p := mustPolicy(t, []Pool{{ID: "eng", Name: "Engineering"}}, nil, []Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}})
	raw, _ := json.Marshal(Envelope{Policy: p})
	status, err := svc.Apply(raw)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if !status.Ready || status.Version != 1 {
		t.Fatalf("Apply() status = %+v", status)
	}
	// Reload from disk must restore the policy.
	reloaded := New(Options{DataDir: dir, Enabled: true})
	if err := reloaded.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := reloaded.Policy(); got.Version != 1 || !reloaded.Status().Ready {
		t.Fatalf("reloaded policy = %+v status = %+v", got, reloaded.Status())
	}
	// Stale version must be rejected.
	stale := p
	stale.Version = 0
	staleRaw, _ := json.Marshal(Envelope{Policy: stale})
	if _, err := svc.Apply(staleRaw); err == nil {
		t.Fatalf("Apply(stale) expected error")
	}
	// A provided tampered hash must be rejected.
	tampered := p
	tampered.Hash = strings.Repeat("f", 64)
	tamperedRaw, _ := json.Marshal(Envelope{Policy: tampered})
	if _, err := svc.Apply(tamperedRaw); err == nil {
		t.Fatalf("Apply(tampered hash) expected error")
	}
	// An absent hash is canonicalized server-side.
	unhashed, _ := Normalize(Policy{Version: 2, Provider: ProviderCodex, Pools: p.Pools, Members: p.Members, Bindings: p.Bindings})
	unhashedRaw, _ := json.Marshal(Envelope{Policy: unhashed})
	status, err = svc.Apply(unhashedRaw)
	if err != nil || !status.Ready || status.Version != 2 {
		t.Fatalf("Apply(no hash) = %+v, %v", status, err)
	}
	// Reload reflects the server-canonicalized hash.
	recheck := New(Options{DataDir: dir, Enabled: true})
	if err := recheck.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if got := recheck.Policy(); got.Version != 2 || got.Hash == "" {
		t.Fatalf("reloaded canonical policy = %+v", got)
	}
}

func TestServicePickRouting(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir(), Reserve: time.Second, MaxWait: time.Second, Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}, {ID: "off", Name: "Off", Enabled: false}},
		[]Member{{PoolID: "eng", AuthID: "auth-a", Priority: 0, Enabled: true}, {PoolID: "eng", AuthID: "auth-b", Priority: 1, Enabled: true}, {PoolID: "off", AuthID: "auth-c", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}, {APIKeyHash: "00ff00ff", PoolID: "off"}},
	)
	raw, _ := json.Marshal(Envelope{Policy: p})
	if _, err := svc.Apply(raw); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	req := func(hash string, candidates ...string) schedulerPickRequest {
		cands := make([]schedulerAuthCandidate, 0, len(candidates))
		for _, id := range candidates {
			cands = append(cands, schedulerAuthCandidate{ID: id})
		}
		return schedulerPickRequest{
			Provider:   "codex",
			Options:    schedulerOptions{Metadata: map[string]any{"quota_key_hash": hash}},
			Candidates: cands,
		}
	}

	// Unbound caller selects globally (host builtin is not reachable under exclusivity).
	resp := svc.Pick(req("12345678", "auth-a", "auth-b"))
	if resp.Decision != "selected" || !resp.Handled || (resp.AuthID != "auth-a" && resp.AuthID != "auth-b") {
		t.Fatalf("unbound pick = %+v", resp)
	}
	// Non-codex provider is not handled.
	other := req("abcd1234", "auth-a", "auth-b")
	other.Provider = "claude"
	if resp := svc.Pick(other); resp.Handled {
		t.Fatalf("non-codex pick unexpectedly handled: %+v", resp)
	}
	// Bound caller is restricted to pool members.
	selected := svc.Pick(req("abcd1234", "auth-a", "auth-b", "auth-c")).AuthID
	if selected != "auth-a" && selected != "auth-b" {
		t.Fatalf("bound pick selected outside pool: %q", selected)
	}
	// Disabled pool rejects.
	resp = svc.Pick(req("00ff00ff", "auth-c"))
	if resp.Decision != "reject" || resp.ErrorCode != "account_pool_disabled" {
		t.Fatalf("disabled pool pick = %+v", resp)
	}
	// No eligible candidates rejects.
	resp = svc.Pick(req("abcd1234", "auth-c"))
	if resp.Decision != "reject" || resp.ErrorCode != "account_pool_unavailable" {
		t.Fatalf("no eligible pick = %+v", resp)
	}
	// Missing identity rejects when a policy is enforced.
	missing := req("", "auth-a", "auth-b")
	missing.Options.Metadata = nil
	if resp := svc.Pick(missing); resp.Decision != "reject" {
		t.Fatalf("missing identity pick = %+v", resp)
	}
	// No candidates at all rejects with a retryable error.
	if resp := svc.Pick(req("abcd1234")); resp.Decision != "reject" || !resp.Retryable {
		t.Fatalf("no candidates pick = %+v", resp)
	}
	// Empty policy (never applied) is pass-through: global selection succeeds,
	// so codex stays usable before any pool is configured.
	before := New(Options{DataDir: t.TempDir(), Enabled: true})
	if resp := before.Pick(req("abcd1234", "auth-a")); resp.Decision != "selected" || resp.AuthID != "auth-a" {
		t.Fatalf("empty-policy pick = %+v", resp)
	}
	// Missing identity is not required in pass-through mode.
	bare := schedulerPickRequest{Provider: "codex", Candidates: []schedulerAuthCandidate{{ID: "auth-a"}}}
	if resp := before.Pick(bare); resp.Decision != "selected" || resp.AuthID != "auth-a" {
		t.Fatalf("empty-policy anonymous pick = %+v", resp)
	}
}

func TestServiceAdmitScope(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir(), Reserve: time.Second, MaxWait: time.Second, Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}},
		[]Member{{PoolID: "eng", AuthID: "auth-a", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}},
	)
	raw, _ := json.Marshal(Envelope{Policy: p})
	if _, err := svc.Apply(raw); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	// Unbound caller is never gated.
	if result := svc.AdmitIntercept("req-1", map[string]any{"quota_key_hash": "12345678", "selected_auth_id": "auth-a"}); result != nil {
		t.Fatalf("unbound admit = %+v", result)
	}
	// Bound caller with selected auth is admitted (nil).
	if result := svc.AdmitIntercept("req-1", map[string]any{"quota_key_hash": "abcd1234", "selected_auth_id": "auth-a"}); result != nil {
		t.Fatalf("bound admit = %+v", result)
	}
	// Same request id duplicates are idempotent.
	if result := svc.AdmitIntercept("req-1", map[string]any{"quota_key_hash": "abcd1234", "selected_auth_id": "auth-a"}); result != nil {
		t.Fatalf("duplicate admit = %+v", result)
	}
	// Missing selected auth is never gated.
	if result := svc.AdmitIntercept("req-2", map[string]any{"quota_key_hash": "abcd1234"}); result != nil {
		t.Fatalf("no selected auth admit = %+v", result)
	}
	// Complete releases the active slot.
	svc.Complete("req-1")
}

func TestEngineConcurrencyGate(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	current := base
	engine := NewEngine(time.Second, 100*time.Millisecond, nil)
	engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	engine.Configure(map[string]Limit{"auth-a": {Max: 1}})

	req := schedulerPickRequest{Provider: "codex", Candidates: []schedulerAuthCandidate{{ID: "auth-a"}}, Options: schedulerOptions{Metadata: map[string]any{"quota_key_hash": "abcd1234"}}}
	if authID := engine.Pick(req); authID != "auth-a" {
		t.Fatalf("Pick() = %q", authID)
	}
	if code, status, _, ok := engine.Admit("r1", "auth-a"); !ok || code != "" || status != 0 {
		t.Fatalf("Admit(r1) = %q %d %v", code, status, ok)
	}
	// Second concurrent admit on the same account must be rejected (Max=1, wait expires fast).
	code, status, retryable, ok := engine.Admit("r2", "auth-a")
	if ok || code != "account_busy" || status != 503 || !retryable {
		t.Fatalf("Admit(r2) = %q %d retry=%v ok=%v", code, status, retryable, ok)
	}
	// Completing the first releases the slot.
	engine.Complete("r1")
	code, status, _, ok = engine.Admit("r3", "auth-a")
	if !ok || code != "" || status != 0 {
		t.Fatalf("Admit(r3 after release) = %q %d %v", code, status, ok)
	}
}

func TestEngineSessionStickiness(t *testing.T) {
	sessionFn := func(req schedulerPickRequest) string {
		return normalizeSessionKey(metadataString(req.Options.Metadata, "session_id"))
	}
	engine := NewEngine(time.Second, time.Second, sessionFn)
	engine.SetClock(func() time.Time { return time.Now() }, nil)
	req := schedulerPickRequest{
		Provider:   "codex",
		Candidates: []schedulerAuthCandidate{{ID: "a"}, {ID: "b"}},
		Options:    schedulerOptions{Metadata: map[string]any{"session_id": "sess-1"}},
	}
	first := engine.Pick(req)
	if first != "a" && first != "b" {
		t.Fatalf("Pick() first = %q", first)
	}
	for i := 0; i < 5; i++ {
		if got := engine.Pick(req); got != first {
			t.Fatalf("Pick() iter %d = %q want %q", i, got, first)
		}
	}
}

func TestServiceManagementRoutes(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir(), Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	// GET snapshot works pre-policy.
	response := svc.HandleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/enterprise-access-audit/account-pool"})
	if response.StatusCode != 200 {
		t.Fatalf("GET snapshot status = %d", response.StatusCode)
	}
	p := mustPolicy(t, []Pool{{ID: "eng", Name: "Engineering"}}, nil, []Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}})
	raw, _ := json.Marshal(Envelope{Policy: p})
	response = svc.HandleManagement(managementRequest{Method: "PUT", Path: "/v0/management/plugins/enterprise-access-audit/account-pool/policy", Body: raw})
	if response.StatusCode != 200 {
		t.Fatalf("PUT policy status = %d body=%s", response.StatusCode, response.Body)
	}
	// PUT limits.
	limits, _ := json.Marshal(map[string]any{"items": []AccountLimit{{AuthID: "auth-a", Limit: 1, Limit15s: 60, WindowSeconds: 15}}})
	response = svc.HandleManagement(managementRequest{Method: "PUT", Path: "/v0/management/plugins/enterprise-access-audit/account-pool/concurrency-limits", Body: limits})
	if response.StatusCode != 200 {
		t.Fatalf("PUT limits status = %d body=%s", response.StatusCode, response.Body)
	}
	// GET state requires authId.
	response = svc.HandleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/enterprise-access-audit/account-pool/state"})
	if response.StatusCode != 400 {
		t.Fatalf("GET state no authId status = %d", response.StatusCode)
	}
	response = svc.HandleManagement(managementRequest{Method: "GET", Path: "/v0/management/plugins/enterprise-access-audit/account-pool/state", Query: url.Values{"authId": []string{"auth-a"}}})
	if response.StatusCode != 200 {
		t.Fatalf("GET state status = %d body=%s", response.StatusCode, response.Body)
	}
}
