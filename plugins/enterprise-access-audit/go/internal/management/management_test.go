package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

func newTestHandler(t *testing.T) (*Handler, *state.Manager) {
	t.Helper()
	cfg, err := config.Normalize(config.Default(), t.TempDir())
	if err != nil {
		t.Fatalf("config.Normalize() error = %v", err)
	}
	cfg.DatabasePath = filepath.Join(cfg.DataDir, "management.sqlite")
	manager := state.New()
	if err := manager.Configure(context.Background(), cfg); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown() })
	return New(manager, cfg), manager
}

func decodeResponse(t *testing.T, response ManagementResponse, target any) {
	t.Helper()
	if err := json.Unmarshal(response.Body, target); err != nil {
		t.Fatalf("response body %q is invalid JSON: %v", response.Body, err)
	}
}

func TestRoutesAreFixedLiteralPaths(t *testing.T) {
	registration := Routes("/v0/management", "/v0/resource/plugins/enterprise-access-audit")
	if len(registration.Routes) != 7 {
		t.Fatalf("route count = %d", len(registration.Routes))
	}
	if len(registration.Resources) != 1 || registration.Resources[0].Path != "/v0/resource/plugins/enterprise-access-audit/ui" {
		t.Fatalf("resource registration = %+v", registration.Resources)
	}
	for _, route := range registration.Routes {
		if route.Path == "" || route.Path[0] != '/' {
			t.Fatalf("route path = %q", route.Path)
		}
		for _, forbidden := range []string{":", "*", ".."} {
			if contains(route.Path, forbidden) {
				t.Fatalf("route %q contains forbidden %q", route.Path, forbidden)
			}
		}
	}
}

func TestResourceUIIsServedByPluginHandler(t *testing.T) {
	handler, _ := newTestHandler(t)
	response := handler.Handle(context.Background(), ManagementRequest{Method: http.MethodGet, Path: "/v0/resource/plugins/enterprise-access-audit/ui"})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("resource status = %d", response.StatusCode)
	}
	if got := response.Headers.Get("Content-Type"); !strings.HasPrefix(got, "text/html") {
		t.Fatalf("resource content type = %q", got)
	}
	body := string(response.Body)
	if !strings.Contains(body, "cpa-plugin-api-request") ||
		!strings.Contains(body, "document.referrer") ||
		!strings.Contains(body, "}, hostOrigin)") ||
		!strings.Contains(body, "setInterval") ||
		!strings.Contains(body, "formatTimestamp") ||
		!strings.Contains(body, "toLocaleString") ||
		!strings.Contains(body, "用户名搜索") ||
		!strings.Contains(body, "/v0/management/auth-files/models") ||
		!strings.Contains(body, "model-definitions") ||
		!strings.Contains(body, "/policies/batch") ||
		!strings.Contains(body, "批量设置用户限制策略") {
		t.Fatal("resource UI does not contain the expected audit workspace controls")
	}
}

func TestPolicyEndpointsPreserveOmittedFieldsAndRejectRawKeys(t *testing.T) {
	handler, _ := newTestHandler(t)
	ctx := context.Background()
	response := handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: PoliciesPath, Query: url.Values{"key_hash": {"ABCDEF12"}}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("absent policy status = %d", response.StatusCode)
	}
	if got := response.Headers.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("policy cache-control = %q", got)
	}
	var listed struct {
		Policies []policyResponse `json:"policies"`
	}
	decodeResponse(t, response, &listed)
	if len(listed.Policies) != 1 || !listed.Policies[0].AuditEnabled || len(listed.Policies[0].DeniedModels) != 0 {
		t.Fatalf("absent policy = %+v", listed.Policies)
	}

	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: BatchPoliciesPath, Body: []byte(`{"key_hashes":["abcdef12"],"denied_models":[" GPT-4 ","gpt-4-mini"],"audit_enabled":false}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("batch status = %d, body=%s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: PolicyPath, Body: []byte(`{"key_hash":"abcdef12","audit_enabled":true}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("single toggle status = %d, body=%s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: PoliciesPath, Query: url.Values{"key_hash": {"abcdef12"}}})
	decodeResponse(t, response, &listed)
	if len(listed.Policies[0].DeniedModels) != 2 || !listed.Policies[0].AuditEnabled {
		t.Fatalf("single toggle changed denied models = %+v", listed.Policies[0])
	}

	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: PolicyPath, Body: []byte(`{"key_hash":"raw-secret-api-key"}`)})
	if response.StatusCode != http.StatusBadRequest || !contains(string(response.Body), "invalid_input") {
		t.Fatalf("raw key response = %d %s", response.StatusCode, response.Body)
	}
}

