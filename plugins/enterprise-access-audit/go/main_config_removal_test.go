package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/accountpool"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/intercept"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
)

// TestRemovingConfigThenImmediateRequest covers the review's remaining timing
// gap: removing the account-pool enablement (switch gone, exclusive claim
// gone) must take effect on the very next dispatch with no grace period:
//
//  1. removal drops the scheduler capability so the host restores its native
//     fast path (claim removal is a deliberate release, not an accidental
//     failure), while a pick that still reaches the plugin delegates to the
//     builtin scheduler instead of a stale pool selection or fail-closed,
//  2. the next request is not gated even though the persisted limits stay
//     loaded (data_dir is kept, so a pass proves the Enabled bypass rather
//     than a data-directory move), and the limits file survives on disk,
//  3. re-enabling restores the capability, pool selection, and gating with
//     the in-flight slot from before the removal still honored — state is
//     preserved across the removal round-trip,
//  4. deleting the whole config block behaves the same as (1): capability
//     dropped, stale picks delegate, immediate requests pass.
//
// The enabled:false + retained-claim timing is locked separately by
// TestDisablingPoolImmediatelyRestoresBuiltinAndStopsLimits.
func TestRemovingConfigThenImmediateRequest(t *testing.T) {
	pluginState = state.New()
	root := t.TempDir()
	limitsFile := filepath.Join(root, "pool", "account-pool-limits.json")
	t.Cleanup(func() {
		if _, err := handleMethod("plugin.shutdown", nil); err != nil {
			t.Errorf("plugin.shutdown error = %v", err)
		}
		pluginState = state.New()
	})

	base := "data_dir: " + filepath.ToSlash(root) + "\n" +
		"database_path: " + filepath.ToSlash(filepath.Join(root, "audit.sqlite")) + "\n"
	dataDirYAML := "account_pool.data_dir: " + filepath.ToSlash(filepath.Join(root, "pool")) + "\n"
	enabledYAML := "account_pool.enabled: true\n" +
		dataDirYAML +
		"account_pool.max_wait_seconds: 1\n" +
		"exclusive-scheduler-providers: [codex]\n"
	configure := func(extra, method string) registration {
		t.Helper()
		rawRequest, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte(base + extra), SchemaVersion: 2})
		if errMarshal != nil {
			t.Fatalf("marshal lifecycle request: %v", errMarshal)
		}
		raw, errHandle := handleMethod(method, rawRequest)
		if errHandle != nil {
			t.Fatalf("%s error = %v", method, errHandle)
		}
		var result envelope
		if errUnmarshal := json.Unmarshal(raw, &result); errUnmarshal != nil || !result.OK {
			t.Fatalf("%s response = %s, error=%v", method, raw, errUnmarshal)
		}
		var reg registration
		if errDecode := json.Unmarshal(result.Result, &reg); errDecode != nil {
			t.Fatalf("decode registration: %v", errDecode)
		}
		return reg
	}
	interceptAfter := func(requestID string) intercept.Response {
		t.Helper()
		rawRequest, errMarshal := json.Marshal(intercept.Request{
			RequestID:    requestID,
			SourceFormat: "openai",
			Body:         []byte(`{"messages":[{"role":"user","content":"hello"}]}`),
			Metadata: map[string]any{
				"request_path":     "/v1/chat/completions",
				"quota_key_hash":   "deadbeef",
				"selected_auth_id": "auth-x",
			},
		})
		if errMarshal != nil {
			t.Fatalf("marshal intercept request: %v", errMarshal)
		}
		raw, errHandle := handleMethod("request.intercept_after", rawRequest)
		if errHandle != nil {
			t.Fatalf("request.intercept_after error = %v", errHandle)
		}
		var result struct {
			OK     bool               `json:"ok"`
			Result intercept.Response `json:"result"`
		}
		if errUnmarshal := json.Unmarshal(raw, &result); errUnmarshal != nil || !result.OK {
			t.Fatalf("request.intercept_after response = %s, error=%v", raw, errUnmarshal)
		}
		return result.Result
	}
	pick := func(want string) accountpool.SchedulerPickResponse {
		t.Helper()
		pickRaw, errPick := handleMethod("scheduler.pick", []byte(`{"Provider":"codex","Candidates":[{"ID":"auth-x"}]}`))
		if errPick != nil {
			t.Fatalf("scheduler.pick error = %v", errPick)
		}
		var pickEnvelope struct {
			OK     bool                              `json:"ok"`
			Result accountpool.SchedulerPickResponse `json:"result"`
		}
		if errUnmarshal := json.Unmarshal(pickRaw, &pickEnvelope); errUnmarshal != nil || !pickEnvelope.OK {
			t.Fatalf("scheduler.pick response = %s, error=%v", pickRaw, errUnmarshal)
		}
		got := pickEnvelope.Result.Decision
		if want != "" && got != want {
			t.Fatalf("pick decision = %q (%+v), want %q", got, pickEnvelope.Result, want)
		}
		return pickEnvelope.Result
	}
	assertCapability := func(reg registration, want bool, context string) {
		t.Helper()
		if reg.Capabilities.Scheduler != want {
			t.Fatalf("%s: scheduler capability = %v, want %v (%+v)", context, reg.Capabilities.Scheduler, want, reg.Capabilities)
		}
		if want {
			if len(reg.Capabilities.SchedulerExclusiveProviders) != 1 || reg.Capabilities.SchedulerExclusiveProviders[0] != "codex" {
				t.Fatalf("%s: exclusive providers = %v, want [codex]", context, reg.Capabilities.SchedulerExclusiveProviders)
			}
		} else if len(reg.Capabilities.SchedulerExclusiveProviders) != 0 {
			t.Fatalf("%s: claim is still advertised: %v", context, reg.Capabilities.SchedulerExclusiveProviders)
		}
	}

	// Phase 1: enabled pool with the exclusive claim and a live limit.
	reg := configure(enabledYAML, "plugin.register")
	assertCapability(reg, true, "register")
	svc := pluginState.AccountPool()
	if svc == nil {
		t.Fatal("account pool service is unavailable after register")
	}
	if errPut := svc.PutLimits([]accountpool.AccountLimit{{AuthID: "auth-x", Limit: 1}}); errPut != nil {
		t.Fatalf("PutLimits() error = %v", errPut)
	}

	// Phase 2: the limit gates while enabled (baseline for everything below).
	if result := interceptAfter("removal-before-1"); result.Terminate {
		t.Fatalf("first request should pass while enabled: %+v", result)
	}
	if result := interceptAfter("removal-before-2"); !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("configured limit should reject the second request while enabled: %+v", result)
	}

	// Phase 3: remove the enablement and the exclusive claim (data_dir is
	// kept so the limits stay loaded — a pass below must come from the
	// Enabled bypass, not from a data-directory change), then dispatch on the
	// very next request with no restart and no grace period.
	reg = configure(dataDirYAML, "plugin.reconfigure")
	assertCapability(reg, false, "removal reconfigure")
	if resp := pick("delegate_builtin"); resp.DelegateBuiltin != "round-robin" {
		t.Fatalf("pick after removal must delegate to builtin round-robin: %+v", resp)
	}
	if result := interceptAfter("removal-immediately"); result.Terminate {
		t.Fatalf("persisted concurrency limit must not gate requests after removal: %+v", result)
	}
	if _, errStat := os.Stat(limitsFile); errStat != nil {
		t.Fatalf("persisted limits file must survive config removal: %v", errStat)
	}

	// Phase 4: re-enabling restores capability, pool selection, and gating —
	// including the in-flight slot held by removal-before-1, proving engine
	// state survived the removal round-trip.
	reg = configure(enabledYAML, "plugin.reconfigure")
	assertCapability(reg, true, "re-enable reconfigure")
	svc = pluginState.AccountPool()
	if svc == nil {
		t.Fatal("account pool service is unavailable after re-enable")
	}
	if resp := pick("selected"); resp.AuthID != "auth-x" {
		t.Fatalf("pick after re-enable = %+v, want auth-x from the pool path", resp)
	}
	if result := interceptAfter("after-reenable-1"); !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("in-flight slot from before the removal must still be honored after re-enable: %+v", result)
	}
	svc.Complete("removal-before-1")
	if result := interceptAfter("after-reenable-2"); result.Terminate {
		t.Fatalf("gating should admit once the pre-removal slot is released: %+v", result)
	}

	// Phase 5: deleting the whole account-pool block behaves like (1).
	reg = configure("", "plugin.reconfigure")
	assertCapability(reg, false, "full block removal")
	if resp := pick("delegate_builtin"); resp.DelegateBuiltin != "round-robin" {
		t.Fatalf("pick after full block removal must delegate to builtin round-robin: %+v", resp)
	}
	if result := interceptAfter("after-full-removal"); result.Terminate {
		t.Fatalf("full block removal must not gate the immediate next request: %+v", result)
	}
}
