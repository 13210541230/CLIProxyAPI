package intercept

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/model"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

func newTestHandler(t *testing.T) (*Handler, *state.Manager) {
	t.Helper()
	root := t.TempDir()
	cfg, err := config.Normalize(config.Config{
		DataDir:             root,
		DatabasePath:        filepath.Join(root, "audit.sqlite"),
		RetentionDays:       config.DefaultRetentionDays,
		DefaultAuditEnabled: true,
		MaxTextBytes:        16,
		CleanupInterval:     config.DefaultCleanupInterval,
	}, root)
	if err != nil {
		t.Fatalf("config.Normalize() error = %v", err)
	}
	manager := state.New()
	if err := manager.Configure(context.Background(), cfg); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown() })
	return New(manager, cfg), manager
}

func setDeny(t *testing.T, manager *state.Manager, hash string, denied []string) {
	t.Helper()
	if err := manager.WithStore(context.Background(), func(active *store.Store) error {
		values := append([]string(nil), denied...)
		return active.ReplacePolicies(context.Background(), []model.PolicyPatch{{KeyHash: hash, DeniedModels: &values}})
	}); err != nil {
		t.Fatalf("ReplacePolicies() error = %v", err)
	}
}

func TestPhaseOnePathMatrix(t *testing.T) {
	tests := []struct {
		name, source, path string
		want               bool
	}{
		{"chat", "openai", "/v1/chat/completions", true},
		{"completions", "openai", "/v1/completions", true},
		{"responses", "openai-response", "/v1/responses", true},
		{"codex responses", "codex", "/backend-api/codex/responses", true},
		{"claude", "claude", "/v1/messages", true},
		{"gemini", "gemini", "/v1beta/models/gemini-2:generateContent", true},
		{"gemini stream", "gemini", "/v1beta/models/gemini-2:streamGenerateContent", true},
		{"wrong source", "openai-response", "/v1/chat/completions", false},
		{"count tokens", "claude", "/v1/messages/count_tokens", false},
		{"gemini count tokens", "gemini", "/v1beta/models/gemini-2:countTokens", false},
		{"responses compact", "openai-response", "/v1/responses/compact", false},
		{"session marker", "openai-response", "/v1/responses", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := map[string]any{RequestPathMetadataKey: test.path}
			if test.name == "session marker" {
				metadata[ExecutionSessionMetadataKey] = ""
			}
			_, got := phaseOnePath(test.source, metadata)
			if got != test.want {
				t.Fatalf("phaseOnePath() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestDeniedRequestedAndResolvedModelsTerminate(t *testing.T) {
	handler, manager := newTestHandler(t)
	setDeny(t, manager, "deadbeef", []string{"blocked-model", "resolved-model"})
	base := Request{RequestID: "req-before", SourceFormat: "openai", RequestedModel: "blocked-model", Model: "alias", Body: []byte(`{"messages":[{"role":"user","content":"do not lose"}]}`), Metadata: map[string]any{RequestPathMetadataKey: "/v1/chat/completions", KeyHashMetadataKey: "deadbeef"}}
	got, err := handler.Intercept(context.Background(), base)
	if err != nil || !got.Terminate || got.StatusCode != 403 || string(got.ResponseBody) == "" {
		t.Fatalf("requested denial = %#v, error=%v", got, err)
	}
	if got.ResponseHeaders.Get("Content-Type") != "application/json" {
		t.Fatalf("response content type = %q", got.ResponseHeaders.Get("Content-Type"))
	}
	if string(got.ResponseBody) == "" || !contains(string(got.ResponseBody), `"code":"model_not_allowed"`) {
		t.Fatalf("response body = %s", got.ResponseBody)
	}
	resolved := base
	resolved.RequestID = "req-after"
	resolved.RequestedModel = "allowed-alias"
	resolved.Model = "resolved-model"
	got, err = handler.Intercept(context.Background(), resolved)
	if err != nil || !got.Terminate || got.StatusCode != 403 {
		t.Fatalf("resolved denial = %#v, error=%v", got, err)
	}
	if err := handler.Complete(context.Background(), Completion{RequestID: "req-before", SourceFormat: "openai", Model: "blocked-model", Outcome: "succeeded", StatusCode: 200, Metadata: base.Metadata}); err != nil {
		t.Fatalf("completion after rejection error = %v", err)
	}
	for _, id := range []string{"req-before", "req-after"} {
		record, err := readRecord(manager, id)
		if err != nil || record.Outcome != "rejected" || record.StatusCode != 403 || record.Text != "do not lose" {
			t.Fatalf("record %s = %#v, error=%v", id, record, err)
		}
	}
}

func TestAllowedPassThroughAndLifecycleCorrelation(t *testing.T) {
	handler, manager := newTestHandler(t)
	req := Request{RequestID: "req-success", SourceFormat: "openai", RequestedModel: "allowed", Model: "allowed", Body: []byte(`{"messages":[{"role":"user","content":"hello"}]}`), Metadata: map[string]any{RequestPathMetadataKey: "/v1/chat/completions", KeyHashMetadataKey: "deadbeef"}}
	got, err := handler.Intercept(context.Background(), req)
	if err != nil || got.Terminate || string(got.Body) != string(req.Body) {
		t.Fatalf("pass-through = %#v, error=%v", got, err)
	}
	if err := handler.Complete(context.Background(), Completion{RequestID: req.RequestID, SourceFormat: req.SourceFormat, Model: req.Model, Outcome: "succeeded", StatusCode: 200, Metadata: req.Metadata}); err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	record, err := readRecord(manager, req.RequestID)
	if err != nil || record.Outcome != "succeeded" || record.StatusCode != 200 || !record.TextAvailable || record.Text != "hello" {
		t.Fatalf("success record = %#v, error=%v", record, err)
	}
}

func TestMissingInvalidIdentityAndExcludedPathsDoNotAuditOrEnforce(t *testing.T) {
	handler, manager := newTestHandler(t)
	setDeny(t, manager, "deadbeef", []string{"blocked"})
	tests := []struct {
		name, source, path, hash, session string
	}{
		{"missing hash", "openai", "/v1/chat/completions", "", ""},
		{"invalid hash", "openai", "/v1/chat/completions", "raw-secret", ""},
		{"count tokens", "claude", "/v1/messages/count_tokens", "deadbeef", ""},
		{"gemini count tokens", "gemini", "/v1beta/models/gemini:countTokens", "deadbeef", ""},
		{"responses compact", "openai-response", "/v1/responses/compact", "deadbeef", ""},
		{"codex compact", "openai-response", "/backend-api/codex/responses/compact", "deadbeef", ""},
		{"session response", "openai-response", "/v1/responses", "deadbeef", "ws-1"},
		{"image", "openai", "/v1/images/generations", "deadbeef", ""},
		{"model list", "openai", "/v1/models", "deadbeef", ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := map[string]any{RequestPathMetadataKey: test.path}
			if test.hash != "" {
				metadata[KeyHashMetadataKey] = test.hash
			}
			if test.session != "" {
				metadata[ExecutionSessionMetadataKey] = test.session
			}
			id := "excluded-" + test.name
			got, err := handler.Intercept(context.Background(), Request{RequestID: id, SourceFormat: test.source, RequestedModel: "blocked", Model: "blocked", Body: []byte(`{"messages":[{"role":"user","content":"secret"}]}`), Metadata: metadata})
			if err != nil || got.Terminate {
				t.Fatalf("excluded response = %#v, error=%v", got, err)
			}
			if test.hash == "deadbeef" {
				if _, errRecord := readRecord(manager, id); errRecord == nil {
					t.Fatal("excluded request produced an audit record")
				}
			}
		})
	}
}

func TestNumericPromptAndTextLimitAreMetadataSafe(t *testing.T) {
	handler, manager := newTestHandler(t)
	metadata := map[string]any{RequestPathMetadataKey: "/v1/completions", KeyHashMetadataKey: "deadbeef"}
	if _, err := handler.Intercept(context.Background(), Request{RequestID: "numeric", SourceFormat: "openai", RequestedModel: "model", Body: []byte(`{"prompt":[1,2,3],"raw":"must not persist"}`), Metadata: metadata}); err != nil {
		t.Fatal(err)
	}
	numeric, err := readRecord(manager, "numeric")
	if err != nil || numeric.Text != "" || numeric.TextAvailable || numeric.TextUnavailableReason != "numeric_prompt" {
		t.Fatalf("numeric record = %#v, error=%v", numeric, err)
	}
	long := Request{RequestID: "long", SourceFormat: "openai", RequestedModel: "model", Body: []byte(`{"prompt":"0123456789abcdefghijklmnopqrstuvwxyz"}`), Metadata: metadata}
	if _, err := handler.Intercept(context.Background(), long); err != nil {
		t.Fatal(err)
	}
	bounded, err := readRecord(manager, "long")
	if err != nil || len([]byte(bounded.Text)) > 16 || !bounded.TextTruncated {
		t.Fatalf("bounded record = %#v, error=%v", bounded, err)
	}
}

func TestAdversarialExcludedTextIsNotPersisted(t *testing.T) {
	handler, manager := newTestHandler(t)
	tests := []struct {
		name, source, path, body string
	}{
		{"responses assistant", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","role":"assistant","text":"assistant secret"}]}`},
		{"responses missing role", "openai-response", "/v1/responses", `{"input":[{"type":"input_text","text":"missing role secret"}]}`},
		{"chat missing type", "openai", "/v1/chat/completions", `{"messages":[{"role":"user","content":[{"text":"missing type secret"}]}]}`},
		{"claude media text", "claude", "/v1/messages", `{"messages":[{"role":"user","content":[{"type":"image","text":"image secret"}]}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			id := "adversarial-" + test.name
			metadata := map[string]any{RequestPathMetadataKey: test.path, KeyHashMetadataKey: "deadbeef"}
			if _, err := handler.Intercept(context.Background(), Request{RequestID: id, SourceFormat: test.source, RequestedModel: "model", Body: []byte(test.body), Metadata: metadata}); err != nil {
				t.Fatal(err)
			}
			record, err := readRecord(manager, id)
			if err != nil {
				t.Fatal(err)
			}
			if record.Text != "" || record.TextAvailable {
				t.Fatalf("adversarial text persisted: %#v", record)
			}
		})
	}
}

func TestCompletionOutcomesAreNotSuccess(t *testing.T) {
	handler, manager := newTestHandler(t)
	metadata := map[string]any{RequestPathMetadataKey: "/v1/completions", KeyHashMetadataKey: "deadbeef"}
	for _, outcome := range []string{"failed", "rejected", "canceled"} {
		id := "req-" + outcome
		if _, err := handler.Intercept(context.Background(), Request{RequestID: id, SourceFormat: "openai", RequestedModel: "model", Body: []byte(`{"prompt":"hello"}`), Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
		if err := handler.Complete(context.Background(), Completion{RequestID: id, SourceFormat: "openai", RequestedModel: "model", Outcome: outcome, StatusCode: 500, Metadata: metadata}); err != nil {
			t.Fatal(err)
		}
		record, err := readRecord(manager, id)
		if err != nil || record.Outcome != outcome {
			t.Fatalf("%s record = %#v, error=%v", outcome, record, err)
		}
	}
}

func TestCompletionCyberPolicySignalUsesExplicitUpstreamCode(t *testing.T) {
	handler, manager := newTestHandler(t)
	metadata := map[string]any{RequestPathMetadataKey: "/v1/responses", KeyHashMetadataKey: "deadbeef"}
	request := Request{RequestID: "cyber-policy", SourceFormat: "openai-response", RequestedModel: "model", Body: []byte(`{"input":"security test"}`), Metadata: metadata}
	if _, err := handler.Intercept(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if err := handler.Complete(context.Background(), Completion{
		RequestID: request.RequestID, SourceFormat: request.SourceFormat, RequestedModel: request.RequestedModel,
		Outcome: "failed", StatusCode: 400, Error: `{"error":{"code":"cyber_policy","message":"blocked"}}`, Metadata: metadata,
	}); err != nil {
		t.Fatal(err)
	}
	record, err := readRecord(manager, request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	if record.Outcome != "failed" || record.SecuritySignal != "cyber_policy" || record.SecurityMessage != "blocked" {
		t.Fatalf("cyber policy record = %#v", record)
	}

	genericID := "generic-error"
	genericRequest := request
	genericRequest.RequestID = genericID
	if _, err := handler.Intercept(context.Background(), genericRequest); err != nil {
		t.Fatal(err)
	}
	if err := handler.Complete(context.Background(), Completion{
		RequestID: genericID, SourceFormat: request.SourceFormat, RequestedModel: request.RequestedModel,
		Outcome: "failed", StatusCode: 400, Error: `{"error":{"code":"invalid_request"}}`, Metadata: metadata,
	}); err != nil {
		t.Fatal(err)
	}
	genericRecord, err := readRecord(manager, genericID)
	if err != nil {
		t.Fatal(err)
	}
	if genericRecord.SecuritySignal != "" {
		t.Fatalf("generic error was classified as cyber policy: %#v", genericRecord)
	}

	successID := "successful-response"
	successRequest := request
	successRequest.RequestID = successID
	if _, err := handler.Intercept(context.Background(), successRequest); err != nil {
		t.Fatal(err)
	}
	if err := handler.Complete(context.Background(), Completion{
		RequestID: successID, SourceFormat: request.SourceFormat, RequestedModel: request.RequestedModel,
		Outcome: "succeeded", StatusCode: 200, Error: `{"error":{"code":"cyber_policy"}}`, Metadata: metadata,
	}); err != nil {
		t.Fatal(err)
	}
	successRecord, err := readRecord(manager, successID)
	if err != nil {
		t.Fatal(err)
	}
	if successRecord.SecuritySignal != "" {
		t.Fatalf("successful response was classified as cyber policy: %#v", successRecord)
	}
}

func readRecord(manager *state.Manager, requestID string) (record store.AuditRecord, err error) {
	err = manager.WithStore(context.Background(), func(active *store.Store) error {
		var readErr error
		record, readErr = active.GetAuditByRequestID(context.Background(), requestID)
		return readErr
	})
	return record, err
}

func contains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}
