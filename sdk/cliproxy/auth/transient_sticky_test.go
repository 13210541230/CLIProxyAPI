package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// stickyTransientExecutor fails the first two Execute/ExecuteStream calls with
// a transient 502 and succeeds afterwards. It also records which credential
// each attempt was handed so the test can assert rotation never happened.
type stickyTransientExecutor struct {
	identifier  string
	execCalls   atomic.Int32
	streamCalls atomic.Int32

	mu       sync.Mutex
	authSeen []string
}

func (e *stickyTransientExecutor) recordAuth(id string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.authSeen = append(e.authSeen, id)
}

func (e *stickyTransientExecutor) Identifier() string { return e.identifier }

func (e *stickyTransientExecutor) ShouldPrepareRequestAuth(*Auth) bool { return false }
func (e *stickyTransientExecutor) PrepareRequestAuth(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *stickyTransientExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordAuth(auth.ID)
	e.execCalls.Add(1)
	if n := e.execCalls.Load(); n <= 2 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream stream ended with incomplete SSE data frame"}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *stickyTransientExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *stickyTransientExecutor) ExecuteStream(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordAuth(auth.ID)
	e.streamCalls.Add(1)
	if n := e.streamCalls.Load(); n <= 2 {
		return nil, &Error{HTTPStatus: http.StatusBadGateway, Message: "upstream stream ended with incomplete SSE data frame"}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *stickyTransientExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *stickyTransientExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

func assertSameCredentialSticky(t *testing.T, exec *stickyTransientExecutor, authA *Auth, minCalls int) {
	t.Helper()
	exec.mu.Lock()
	defer exec.mu.Unlock()
	if len(exec.authSeen) == 0 {
		t.Fatal("executor never called")
	}
	for _, id := range exec.authSeen {
		if id != authA.ID {
			t.Fatalf("executor was handed credential %q after transient failures, want sticky to same credential %q", id, authA.ID)
		}
	}
	if exec.execCalls.Load()+exec.streamCalls.Load() < int32(minCalls) {
		t.Fatalf("calls = %d, want at least %d (original attempt plus same-credential retries)", exec.execCalls.Load()+exec.streamCalls.Load(), minCalls)
	}
}

func TestExecuteStream_TransientUpstreamStaysOnSameCredential(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	m.SetTransientCredentialRetries(2)

	exec := &stickyTransientExecutor{identifier: "openai-compatible"}
	m.RegisterExecutor(exec)

	// Two credentials for the same provider/model so a rotation target exists.
	authA := &Auth{ID: "auth-sticky-a", Provider: "openai-compatible"}
	authB := &Auth{ID: "auth-sticky-b", Provider: "openai-compatible"}
	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "mimo-v2.5"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "mimo-v2.5"}})
	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register b: %v", err)
	}

	result, err := m.ExecuteStream(context.Background(), []string{"openai-compatible"},
		cliproxyexecutor.Request{Model: "mimo-v2.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("ExecuteStream: %v", err)
	}
	if result == nil {
		t.Fatal("missing result")
	}
	assertSameCredentialSticky(t, exec, authA, 3)
}

func TestExecute_TransientUpstreamStaysOnSameCredential(t *testing.T) {
	prev := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(prev) })

	m := NewManager(nil, nil, nil)
	m.SetTransientCredentialRetries(2)

	exec := &stickyTransientExecutor{identifier: "openai-compatible"}
	m.RegisterExecutor(exec)

	authA := &Auth{ID: "auth-sticky-exec-a", Provider: "openai-compatible"}
	authB := &Auth{ID: "auth-sticky-exec-b", Provider: "openai-compatible"}
	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "mimo-v2.5"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "mimo-v2.5"}})
	if _, err := m.Register(context.Background(), authA); err != nil {
		t.Fatalf("register a: %v", err)
	}
	if _, err := m.Register(context.Background(), authB); err != nil {
		t.Fatalf("register b: %v", err)
	}

	resp, err := m.Execute(context.Background(), []string{"openai-compatible"},
		cliproxyexecutor.Request{Model: "mimo-v2.5"}, cliproxyexecutor.Options{})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("payload = %q, want ok", resp.Payload)
	}
	assertSameCredentialSticky(t, exec, authA, 3)
}
