package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/accountpool"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/intercept"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
)

func TestPoolReservationIDIsHostOwnedAndStrippedAfterAuth(t *testing.T) {
	pluginState = state.New()
	root := filepath.ToSlash(t.TempDir())
	t.Cleanup(func() {
		_, _ = handleMethod("plugin.shutdown", nil)
		pluginState = state.New()
	})
	yaml := "data_dir: " + root + "\naccount_pool.enabled: true\n"
	rawRegister, _ := json.Marshal(lifecycleRequest{ConfigYAML: []byte(yaml), SchemaVersion: 2})
	if _, err := handleMethod("plugin.register", rawRegister); err != nil {
		t.Fatal(err)
	}
	request := intercept.Request{
		RequestID: "host-execution-id", SourceFormat: "openai", Model: "test",
		Body:     []byte(`{"input":"hello"}`),
		Headers:  http.Header{http.CanonicalHeaderKey(accountpool.PickRequestIDHeader): {"client-forged-id"}, "X-Session-Id": {"conversation"}},
		Metadata: map[string]any{"request_path": "/v1/responses", "quota_key_hash": "abcd1234"},
	}
	dispatch := func(method string, req intercept.Request) intercept.Response {
		t.Helper()
		payload, _ := json.Marshal(req)
		raw, err := handleMethod(method, payload)
		if err != nil {
			t.Fatal(err)
		}
		var result struct {
			OK     bool               `json:"ok"`
			Result intercept.Response `json:"result"`
		}
		if err := json.Unmarshal(raw, &result); err != nil || !result.OK {
			t.Fatalf("dispatch %s: %s, err=%v", method, raw, err)
		}
		return result.Result
	}
	before := dispatch("request.intercept_before", request)
	if id := before.Headers.Get(accountpool.PickRequestIDHeader); id != request.RequestID {
		t.Fatalf("host must overwrite client-controlled correlation: %q", id)
	}
	request.Headers.Set(accountpool.PickRequestIDHeader, before.Headers.Get(accountpool.PickRequestIDHeader))
	svc := pluginState.AccountPool()
	var pickRequest accountpool.SchedulerPickRequest
	if err := json.Unmarshal([]byte(`{"Provider":"codex","Candidates":[{"ID":"auth-x"}]}`), &pickRequest); err != nil {
		t.Fatal(err)
	}
	pickRequest.Options.Headers = request.Headers
	pickRequest.Options.Metadata = request.Metadata
	pick := svc.Pick(pickRequest)
	if pick.AuthID != "auth-x" || svc.StateSnapshot("auth-x").Reserved != 1 {
		t.Fatalf("pick must carry host execution id: %+v", pick)
	}
	request.Metadata["selected_auth_id"] = "auth-x"
	after := dispatch("request.intercept_after", request)
	removed := false
	for _, name := range after.ClearHeaders {
		removed = removed || strings.EqualFold(name, accountpool.PickRequestIDHeader)
	}
	if !removed || after.Terminate {
		t.Fatalf("correlation header must be removed before upstream: %+v", after)
	}
	// Match the host contract: remove ClearHeaders first, then merge Headers.
	// Checking only the clear list misses accidental reintroduction.
	merged := request.Headers.Clone()
	for _, name := range after.ClearHeaders {
		merged.Del(name)
	}
	for name, values := range after.Headers {
		merged.Del(name)
		for _, value := range values {
			merged.Add(name, value)
		}
	}
	for name := range merged {
		if strings.EqualFold(name, accountpool.PickRequestIDHeader) {
			t.Fatalf("host clear-then-merge must not reintroduce private header: %q", name)
		}
	}
	if snapshot := svc.StateSnapshot("auth-x"); snapshot.Reserved != 0 || snapshot.Active != 1 {
		t.Fatalf("real dispatch must consume its own pick: %+v", snapshot)
	}
	svc.Complete(request.RequestID)
}
