package main

import (
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/accountpool"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/intercept"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
)

func TestFlatAccountPoolKeysEnableSchedulerRegistration(t *testing.T) {
	root := t.TempDir()
	yaml := "data_dir: " + filepath.ToSlash(root) + "\n" +
		"account_pool.enabled: true\n" +
		"account_pool.data_dir: " + filepath.ToSlash(filepath.Join(root, "pool")) + "\n"
	configYAML := base64.StdEncoding.EncodeToString([]byte(yaml))
	raw, err := handleMethod("plugin.register", []byte(`{"schema_version":2,"config_yaml":"`+configYAML+`"}`))
	if err != nil {
		t.Fatalf("register error = %v", err)
	}
	var envelopeResult envelope
	if err := json.Unmarshal(raw, &envelopeResult); err != nil || !envelopeResult.OK {
		t.Fatalf("registration envelope = %s, error=%v", raw, err)
	}
	var result registration
	if err := json.Unmarshal(envelopeResult.Result, &result); err != nil {
		t.Fatalf("decode registration: %v", err)
	}
	if !result.Capabilities.Scheduler {
		t.Fatal("flat account_pool.enabled did not enable the scheduler capability")
	}
	if len(result.Capabilities.SchedulerExclusiveProviders) != 1 || result.Capabilities.SchedulerExclusiveProviders[0] != "codex" {
		t.Fatalf("exclusive providers = %v, want [codex]", result.Capabilities.SchedulerExclusiveProviders)
	}
	// Close the store so Windows can remove the TempDir, then reset the
	// package-level manager: Shutdown permanently closes it, and the following
	// dispatch test still needs to register.
	_, _ = handleMethod("plugin.shutdown", nil)
	pluginState = state.New()
}

func TestDisablingPoolImmediatelyRestoresBuiltinAndStopsLimits(t *testing.T) {
	pluginState = state.New()
	root := t.TempDir()
	t.Cleanup(func() {
		if _, err := handleMethod("plugin.shutdown", nil); err != nil {
			t.Errorf("plugin.shutdown error = %v", err)
		}
		pluginState = state.New()
	})

	configure := func(enabled bool, method string) envelope {
		t.Helper()
		yaml := "data_dir: " + filepath.ToSlash(root) + "\n" +
			"database_path: " + filepath.ToSlash(filepath.Join(root, "audit.sqlite")) + "\n" +
			"account_pool.enabled: " + map[bool]string{true: "true", false: "false"}[enabled] + "\n" +
			"account_pool.data_dir: " + filepath.ToSlash(filepath.Join(root, "pool")) + "\n" +
			"account_pool.max_wait_seconds: 1\n" +
			"exclusive-scheduler-providers: [codex]\n"
		rawRequest, errMarshal := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml), SchemaVersion: 2})
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
		return result
	}

	configure(true, "plugin.register")
	svc := pluginState.AccountPool()
	if svc == nil {
		t.Fatal("account pool service is unavailable after register")
	}
	if err := svc.PutLimits([]accountpool.AccountLimit{{AuthID: "auth-x", Limit: 1}}); err != nil {
		t.Fatalf("PutLimits() error = %v", err)
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
	if result := interceptAfter("before-disable-1"); result.Terminate {
		t.Fatalf("first request should pass before disabling: %+v", result)
	}
	if result := interceptAfter("before-disable-2"); !result.Terminate || result.StatusCode != 503 {
		t.Fatalf("configured concurrency limit should reject the second request: %+v", result)
	}

	registerResult := configure(false, "plugin.reconfigure")
	var registrationResult registration
	if err := json.Unmarshal(registerResult.Result, &registrationResult); err != nil {
		t.Fatalf("decode reconfigure registration: %v", err)
	}
	if !registrationResult.Capabilities.Scheduler || len(registrationResult.Capabilities.SchedulerExclusiveProviders) != 1 || registrationResult.Capabilities.SchedulerExclusiveProviders[0] != "codex" {
		t.Fatalf("exclusive claim should remain active for safe builtin delegation: %+v", registrationResult.Capabilities)
	}

	pickRaw, err := handleMethod("scheduler.pick", []byte(`{"Provider":"codex","Candidates":[{"ID":"auth-x"}]}`))
	if err != nil {
		t.Fatalf("scheduler.pick error = %v", err)
	}
	var pickEnvelope struct {
		OK     bool                              `json:"ok"`
		Result accountpool.SchedulerPickResponse `json:"result"`
	}
	if err := json.Unmarshal(pickRaw, &pickEnvelope); err != nil || !pickEnvelope.OK {
		t.Fatalf("scheduler.pick response = %s, error=%v", pickRaw, err)
	}
	if pickEnvelope.Result.Decision != "delegate_builtin" || pickEnvelope.Result.DelegateBuiltin != "round-robin" {
		t.Fatalf("disabled pool must immediately delegate selection to CPA: %+v", pickEnvelope.Result)
	}
	if result := interceptAfter("immediately-after-disable"); result.Terminate {
		t.Fatalf("persisted concurrency limit must not gate requests after disable: %+v", result)
	}
}

