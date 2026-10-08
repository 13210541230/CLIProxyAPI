package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modeltrace"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type modelTraceMockExecutor struct {
	entered chan handlers.ModelExecutionRequest
}

func (e *modelTraceMockExecutor) ExecuteModelStream(ctx context.Context, r handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
	e.entered <- r
	ch := make(chan handlers.ModelExecutionChunk)
	go func() { <-ctx.Done(); close(ch) }()
	return handlers.ModelExecutionStream{StatusCode: 200, Chunks: ch}, nil
}
func modelTraceTestHandler(t *testing.T) (*Handler, *gin.Engine, *coreauth.Manager, *modelTraceMockExecutor) {
	t.Helper()
	m := coreauth.NewManager(nil, nil, nil)
	a := &coreauth.Auth{ID: t.Name() + ".json", Provider: "codex", Metadata: map[string]any{"account_id": "test-account", "access_token": "NEVER-PERSIST"}, Attributes: map[string]string{"path": "test.json"}, Status: coreauth.StatusActive}
	registered, err := m.Register(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(registered.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	t.Cleanup(func() { reg.UnregisterClient(registered.ID) })
	h := NewHandler(&config.Config{}, filepath.Join(t.TempDir(), "config.yaml"), m)
	executor := &modelTraceMockExecutor{entered: make(chan handlers.ModelExecutionRequest, 6)}
	h.InitializeModelTrace(executor)
	t.Cleanup(func() { _ = h.StopModelTrace(context.Background()) })
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/auth-files/modeltrace", h.GetModelTrace)
	r.POST("/auth-files/modeltrace", h.StartModelTrace)
	r.DELETE("/auth-files/modeltrace", h.CancelModelTrace)
	r.GET("/auth-files/modeltrace/record", h.GetModelTraceRecord)
	return h, r, m, executor
}
func modelTraceRequest(r http.Handler, method, path, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}
func TestModelTraceContractAdmissionPinningAndIdentity(t *testing.T) {
	h, r, m, e := modelTraceTestHandler(t)
	a := m.List()[0]
	index := lockedAuthIndex(a)
	w := modelTraceRequest(r, "GET", "/auth-files/modeltrace", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"states":{}`) || !strings.Contains(w.Body.String(), `"display_name"`) {
		t.Fatal(w.Code, w.Body)
	}
	for _, tc := range []struct {
		body string
		code int
	}{{`{`, 400}, {`{"auth_index":"missing","model":"gpt-5.5"}`, 404}, {`{"auth_index":"` + index + `","model":"unknown"}`, 400}, {`{"auth_index":"` + index + `","model":"gpt-5.4"}`, 400}} {
		w = modelTraceRequest(r, "POST", "/auth-files/modeltrace", tc.body)
		if w.Code != tc.code {
			t.Fatal(w.Code, w.Body)
		}
	}
	body := `{"auth_index":"` + index + `","model":"gpt-5.5"}`
	w = modelTraceRequest(r, "POST", "/auth-files/modeltrace", body)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	for range 3 {
		req := <-e.entered
		if req.AuthID != a.ID || req.ForcedProvider != "codex" || !req.Stream || req.EntryProtocol != "codex" || strings.Contains(string(req.Body), "NEVER-PERSIST") {
			t.Fatal(req)
		}
	}
	w = modelTraceRequest(r, "POST", "/auth-files/modeltrace", body)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	w = modelTraceRequest(r, "DELETE", "/auth-files/modeltrace?auth_index="+index, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"cancelled":true`) {
		t.Fatal(w.Code, w.Body)
	}
	h.modelTrace.Wait()
	w = modelTraceRequest(r, "GET", "/auth-files/modeltrace/record?auth_index="+index, "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) || strings.Contains(w.Body.String(), "NEVER-PERSIST") {
		t.Fatal(w.Code, w.Body)
	}
	var record struct {
		Record modeltrace.Record `json:"record"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &record)
	w = modelTraceRequest(r, "GET", "/auth-files/modeltrace/record?auth_index="+index+"&id=missing", "")
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
	a.Metadata["account_id"] = "replacement-account"
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	w = modelTraceRequest(r, "GET", "/auth-files/modeltrace/record?auth_index="+index+"&id="+record.Record.ID, "")
	if w.Code != 404 {
		t.Fatal("replacement inherited", w.Body)
	}
	a.Disabled = true
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	w = modelTraceRequest(r, "POST", "/auth-files/modeltrace", body)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestModelTraceUnavailableAndMissingIdentity(t *testing.T) {
	h, r, m, _ := modelTraceTestHandler(t)
	a := m.List()[0]
	index := lockedAuthIndex(a)
	a.Metadata = map[string]any{}
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	w := modelTraceRequest(r, "POST", "/auth-files/modeltrace", `{"auth_index":"`+index+`","model":"gpt-5.5"}`)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body)
	}
	h.modelTrace = nil
	w = modelTraceRequest(r, "GET", "/auth-files/modeltrace", "")
	if w.Code != 503 {
		t.Fatal(w.Code)
	}
}