func TestAbsentPolicyInheritsDefaultForBatchNullAndSingleOmittedAudit(t *testing.T) {
	handler, _ := newTestHandler(t)
	ctx := context.Background()
	response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: SettingsPath, Body: []byte(`{"default_audit_enabled":false}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("default setting status = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: BatchPoliciesPath, Body: []byte(`{"key_hashes":["abcdef12"],"denied_models":["batch-model"],"audit_enabled":null}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("absent batch null status = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: PolicyPath, Body: []byte(`{"key_hash":"abcdef13","denied_models":["single-model"]}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("absent single omitted status = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: BatchPoliciesPath, Body: []byte(`{"key_hashes":["abcdef14"],"denied_models":["existing-model"],"audit_enabled":true}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("existing seed status = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: BatchPoliciesPath, Body: []byte(`{"key_hashes":["abcdef14"],"denied_models":["existing-batch-model"],"audit_enabled":null}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("existing batch null status = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: PolicyPath, Body: []byte(`{"key_hash":"abcdef14","denied_models":["existing-single-model"]}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("existing single omitted status = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: PoliciesPath, Query: url.Values{"key_hash": {"abcdef12", "abcdef13", "abcdef14"}}})
	var listed struct {
		Policies []policyResponse `json:"policies"`
	}
	decodeResponse(t, response, &listed)
	if len(listed.Policies) != 3 {
		t.Fatalf("listed policy count = %d", len(listed.Policies))
	}
	if listed.Policies[0].AuditEnabled || listed.Policies[1].AuditEnabled {
		t.Fatalf("absent policy default was not inherited: %+v", listed.Policies)
	}
	if !listed.Policies[2].AuditEnabled || len(listed.Policies[2].DeniedModels) != 1 || listed.Policies[2].DeniedModels[0] != "existing-single-model" {
		t.Fatalf("existing policy omitted/null semantics changed: %+v", listed.Policies[2])
	}
}

func TestBatchPolicyIsAtomicAndNullPreservesAudit(t *testing.T) {
	handler, _ := newTestHandler(t)
	ctx := context.Background()
	seed := []byte(`{"key_hashes":["abcdef12","abcdef13"],"denied_models":["model-a"],"audit_enabled":false}`)
	if response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: BatchPoliciesPath, Body: seed}); response.StatusCode != http.StatusOK {
		t.Fatalf("seed batch = %d %s", response.StatusCode, response.Body)
	}
	response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: BatchPoliciesPath, Body: []byte(`{"key_hashes":["abcdef12","bad"],"denied_models":["model-b"],"audit_enabled":null}`)})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid batch status = %d", response.StatusCode)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: PoliciesPath, Query: url.Values{"key_hash": {"abcdef12", "abcdef13"}}})
	var listed struct {
		Policies []policyResponse `json:"policies"`
	}
	decodeResponse(t, response, &listed)
	if len(listed.Policies) != 2 || listed.Policies[0].DeniedModels[0] != "model-a" || listed.Policies[0].AuditEnabled {
		t.Fatalf("atomic rollback failed: %+v", listed.Policies)
	}
	if listed.Policies[1].DeniedModels[0] != "model-a" || listed.Policies[1].AuditEnabled {
		t.Fatalf("second policy changed during rollback: %+v", listed.Policies)
	}
}

