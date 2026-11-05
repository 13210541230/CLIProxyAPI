package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestMergeModelStatePreservesTransientFailureHistory(t *testing.T) {
	now := time.Unix(1000, 0)
	live := &ModelState{UpdatedAt: now}
	live.transientFailures.record(now, 1)
	live.transientFailures.record(now.Add(time.Second), 2)
	mergeModelState(live, &ModelState{UpdatedAt: now.Add(2 * time.Second)})
	if !live.transientFailures.record(now.Add(3*time.Second), 3) {
		t.Fatal("merging a restored model state erased live failure evidence")
	}
}

func TestMergeTransientHistoryDeduplicatesAndHonorsSuccess(t *testing.T) {
	now := time.Unix(1000, 0)
	var first, second transientFailureHistory
	first.record(now, 1)
	first.record(now.Add(time.Second), 2)
	second.record(now.Add(time.Second), 2)
	second.record(now.Add(2*time.Second), 3)
	merged := mergeTransientFailureHistories(first, second)
	if merged.record(now.Add(3*time.Second), 3) {
		t.Fatal("a duplicated dispatch must not trigger or extend cooldown")
	}
	merged.success(3)
	merged = mergeTransientFailureHistories(merged, first)
	if merged.record(now.Add(4*time.Second), 4) || merged.record(now.Add(5*time.Second), 5) {
		t.Fatal("merging an older snapshot resurrected failures before a newer success")
	}
	if !merged.record(now.Add(6*time.Second), 6) {
		t.Fatal("three distinct post-success failures should trigger cooldown")
	}
}

func TestCodexTransientMarkResultSupportsNilContext(t *testing.T) {
	m, id := newTransientTestManager(t)
	m.MarkResult(nil, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}})
	if transientBlocked(t, m, id, "gpt-5.6-terra") {
		t.Fatal("one failure must not cool the account")
	}
}

func TestTransientFailuresUseDispatchOrderNotRequestEntryOrder(t *testing.T) {
	m, id := newTransientTestManager(t)
	contexts := make([]context.Context, 3)
	scopes := make([]*transientRequestScope, 3)
	for i := range contexts {
		contexts[i], scopes[i] = m.withTransientRequestScope(context.Background())
	}
	newer, newerScope := m.withTransientRequestScope(context.Background())
	m.MarkResult(newer, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Success: true, ExecutionSeq: m.nextExecutionSeq()})
	newerScope.finish(newer, m)
	for i := range contexts {
		m.MarkResult(contexts[i], Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: 502, Message: "overloaded"}, ExecutionSeq: m.nextExecutionSeq()})
		scopes[i].finish(contexts[i], m)
		if got := transientBlocked(t, m, id, "gpt-5.6-terra"); got != (i == 2) {
			t.Fatalf("after %d failures dispatched after success, blocked=%v", i+1, got)
		}
	}
}

func TestTransientOlderDispatchedFailureCannotOverrideNewerSuccess(t *testing.T) {
	m, id := newTransientTestManager(t)
	old, oldScope := m.withTransientRequestScope(context.Background())
	m.MarkResult(old, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: 502, Message: "overloaded"}, ExecutionSeq: m.nextExecutionSeq()})
	newer, newerScope := m.withTransientRequestScope(context.Background())
	m.MarkResult(newer, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Success: true, ExecutionSeq: m.nextExecutionSeq()})
	newerScope.finish(newer, m)
	oldScope.finish(old, m)
	for i := 0; i < 2; i++ {
		m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: 502, Message: "overloaded"}})
	}
	if transientBlocked(t, m, id, "gpt-5.6-terra") {
		t.Fatal("old failure was counted after a newer dispatched success")
	}
}

func TestCodexEmptyStreamFailuresRequireIndependentRequests(t *testing.T) {
	for _, nilSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed-source", true: "nil-source"}[nilSource], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			m.SetRetryConfig(0, 0, 1)
			m.SetTransientCredentialRetries(0)
			m.RegisterExecutor(&customStreamMockExecutor{
				identifier: "codex",
				streamFn: func(_ context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if nilSource {
						return &cliproxyexecutor.StreamResult{}, nil
					}
					ch := make(chan cliproxyexecutor.StreamChunk)
					close(ch)
					return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
				},
			})
			for i := 1; i <= 3; i++ {
				stream, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
				failed := err != nil
				if stream != nil {
					for chunk := range stream.Chunks {
						failed = failed || chunk.Err != nil
					}
				}
				if !failed {
					t.Fatal("empty stream should fail the request")
				}
				if got := transientBlocked(t, m, id, "gpt-5.6-terra"); got != (i == 3) {
					t.Fatalf("after %d empty-stream requests, blocked=%v", i, got)
				}
			}
		})
	}
}

func TestTransientSettlementPreservesAttemptQuotaHeaders(t *testing.T) {
	m, id := newTransientTestManager(t)
	ctx, scope := m.withTransientRequestScope(context.Background())
	attempt := internallogging.WithFreshResponseHeadersHolder(ctx)
	internallogging.SetResponseHeaders(attempt, http.Header{"X-Codex-Primary-Used-Percent": []string{"73"}})
	m.MarkResult(attempt, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: 502, Message: "overloaded"}, ExecutionSeq: m.nextExecutionSeq()})
	scope.finish(ctx, m)
	auth, _ := m.GetByID(id)
	if got := auth.Quota.Signals["X-Codex-Primary-Used-Percent"]; got != "73" {
		t.Fatalf("quota signal=%q, want original attempt value 73", got)
	}
}