func TestJSONDispatchWiresInterceptAndCompletion(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() { _, _ = handleMethod("plugin.shutdown", nil) })
	yaml := "data_dir: " + filepath.ToSlash(root) + "\ndatabase_path: " + filepath.ToSlash(filepath.Join(root, "audit.sqlite")) + "\n"
	configYAML := base64.StdEncoding.EncodeToString([]byte(yaml))
	registrationRaw, err := handleMethod("plugin.register", []byte(`{"schema_version":2,"config_yaml":"`+configYAML+`"}`))
	if err != nil {
		t.Fatalf("register error = %v", err)
	}
	var registrationEnvelope envelope
	if err := json.Unmarshal(registrationRaw, &registrationEnvelope); err != nil || !registrationEnvelope.OK {
		t.Fatalf("registration envelope = %s, error=%v", registrationRaw, err)
	}
	managementRaw, err := handleMethod("management.register", []byte(`{"BasePath":"/v0/management","ResourceBasePath":"/v0/resource/plugins/enterprise-access-audit"}`))
	if err != nil || !strings.Contains(string(managementRaw), `enterprise-access-audit/policies`) || !strings.Contains(string(managementRaw), `enterprise-access-audit/ui`) || strings.Contains(string(managementRaw), `:id`) {
		t.Fatalf("management registration = %s, error=%v", managementRaw, err)
	}
	managementRequest := `{"Method":"GET","Path":"/v0/management/enterprise-access-audit/settings","Query":{},"Body":null}`
	managementResponseRaw, err := handleMethod("management.handle", []byte(managementRequest))
	var managementEnvelope struct {
		OK     bool `json:"ok"`
		Result struct {
			StatusCode int    `json:"StatusCode"`
			Body       []byte `json:"Body"`
		} `json:"result"`
	}
	if err != nil || json.Unmarshal(managementResponseRaw, &managementEnvelope) != nil || !managementEnvelope.OK || managementEnvelope.Result.StatusCode != 200 || !strings.Contains(string(managementEnvelope.Result.Body), `retention_days`) {
		t.Fatalf("management dispatch = %s, error=%v", managementResponseRaw, err)
	}
	request := `{"RequestID":"dispatch-1","SourceFormat":"openai","RequestedModel":"allowed","Body":"eyJtZXNzYWdlcyI6W3sicm9sZSI6InVzZXIiLCJjb250ZW50IjoiaGkifV19","Metadata":{"request_path":"/v1/chat/completions","quota_key_hash":"deadbeef"}}`
	interceptRaw, err := handleMethod("request.intercept_before", []byte(request))
	if err != nil {
		t.Fatalf("intercept error = %v", err)
	}
	var interceptEnvelope envelope
	if err := json.Unmarshal(interceptRaw, &interceptEnvelope); err != nil || !interceptEnvelope.OK {
		t.Fatalf("intercept envelope = %s, error=%v", interceptRaw, err)
	}
	completionRaw, err := handleMethod("request.complete", []byte(`{"RequestID":"dispatch-1","SourceFormat":"openai","Model":"allowed","Outcome":"succeeded","StatusCode":200,"Metadata":{"request_path":"/v1/chat/completions","quota_key_hash":"deadbeef"}}`))
	if err != nil || !strings.Contains(string(completionRaw), `"ok":true`) {
		t.Fatalf("completion = %s, error=%v", completionRaw, err)
	}
	_, _ = handleMethod("plugin.shutdown", nil)
}