func TestAuditFiltersPaginationDetailAndNumericSerialization(t *testing.T) {
	handler, manager := newTestHandler(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if err := manager.WithStore(ctx, func(active *store.Store) error {
		return active.InsertAudit(ctx, store.AuditRecord{ID: 0, KeyHash: "abcdef12", CreatedAt: now.Add(-time.Minute), Model: "model-a", SourceFormat: "openai", RequestID: "one", Outcome: "succeeded", StatusCode: 200, Text: "secret text", TextAvailable: true})
	}); err != nil {
		t.Fatalf("insert text record = %v", err)
	}
	if err := manager.WithStore(ctx, func(active *store.Store) error {
		return active.InsertAudit(ctx, store.AuditRecord{KeyHash: "abcdef12", CreatedAt: now, Model: "model-b", SourceFormat: "openai", RequestID: "two", Outcome: "failed", StatusCode: 500, Text: "", TextAvailable: false, TextUnavailableReason: "numeric_prompt", SecuritySignal: "cyber_policy", SecurityMessage: "blocked"})
	}); err != nil {
		t.Fatalf("insert numeric record = %v", err)
	}
	if err := manager.WithStore(ctx, func(active *store.Store) error {
		return active.InsertAudit(ctx, store.AuditRecord{KeyHash: "abcdef13", CreatedAt: now.Add(-2 * time.Minute), Model: "model-c", SourceFormat: "openai", RequestID: "three", Outcome: "succeeded", StatusCode: 200, Text: "other user", TextAvailable: true})
	}); err != nil {
		t.Fatalf("insert second key record = %v", err)
	}
	response := handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: AuditPath, Query: url.Values{"key_hash": {"abcdef12", "abcdef13"}, "page_size": {"10"}}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("multi-key audit list status = %d %s", response.StatusCode, response.Body)
	}
	var multiKeyPage struct {
		Records []auditResponse `json:"records"`
	}
	decodeResponse(t, response, &multiKeyPage)
	if len(multiKeyPage.Records) != 3 {
		t.Fatalf("multi-key audit records = %d, want 3", len(multiKeyPage.Records))
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: AuditPath, Query: url.Values{"model": {"MODEL-B"}, "outcome": {"failed"}, "security_signal": {"cyber_policy"}, "page_size": {"1"}}})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("audit list status = %d %s", response.StatusCode, response.Body)
	}
	var page struct {
		Records    []auditResponse `json:"records"`
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	decodeResponse(t, response, &page)
	if len(page.Records) != 1 || page.Pagination.Total != 1 || page.Records[0].Text != "" || page.Records[0].TextAvailable || page.Records[0].TextUnavailableReason != "numeric_prompt" || page.Records[0].SecuritySignal != "cyber_policy" || page.Records[0].SecurityMessage != "blocked" {
		t.Fatalf("numeric response = %+v", page)
	}
	id := page.Records[0].ID
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: AuditDetailPath, Query: url.Values{"id": {"999999"}}})
	if response.StatusCode != http.StatusNotFound || !contains(string(response.Body), "not_found") {
		t.Fatalf("missing detail = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: AuditDetailPath, Query: url.Values{"id": {strconvFormat(id)}}})
	if response.StatusCode != http.StatusOK || contains(string(response.Body), "token") || contains(string(response.Body), "raw") {
		t.Fatalf("detail response = %d %s", response.StatusCode, response.Body)
	}
}

func TestSettingsValidationAndMethodErrors(t *testing.T) {
	handler, _ := newTestHandler(t)
	ctx := context.Background()
	response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPost, Path: PoliciesPath})
	if response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("method error = %d", response.StatusCode)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: SettingsPath, Body: []byte(`{"retention_days":0}`)})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid retention = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: SettingsPath, Body: []byte(`{"default_audit_enabled":null}`)})
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("null default audit = %d %s", response.StatusCode, response.Body)
	}
	response = handler.Handle(ctx, ManagementRequest{Method: http.MethodPut, Path: SettingsPath, Body: []byte(`{"retention_days":1,"max_text_bytes":128}`)})
	if response.StatusCode != http.StatusOK {
		t.Fatalf("valid settings = %d %s", response.StatusCode, response.Body)
	}
}

func contains(value, part string) bool {
	return len(value) >= len(part) && strings.Contains(value, part)
}

func strconvFormat(value int64) string {
	return strconv.FormatInt(value, 10)
}
