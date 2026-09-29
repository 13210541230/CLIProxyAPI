package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestAfterAuthInterceptorReturnsExecutorOverrideWithSelectedAuth(t *testing.T) {
	selected := &Auth{ID: "codex-auth-1", Provider: "codex"}
	var observed cliproxyexecutor.RequestAfterAuthInterceptRequest
	opts := cliproxyexecutor.Options{
		Metadata: map[string]any{"requested_model": "gpt-5.6-sol"},
		RequestAfterAuthInterceptor: func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			observed = request
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{ExecutorProvider: "oai-basispoints"}
		},
	}

	_, _, target, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "codex", selected,
		cliproxyexecutor.Request{Model: "gpt-5.6-sol"}, opts, "gpt-5.6-sol", true)
	if err != nil {
		t.Fatalf("applyRequestAfterAuthInterceptor() error = %v", err)
	}
	if target != "oai-basispoints" {
		t.Fatalf("executor override = %q, want oai-basispoints", target)
	}
	if observed.AuthID != selected.ID || observed.AuthProvider != selected.Provider || !observed.AllowExecutorOverride {
		t.Fatalf("after-auth identity/route permission = (%q, %q, %v), want selected Codex auth and route enabled", observed.AuthID, observed.AuthProvider, observed.AllowExecutorOverride)
	}
}

func TestAfterAuthInterceptorCannotOverrideCountTokenExecutor(t *testing.T) {
	selected := &Auth{ID: "codex-auth-1", Provider: "codex"}
	called := false
	opts := cliproxyexecutor.Options{
		RequestAfterAuthInterceptor: func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			called = true
			if request.AllowExecutorOverride {
				t.Fatal("count-token interception unexpectedly allowed an executor override")
			}
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{ExecutorProvider: "oai-basispoints"}
		},
	}

	_, _, target, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "codex", selected,
		cliproxyexecutor.Request{}, opts, "gpt-5.6-sol", false)
	if err != nil {
		t.Fatalf("applyRequestAfterAuthInterceptor() error = %v", err)
	}
	if !called {
		t.Fatal("after-auth interceptor was not invoked")
	}
	if target != "" {
		t.Fatalf("count-token executor override = %q, want none", target)
	}
}

func TestExecutionExecutorOverrideResolvesAndFailsClosed(t *testing.T) {
	codex := &executorOverrideTestExecutor{id: "codex"}
	bps := &executorOverrideTestExecutor{id: "oai-basispoints"}
	manager := &Manager{executors: map[string]ProviderExecutor{"codex": codex, "oai-basispoints": bps}}

	selected, provider, err := manager.executionExecutorForProvider(codex, "codex", "oai-basispoints")
	if err != nil {
		t.Fatalf("executionExecutorForProvider() error = %v", err)
	}
	if selected != bps || provider != "oai-basispoints" {
		t.Fatalf("execution target = (%v, %q), want BPS executor", selected, provider)
	}

	if _, _, err := manager.executionExecutorForProvider(codex, "codex", "missing-provider"); err == nil {
		t.Fatal("missing override executor unexpectedly fell back to the selected executor")
	}
	selected, provider, err = manager.executionExecutorForProvider(codex, "codex", "")
	if err != nil || selected != codex || provider != "codex" {
		t.Fatalf("empty override target = (%v, %q, %v), want unchanged Codex executor", selected, provider, err)
	}
}

type executorOverrideTestExecutor struct{ id string }

func (e *executorOverrideTestExecutor) Identifier() string { return e.id }
func (*executorOverrideTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*executorOverrideTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return &cliproxyexecutor.StreamResult{}, nil
}
func (*executorOverrideTestExecutor) Refresh(context.Context, *Auth) (*Auth, error) { return nil, nil }
func (*executorOverrideTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (*executorOverrideTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
