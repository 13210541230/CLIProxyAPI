package accountpool

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"
)

func layeredRequest(hash string, candidates ...schedulerAuthCandidate) schedulerPickRequest {
	return schedulerPickRequest{
		Provider:   "codex",
		Options:    schedulerOptions{Metadata: map[string]any{"quota_key_hash": hash}},
		Candidates: candidates,
	}
}

func apiKeyCandidate(id string, priority int) schedulerAuthCandidate {
	return schedulerAuthCandidate{
		ID:       id,
		Priority: priority,
		Attributes: map[string]string{
			"source":       "config:codex[sk-test]",
			"config_index": "0",
		},
	}
}

func oauthCandidate(id string, priority int) schedulerAuthCandidate {
	return schedulerAuthCandidate{
		ID:         id,
		Priority:   priority,
		Attributes: map[string]string{"source": "/data/auths/codex-1.json"},
	}
}

func newLayeredService(t *testing.T, enabled bool, p *Policy) *Service {
	t.Helper()
	svc := New(Options{DataDir: t.TempDir(), Reserve: time.Second, MaxWait: time.Second, Enabled: enabled})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if p != nil {
		raw, errMarshal := json.Marshal(Envelope{Policy: *p})
		if errMarshal != nil {
			t.Fatalf("marshal policy: %v", errMarshal)
		}
		if _, err := svc.Apply(raw); err != nil {
			t.Fatalf("Apply() error = %v", err)
		}
	}
	return svc
}

// Binding decision: api-key credentials are never constrained by pool
// membership, so a bound caller whose request is only served by a
// provider-configured api-key must still be selected.
func TestServicePick_APIKeyLayerBypassesPoolMembership(t *testing.T) {
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}},
		[]Member{{PoolID: "eng", AuthID: "auth-oauth", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}},
	)
	svc := newLayeredService(t, true, &p)

	resp := svc.Pick(layeredRequest("abcd1234", apiKeyCandidate("key-1", 25)))
	if resp.Decision != "selected" || !resp.Handled || resp.AuthID != "key-1" {
		t.Fatalf("api-key layer must serve bound callers outside pool membership: %+v", resp)
	}
}

// A pool-bound rejection fails over to the api-key layer instead of
// surfacing account_pool_* while a provider-configured credential is able
// to serve the request.
func TestServicePick_PoolRejectFailsOverToAPIKey(t *testing.T) {
	p := mustPolicy(t,
		[]Pool{{ID: "off", Name: "Off", Enabled: false}},
		[]Member{{PoolID: "off", AuthID: "auth-oauth", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "off"}},
	)
	svc := newLayeredService(t, true, &p)

	resp := svc.Pick(layeredRequest("abcd1234", apiKeyCandidate("key-1", 0), oauthCandidate("auth-oauth", 50)))
	if resp.Decision != "selected" || resp.AuthID != "key-1" {
		t.Fatalf("pool rejection must fail over to api-key layer: %+v", resp)
	}
}

// Provider priority decides which layer wins: higher priority serves first,
// equal priority keeps the pool/OAuth layer first.
func TestServicePick_PriorityDecidesLayer(t *testing.T) {
	svc := newLayeredService(t, true, nil)

	resp := svc.Pick(layeredRequest("", oauthCandidate("oauth-1", 0), apiKeyCandidate("key-high", 25)))
	if resp.Decision != "selected" || resp.AuthID != "key-high" {
		t.Fatalf("higher-priority api-key layer must win: %+v", resp)
	}

	resp = svc.Pick(layeredRequest("", oauthCandidate("oauth-1", 0), apiKeyCandidate("key-same", 0)))
	if resp.Decision != "selected" || resp.AuthID != "oauth-1" {
		t.Fatalf("equal priority must keep the OAuth layer first: %+v", resp)
	}

	resp = svc.Pick(layeredRequest("", oauthCandidate("oauth-high", 50), apiKeyCandidate("key-low", 0)))
	if resp.Decision != "selected" || resp.AuthID != "oauth-high" {
		t.Fatalf("higher-priority OAuth layer must win: %+v", resp)
	}
}

