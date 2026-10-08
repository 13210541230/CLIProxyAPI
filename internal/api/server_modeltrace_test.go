package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	management "github.com/router-for-me/CLIProxyAPI/v7/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type modelTraceStopExecutor struct{ entered chan struct{} }

func (e *modelTraceStopExecutor) ExecuteModelStream(ctx context.Context, req handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
	e.entered <- struct{}{}
	chunks := make(chan handlers.ModelExecutionChunk)
	go func() { <-ctx.Done(); close(chunks) }()
	return handlers.ModelExecutionStream{StatusCode: 200, Chunks: chunks}, nil
}

func TestModelTraceServerStopCancelsChargedWork(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	a, err := m.Register(context.Background(), &coreauth.Auth{ID: t.Name(), Provider: "codex", Metadata: map[string]any{"account_id": "synthetic"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: "gpt-5.5"}})
	defer registry.GetGlobalRegistry().UnregisterClient(a.ID)
	h := management.NewHandler(&config.Config{}, filepath.Join(t.TempDir(), "config.yaml"), m)
	e := &modelTraceStopExecutor{entered: make(chan struct{}, 3)}
	h.InitializeModelTrace(e)
	defer func() { _ = h.StopModelTrace(context.Background()) }()
	r := gin.New()
	r.POST("/start", h.StartModelTrace)
	r.GET("/record", h.GetModelTraceRecord)
	index := a.EnsureIndex()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/start", strings.NewReader(`{"auth_index":"`+index+`","model":"gpt-5.5"}`)))
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	for range 3 {
		<-e.entered
	}
	s := &Server{server: &http.Server{}, mgmt: h}
	if err := s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/record?auth_index="+index, nil))
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"status":"cancelled"`) {
		t.Fatal(w.Code, w.Body)
	}
}

func TestModelTraceRoutesRequireManagementAuthentication(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	cfg.RemoteManagement.SecretKey = "configured-test-only"
	h := management.NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), nil)
	h.SetLocalPassword("isolated-key")
	h.InitializeModelTrace(nil)
	defer func() { _ = h.StopModelTrace(context.Background()) }()
	s := &Server{engine: gin.New(), cfg: cfg, mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	for _, route := range []struct{ method, path string }{{"GET", "/auth-files/modeltrace"}, {"POST", "/auth-files/modeltrace"}, {"DELETE", "/auth-files/modeltrace"}, {"GET", "/auth-files/modeltrace/record"}} {
		req := httptest.NewRequest(route.method, "/v0/management"+route.path, strings.NewReader(`{}`))
		req.RemoteAddr = "127.0.0.1:1234"
		w := httptest.NewRecorder()
		s.engine.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized && w.Code != http.StatusForbidden {
			t.Fatalf("unprotected %s: %d", route.path, w.Code)
		}
	}
	req := httptest.NewRequest("GET", "/v0/management/auth-files/modeltrace", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer isolated-key")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body)
	}
}
