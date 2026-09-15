package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

const transientUpstreamRateLimitBody = `{"code":null,"message":"Rate limit exceeded. Please try again later.","param":null,"type":"upstream_error","upstream_body":"{\"type\":\"error\",\"error\":{\"type\":\"FreeUsageLimit Error\",\"message\":\"Rate limit exceeded. Please try again later.\"},\"metadata\":{}}","upstream_status":429}`

type transientUpstreamRateLimitExecutor struct {
	identifier string

	mu      sync.Mutex
	execute int
	stream  int
	count   int
}

func (e *transientUpstreamRateLimitExecutor) Identifier() string { return e.identifier }

func (e *transientUpstreamRateLimitExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.execute++
	call := e.execute
	e.mu.Unlock()
	if call == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: transientUpstreamRateLimitBody}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *transientUpstreamRateLimitExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.mu.Lock()
	e.stream++
	call := e.stream
	e.mu.Unlock()
	if call == 1 {
		return nil, &Error{HTTPStatus: http.StatusTooManyRequests, Message: transientUpstreamRateLimitBody}
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Payload: []byte("ok")}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *transientUpstreamRateLimitExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.count++
	call := e.count
	e.mu.Unlock()
	if call == 1 {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: transientUpstreamRateLimitBody}
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*transientUpstreamRateLimitExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*transientUpstreamRateLimitExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, http.ErrNotSupported
}

func TestTransientUpstreamRateLimitDoesNotLeaveModelCooldown(t *testing.T) {
	err := &Error{HTTPStatus: http.StatusTooManyRequests, Message: transientUpstreamRateLimitBody}
	if !isTransientUpstreamResultError(err) {
		t.Fatal("expected wrapped upstream 429 to be classified as transient")
	}
	if !shouldSkipCredentialCooldown(err) {
		t.Fatal("expected wrapped upstream 429 to skip credential/model cooldown")
	}
	plain := &Error{HTTPStatus: http.StatusTooManyRequests, Message: "Rate limit exceeded"}
	if isTransientUpstreamResultError(plain) || shouldSkipCredentialCooldown(plain) {
		t.Fatal("plain provider 429 must retain quota cooldown behavior")
	}

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "transient-rate-limit-state", Provider: "transient-rate-limit"}
	model := "transient-rate-limit-model"
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    model,
		Success:  false,
		Error:    err,
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() || updated.Quota.Exceeded {
		t.Fatalf("transient upstream 429 cooled the credential: %+v", updated)
	}
	if state := updated.ModelStates[model]; state != nil {
		if state.Unavailable || !state.NextRetryAfter.IsZero() || state.LastError != nil {
			t.Fatalf("transient upstream 429 left model cooldown state: %+v", state)
		}
	}
}

func TestRegisterClearsPersistedWrappedUpstreamRateLimitCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	model := "transient-rate-limit-persisted-model"
	auth := &Auth{
		ID:             "transient-rate-limit-persisted",
		Provider:       "transient-rate-limit",
		Status:         StatusError,
		StatusMessage:  transientUpstreamRateLimitBody,
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(12 * time.Hour),
		LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: transientUpstreamRateLimitBody},
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "credential_quota",
			NextRecoverAt: time.Now().Add(12 * time.Hour),
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(12 * time.Hour),
				LastError:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: transientUpstreamRateLimitBody},
			},
		},
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() || updated.LastError != nil || updated.Quota.Exceeded || !updated.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("persisted wrapped upstream 429 left credential cooldown: %+v", updated)
	}
	if state := updated.ModelStates[model]; state == nil {
		t.Fatal("persisted model state disappeared unexpectedly")
	} else if state.Unavailable || !state.NextRetryAfter.IsZero() || state.LastError != nil {
		t.Fatalf("persisted wrapped upstream 429 left model cooldown: %+v", state)
	}
}

func TestTransientUpstreamRateLimitRetriesAllExecutionPaths(t *testing.T) {
	paths := []struct {
		name string
		call func(*Manager, cliproxyexecutor.Request) error
		got  func(*transientUpstreamRateLimitExecutor) int
	}{
		{
			name: "execute",
			call: func(manager *Manager, request cliproxyexecutor.Request) error {
				_, errExecute := manager.Execute(context.Background(), []string{"transient-rate-limit"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
			got: func(executor *transientUpstreamRateLimitExecutor) int {
				executor.mu.Lock()
				defer executor.mu.Unlock()
				return executor.execute
			},
		},
		{
			name: "stream",
			call: func(manager *Manager, request cliproxyexecutor.Request) error {
				result, errExecute := manager.ExecuteStream(context.Background(), []string{"transient-rate-limit"}, request, cliproxyexecutor.Options{Stream: true})
				if errExecute != nil {
					return errExecute
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						return chunk.Err
					}
				}
				return nil
			},
			got: func(executor *transientUpstreamRateLimitExecutor) int {
				executor.mu.Lock()
				defer executor.mu.Unlock()
				return executor.stream
			},
		},
		{
			name: "count tokens",
			call: func(manager *Manager, request cliproxyexecutor.Request) error {
				_, errExecute := manager.ExecuteCount(context.Background(), []string{"transient-rate-limit"}, request, cliproxyexecutor.Options{})
				return errExecute
			},
			got: func(executor *transientUpstreamRateLimitExecutor) int {
				executor.mu.Lock()
				defer executor.mu.Unlock()
				return executor.count
			},
		},
	}

	for _, path := range paths {
		t.Run(path.name, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(1, 0, 0)
			executor := &transientUpstreamRateLimitExecutor{identifier: "transient-rate-limit"}
			manager.RegisterExecutor(executor)
			auth := &Auth{ID: "transient-rate-limit-" + path.name, Provider: "transient-rate-limit"}
			model := "transient-rate-limit-model-" + path.name
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Fatalf("register auth: %v", errRegister)
			}

			if errExecute := path.call(manager, cliproxyexecutor.Request{Model: model}); errExecute != nil {
				t.Fatalf("execution error = %v, want recovery retry", errExecute)
			}
			if calls := path.got(executor); calls != 2 {
				t.Fatalf("executor calls = %d, want initial 429 plus one retry", calls)
			}
		})
	}
}