// Turning the pool off must keep Codex usable: a stale exclusive registration
// still dispatches into the plugin, which now serves layered global selection
// instead of answering notHandled (policy_unavailable).
func TestServicePick_DisabledPoolKeepsServing(t *testing.T) {
	svc := newLayeredService(t, false, nil)

	resp := svc.Pick(layeredRequest("abcd1234", apiKeyCandidate("key-1", 25)))
	if resp.Decision != "selected" || resp.AuthID != "key-1" {
		t.Fatalf("disabled pool must keep serving api-key layer: %+v", resp)
	}

	resp = svc.Pick(layeredRequest("", oauthCandidate("oauth-1", 0)))
	if resp.Decision != "selected" || resp.AuthID != "oauth-1" {
		t.Fatalf("disabled pool must keep serving OAuth layer globally: %+v", resp)
	}
}

// With the pool enabled but no policy applied, both layers stay usable
// (empty-policy passthrough preserved under layering).
func TestServicePick_EmptyPolicyBothLayers(t *testing.T) {
	svc := newLayeredService(t, true, nil)

	resp := svc.Pick(layeredRequest("abcd1234", oauthCandidate("oauth-1", 0), apiKeyCandidate("key-1", 0)))
	if resp.Decision != "selected" || resp.AuthID != "oauth-1" {
		t.Fatalf("empty policy equal-priority pick = %+v", resp)
	}

	resp = svc.Pick(layeredRequest("abcd1234", apiKeyCandidate("key-1", 0)))
	if resp.Decision != "selected" || resp.AuthID != "key-1" {
		t.Fatalf("empty policy api-key only pick = %+v", resp)
	}
}

// Pool rejections surface only when no layer can serve the request, keeping
// the original error codes for genuine pool failures.
func TestServicePick_PoolRejectWhenNoLayerServes(t *testing.T) {
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}},
		[]Member{{PoolID: "eng", AuthID: "auth-oauth", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}},
	)
	svc := newLayeredService(t, true, &p)

	resp := svc.Pick(layeredRequest("abcd1234", oauthCandidate("other-oauth", 0)))
	if resp.Decision != "reject" || resp.ErrorCode != "account_pool_unavailable" {
		t.Fatalf("genuine pool failure must keep its error code: %+v", resp)
	}

	resp = svc.Pick(layeredRequest("abcd1234"))
	if resp.Decision != "reject" || !resp.Retryable {
		t.Fatalf("empty candidate set must reject retryable: %+v", resp)
	}
}

// Non-codex providers remain untouched.
func TestServicePick_NonCodexNotHandled(t *testing.T) {
	svc := newLayeredService(t, false, nil)
	req := layeredRequest("", apiKeyCandidate("key-1", 25))
	req.Provider = "claude"
	if resp := svc.Pick(req); resp.Handled {
		t.Fatalf("non-codex pick unexpectedly handled: %+v", resp)
	}
}

func TestSplitSchedulerCandidates(t *testing.T) {
	apiKeys, oauths := splitSchedulerCandidates([]schedulerAuthCandidate{
		{ID: "a", Attributes: map[string]string{"source": "config:codex[sk-1]"}},
		{ID: "b", Attributes: map[string]string{"config_index": "3"}},
		{ID: "c", Attributes: map[string]string{"auth_kind": "apikey"}},
		{ID: "d", Attributes: map[string]string{"source": "/data/auths/codex.json"}},
		{ID: "e"},
	})
	if len(apiKeys) != 3 || len(oauths) != 2 {
		t.Fatalf("split = apiKeys=%d oauths=%d, want 3/2", len(apiKeys), len(oauths))
	}
	if apiKeys[0].ID != "a" || apiKeys[1].ID != "b" || apiKeys[2].ID != "c" {
		t.Fatalf("api-key layer ids = %v, want a,b,c", []string{apiKeys[0].ID, apiKeys[1].ID, apiKeys[2].ID})
	}
	if oauths[0].ID != "d" || oauths[1].ID != "e" {
		t.Fatalf("oauth layer ids = %v, want d,e", []string{oauths[0].ID, oauths[1].ID})
	}
}

// Admission stays independent of pool scheduling and of the layered pick path.
func TestServicePick_LayeredSelectionStillAdmits(t *testing.T) {
	svc := newLayeredService(t, false, nil)
	resp := svc.Pick(layeredRequest("", apiKeyCandidate("key-1", 25)))
	if resp.Decision != "selected" || resp.AuthID != "key-1" {
		t.Fatalf("pick = %+v", resp)
	}
	if result := svc.AdmitIntercept("req-1", map[string]any{metadataSelectedAuthID: "key-1"}); result != nil {
		t.Fatalf("unlimited account must admit, got %+v", result)
	}
	if resp.HTTPStatus != 0 && resp.HTTPStatus < http.StatusOK {
		t.Fatalf("unexpected status %d", resp.HTTPStatus)
	}
}
