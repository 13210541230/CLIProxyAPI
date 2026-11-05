package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func newTransientTestManager(t *testing.T) (*Manager, string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	id := registerOverloadAuths(t, m, 1)[0]
	auth, _ := m.GetByID(id)
	auth.Attributes[AttributeAuthKind] = AuthKindOAuth
	if _, err := m.Update(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	return m, id
}

func transientBlocked(t *testing.T, m *Manager, id, model string) bool {
	t.Helper()
	auth, ok := m.GetByID(id)
	if !ok {
		t.Fatal("credential missing")
	}
	blocked, _, _ := isAuthBlockedForModel(auth, model, time.Now())
	return blocked
}

func TestCodexOverloadCooldownRequiresIndependentFailures(t *testing.T) {
	m, id := newTransientTestManager(t)
	result := Result{
		AuthID: id, Provider: "codex", Model: "gpt-5.6-terra",
		Error: &Error{HTTPStatus: http.StatusBadGateway, Message: `{"error":{"code":"server_is_overloaded"}}`},
	}
	for i := 1; i <= 3; i++ {
		m.MarkResult(context.Background(), result)
		blocked := transientBlocked(t, m, id, result.Model)
		if blocked != (i == 3) {
			t.Fatalf("after %d independent overload failures, blocked=%v, want %v", i, blocked, i == 3)
		}
	}
}

func TestTransientFailureHistoryWindowAndOrdering(t *testing.T) {
	base := time.Unix(1000, 0)
	var history transientFailureHistory
	if history.record(base, 1) || history.record(base.Add(30*time.Second), 2) {
		t.Fatal("first two failures must not trigger a cooldown")
	}
	if history.record(base.Add(time.Minute), 3) {
		t.Fatal("expired first failure must not count")
	}
	if !history.record(base.Add(time.Minute+time.Second), 4) {
		t.Fatal("three failures inside the window must trigger a cooldown")
	}
	history.success(5)
	if history.record(base.Add(62*time.Second), 2) {
		t.Fatal("a late failure from before a newer success must not trigger a cooldown")
	}
	if history.record(base.Add(63*time.Second), 6) || history.record(base.Add(64*time.Second), 7) {
		t.Fatal("success must reset prior failures")
	}
	if !history.record(base.Add(65*time.Second), 8) {
		t.Fatal("three new failures after success must trigger a cooldown")
	}
}

func TestCodexStreamRetriesAreOneIndependentFailure(t *testing.T) {
	m, id := newTransientTestManager(t)
	m.SetRetryConfig(0, 0, 1)
	m.SetTransientCredentialRetries(2)
	attempts := 0
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			attempts++
			return nil, overloadStatusError()
		},
	})
	for request := 1; request <= 3; request++ {
		_, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
		if err == nil {
			t.Fatal("expected overload error")
		}
		if got := transientBlocked(t, m, id, "gpt-5.6-terra"); got != (request == 3) {
			t.Fatalf("after request %d, blocked=%v, want %v", request, got, request == 3)
		}
	}
	if attempts != 9 {
		t.Fatalf("upstream attempts=%d, want 9 (3 per independent request)", attempts)
	}
}

func TestCodexStreamRetrySuccessDoesNotPublishTransientFailure(t *testing.T) {
	m, id := newTransientTestManager(t)
	m.SetRetryConfig(0, 0, 1)
	m.SetTransientCredentialRetries(1)
	attempts := 0
	m.RegisterExecutor(&customStreamMockExecutor{
		identifier: "codex",
		streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
			attempts++
			if attempts%2 == 1 {
				return nil, overloadStatusError()
			}
			if transientBlocked(t, m, id, "gpt-5.6-terra") {
				t.Error("failed attempt published cooling before its retry succeeded")
			}
			return successStreamResult(), nil
		},
	})
	for request := 0; request < 4; request++ {
		stream, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
		if err != nil {
			t.Fatalf("retry should succeed: %v", err)
		}
		for chunk := range stream.Chunks {
			if chunk.Err != nil {
				t.Fatal(chunk.Err)
			}
		}
		if transientBlocked(t, m, id, "gpt-5.6-terra") {
			t.Fatal("successful retry left a cooldown")
		}
	}
}

func TestCodexTurnRateLimitWithoutHintRequiresRepeatedRequests(t *testing.T) {
	m, id := newTransientTestManager(t)
	result := Result{
		AuthID: id, Provider: "codex", Model: "gpt-5.6-terra",
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded"}}`},
	}
	for i := 1; i <= 3; i++ {
		m.MarkResult(context.Background(), result)
		if blocked := transientBlocked(t, m, id, result.Model); blocked != (i == 3) {
			t.Fatalf("after %d independent rate limits, blocked=%v, want %v", i, blocked, i == 3)
		}
	}
	auth, _ := m.GetByID(id)
	state := existingModelState(auth, result.Model)
	if state == nil || state.Quota.Exceeded {
		t.Fatal("transient turn rate limit must not be recorded as quota exhaustion")
	}
}

func TestCodexTurnRateLimitHonorsRetryAfter(t *testing.T) {
	m, id := newTransientTestManager(t)
	delay := 30 * time.Second
	result := Result{
		AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", RetryAfter: &delay,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"rate_limit_error","code":"turn_rate_limit_exceeded"}}`},
	}
	m.MarkResult(context.Background(), result)
	if !transientBlocked(t, m, id, result.Model) {
		t.Fatal("an explicit Retry-After must still be honored")
	}
}

func TestCodexTransientDoesNotWeakenQuotaOrAuthCooldown(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusTooManyRequests} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m, id := newTransientTestManager(t)
			result := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: status, Message: "protected error"}}
			m.MarkResult(context.Background(), result)
			if !transientBlocked(t, m, id, result.Model) {
				t.Fatalf("status %d should still cool immediately", status)
			}
			result.Error = &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}
			m.MarkResult(context.Background(), result)
			if !transientBlocked(t, m, id, result.Model) {
				t.Fatal("transient failure removed existing protection")
			}
		})
	}
}