func TestCodexNonStreamRetriesSettleOncePerRequest(t *testing.T) {
	for _, countTokens := range []bool{false, true} {
		for _, retrySucceeds := range []bool{false, true} {
			name := map[bool]string{false: "execute", true: "count"}[countTokens] + "/" + map[bool]string{false: "fails", true: "recovers"}[retrySucceeds]
			t.Run(name, func(t *testing.T) {
				m, id := newTransientTestManager(t)
				m.SetRetryConfig(0, 0, 1)
				m.SetTransientCredentialRetries(2)
				attempts := 0
				invoke := func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					attempts++
					if transientBlocked(t, m, id, "gpt-5.6-terra") {
						t.Error("cooldown became visible while this request was still retrying")
					}
					if retrySucceeds && attempts%3 == 0 {
						return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
					}
					return cliproxyexecutor.Response{}, overloadStatusError()
				}
				m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
				req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
				for i := 1; i <= 3; i++ {
					var err error
					if countTokens {
						_, err = m.ExecuteCount(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
					} else {
						_, err = m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
					}
					if (err == nil) != retrySucceeds {
						t.Fatalf("request %d error=%v, retrySucceeds=%v", i, err, retrySucceeds)
					}
					if got := transientBlocked(t, m, id, req.Model); got != (!retrySucceeds && i == 3) {
						t.Fatalf("after request %d, blocked=%v", i, got)
					}
				}
				if attempts != 9 {
					t.Fatalf("attempts=%d, want 9", attempts)
				}
			})
		}
	}
}

func TestCodexTransientStatusDoesNotMaskModelNotFound(t *testing.T) {
	m, id := newTransientTestManager(t)
	result := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: 503, Code: "model_not_found", Message: `{"error":{"code":"model_not_found"}}`}}
	m.MarkResult(context.Background(), result)
	if !transientBlocked(t, m, id, result.Model) {
		t.Fatal("an explicit model-not-found must retain its existing probe window")
	}
}

func TestCodexRetryEndingInUnauthorizedKeepsCredentialProtection(t *testing.T) {
	for _, countTokens := range []bool{false, true} {
		t.Run(map[bool]string{false: "execute", true: "count"}[countTokens], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			m.SetRetryConfig(0, 0, 1)
			m.SetTransientCredentialRetries(1)
			attempts := 0
			invoke := func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				attempts++
				if attempts == 1 {
					return cliproxyexecutor.Response{}, overloadStatusError()
				}
				return cliproxyexecutor.Response{}, customStatusError{code: http.StatusUnauthorized, msg: `{"error":{"type":"authentication_error","message":"invalid token"}}`}
			}
			m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
			req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
			var err error
			if countTokens {
				_, err = m.ExecuteCount(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
			} else {
				_, err = m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
			}
			if err == nil || attempts != 2 {
				t.Fatalf("attempts=%d, error=%v; expected overload followed by unauthorized", attempts, err)
			}
			if !transientBlocked(t, m, id, req.Model) {
				t.Fatal("final 401 did not retain credential protection")
			}
		})
	}
}

type turnRateLimitHeaderError struct {
	customStatusError
	headers http.Header
}

func (e turnRateLimitHeaderError) Headers() http.Header { return e.headers }

func TestCodexTurnRateLimitUsesWebsocketErrorRetryAfter(t *testing.T) {
	err := turnRateLimitHeaderError{
		customStatusError: customStatusError{code: http.StatusTooManyRequests, msg: `{"error":{"code":"turn_rate_limit_exceeded"}}`},
		headers:           http.Header{"Retry-After": []string{"30"}},
	}
	delay := retryAfterFromError(err)
	if delay == nil || *delay != 30*time.Second {
		t.Fatalf("WebSocket error Retry-After=%v, want 30s", delay)
	}
}

func TestCodexTurnRateLimitHintSurvivesTransientCooldownDisable(t *testing.T) {
	previous := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previous) })
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "forced"}[forced], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			delay := 30 * time.Minute
			failure := &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"code":"turn_rate_limit_exceeded"}}`}
			if forced {
				failure.Code = ErrorCodeForceCooldown
			}
			m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: failure, RetryAfter: &delay})
			auth, _ := m.GetByID(id)
			state := existingModelState(auth, "gpt-5.6-terra")
			if state == nil || time.Until(state.NextRetryAfter) < 29*time.Minute {
				t.Fatalf("explicit Retry-After was shortened: %+v", state)
			}
		})
	}
}

func TestCodexTurnRateLimitHonorsHTTPRetryAfter(t *testing.T) {
	m, id := newTransientTestManager(t)
	ctx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
	m.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: 429, Message: `{"error":{"code":"turn_rate_limit_exceeded"}}`}})
	auth, _ := m.GetByID(id)
	state := existingModelState(auth, "gpt-5.6-terra")
	if state == nil || time.Until(state.NextRetryAfter) < 25*time.Second {
		t.Fatalf("Retry-After header was not honored: %+v", state)
	}
	if state.Quota.Exceeded {
		t.Fatal("transient turn rate limit must not be recorded as quota exhaustion")
	}
}
