package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestAvailabilityNeutralAttemptDiscardsPriorStagedTransientFailure(t *testing.T) {
	for _, mode := range []string{"compact", "count"} {
		t.Run(mode, func(t *testing.T) {
			m, id := newTransientTestManager(t)
			m.SetTransientCredentialRetries(0)
			ctx, scope := m.withTransientRequestScope(context.Background())
			prior := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "server_is_overloaded"}}
			if !scope.stage(ctx, prior) {
				t.Fatal("could not stage prior transient failure")
			}
			invoke := func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				return cliproxyexecutor.Response{}, customStatusError{code: http.StatusNotImplemented, msg: "not implemented"}
			}
			m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
			req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
			var err error
			if mode == "compact" {
				_, err = m.executeMixedOnce(ctx, []string{"codex"}, req, cliproxyexecutor.Options{Alt: "responses/compact"}, 1, 0, 1)
			} else {
				_, err = m.executeCountMixedOnce(ctx, []string{"codex"}, req, cliproxyexecutor.Options{}, 1, 0, 1)
			}
			if err == nil {
				t.Fatal("expected unsupported endpoint error")
			}
			scope.mu.Lock()
			_, retained := scope.pending[transientKey(prior)]
			scope.mu.Unlock()
			if retained {
				t.Fatal("availability-neutral result retained a prior staged failure for the same request")
			}
		})
	}
}

func TestCodexOverloadHonorsExplicitRetryAfter(t *testing.T) {
	previous := transientErrorCooldownSeconds.Load()
	transientErrorCooldownSeconds.Store(-1)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previous) })
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m, id := newTransientTestManager(t)
			delay := 30 * time.Second
			m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: status, Message: "upstream overloaded"}, RetryAfter: &delay})
			auth, _ := m.GetByID(id)
			state := existingModelState(auth, "gpt-5.6-terra")
			if state == nil || time.Until(state.NextRetryAfter) < 25*time.Second {
				t.Fatalf("explicit upstream Retry-After was not honored: %+v", state)
			}
		})
	}
}

func TestCodexOverloadHonorsRetryAfterFromExecution(t *testing.T) {
	for _, stream := range []bool{false, true} {
		for _, websocket := range []bool{false, true} {
			name := map[bool]string{false: "execute", true: "stream"}[stream] + "/" + map[bool]string{false: "http-header", true: "websocket-error-header"}[websocket]
			t.Run(name, func(t *testing.T) {
				m, id := newTransientTestManager(t)
				m.SetRetryConfig(0, 0, 1)
				m.SetTransientCredentialRetries(0)
				upstream := func(ctx context.Context) error {
					status := customStatusError{code: http.StatusBadGateway, msg: `{"error":{"code":"server_is_overloaded"}}`}
					headers := http.Header{"Retry-After": []string{"30"}}
					if websocket {
						return turnRateLimitHeaderError{customStatusError: status, headers: headers}
					}
					internallogging.SetResponseHeaders(ctx, headers)
					return status
				}
				if stream {
					m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						return nil, upstream(ctx)
					}})
					_, _ = m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
				} else {
					m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						return cliproxyexecutor.Response{}, upstream(ctx)
					}})
					_, _ = m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
				}
				auth, _ := m.GetByID(id)
				state := existingModelState(auth, "gpt-5.6-terra")
				if state == nil || time.Until(state.NextRetryAfter) < 25*time.Second {
					t.Fatalf("explicit upstream Retry-After was not honored: %+v", state)
				}
			})
		}
	}
}

func TestCodexRetryAfterHeaderDoesNotLeakToNextAccount(t *testing.T) {
	m := NewManager(nil, nil, nil)
	for _, id := range registerOverloadAuths(t, m, 2) {
		auth, _ := m.GetByID(id)
		auth.Attributes[AttributeAuthKind] = AuthKindOAuth
		if _, err := m.Update(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	m.SetRetryConfig(0, 0, 2)
	m.SetTransientCredentialRetries(0)
	hook := &recordingHook{}
	m.hook = hook
	var accounts []string
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		accounts = append(accounts, auth.ID)
		if len(accounts) == 1 {
			internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
		}
		return cliproxyexecutor.Response{}, overloadStatusError()
	}})
	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err == nil || len(accounts) != 2 || accounts[0] == accounts[1] {
		t.Fatalf("accounts=%v error=%v, want two different failed accounts", accounts, err)
	}
	last := hook.lastResult.Load()
	if last == nil || last.RetryAfter != nil || last.AuthID != accounts[1] {
		t.Fatalf("second account inherited earlier account's Retry-After: %+v", last)
	}
	if !transientBlocked(t, m, accounts[0], "gpt-5.6-terra") || transientBlocked(t, m, accounts[1], "gpt-5.6-terra") {
		t.Fatal("only the account with an explicit hint should cool after one failure")
	}
}

func TestCodexRetryAfterRejectsInvalidAndExpiredHints(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "invalid", "Thu, 01 Jan 1970 00:00:00 GMT"} {
		if got := codexRetryAfterHeaders(http.Header{"Retry-After": []string{value}}); got != nil {
			t.Fatalf("invalid or expired Retry-After %q yielded %v", value, *got)
		}
	}
}

func TestCodexRetryHintsDoNotOverrideRequestFaultClassification(t *testing.T) {
	auth := &Auth{Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}
	for _, status := range []customStatusError{
		{code: http.StatusBadRequest, msg: `{"error":{"type":"invalid_request_error"}}`},
		{code: http.StatusBadGateway, msg: applyPatchToolArgumentFaultMessage},
	} {
		err := turnRateLimitHeaderError{customStatusError: status, headers: http.Header{"Retry-After": []string{"30"}}}
		wrapped := withCodexRetryAfterHint(context.Background(), err, "codex", auth)
		if hasCodexRetryAfterHint(wrapped, "codex", auth) {
			t.Fatalf("request fault must not become an account retry hint: %v", err)
		}
	}
}

func TestCodexExplicitRetryAfterStopsImmediateRetries(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream"} {
		for _, websocket := range []bool{false, true} {
			for _, coolingDisabled := range []bool{false, true} {
				name := mode + "/" + map[bool]string{false: "http", true: "websocket"}[websocket] + "/" + map[bool]string{false: "cooling-enabled", true: "cooling-disabled"}[coolingDisabled]
				t.Run(name, func(t *testing.T) {
					previous := quotaCooldownDisabled.Load()
					quotaCooldownDisabled.Store(coolingDisabled)
					t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
					m, _ := newTransientTestManager(t)
					m.SetRetryConfig(1, 0, 1)
					m.SetTransientCredentialRetries(2)
					attempts := 0
					failure := func(ctx context.Context) error {
						attempts++
						status := customStatusError{code: http.StatusBadGateway, msg: `{"error":{"code":"server_is_overloaded"}}`}
						headers := http.Header{"Retry-After": []string{"30"}}
						if websocket {
							return turnRateLimitHeaderError{customStatusError: status, headers: headers}
						}
						internallogging.SetResponseHeaders(ctx, headers)
						return status
					}
					req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
					var err error
					if mode == "stream" {
						m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
							return nil, failure(ctx)
						}})
						var stream *cliproxyexecutor.StreamResult
						stream, err = m.ExecuteStream(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
						if stream != nil {
							for chunk := range stream.Chunks {
								if chunk.Err != nil {
									err = chunk.Err
								}
							}
						}
					} else {
						invoke := func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
							return cliproxyexecutor.Response{}, failure(ctx)
						}
						m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
						if mode == "count" {
							_, err = m.ExecuteCount(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
						} else {
							_, err = m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
						}
					}
					if attempts != 1 || statusCodeFromError(err) != http.StatusBadGateway {
						t.Fatalf("attempts=%d error=%v status=%d, want one 502", attempts, err, statusCodeFromError(err))
					}
				})
			}
		}
	}
}

func TestCodexStreamRetryAfterStopsModelPoolFallback(t *testing.T) {
	m, id := newTransientTestManager(t)
	auth, _ := m.GetByID(id)
	attempts := 0
	executor := &customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
		attempts++
		internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
		return nil, overloadStatusError()
	}}
	_, err := m.executeStreamWithModelPool(context.Background(), executor, auth, "codex", cliproxyexecutor.Request{}, cliproxyexecutor.Options{}, "gpt-5.6-terra", "", []string{"model-a", "model-b"}, true, OAuthModelAliasResult{}, nil, true, false)
	if err == nil || attempts != 1 {
		t.Fatalf("model-pool attempts=%d error=%v, want one attempt", attempts, err)
	}
}

func TestCodexRetryHintPreservesCrossRoundFailover(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, mode := range []string{"execute", "count", "stream"} {
		t.Run(mode, func(t *testing.T) {
			m := NewManager(nil, nil, nil)
			ids := registerOverloadAuths(t, m, 2)
			for _, id := range ids {
				auth, _ := m.GetByID(id)
				auth.Attributes[AttributeAuthKind] = AuthKindOAuth
				if _, err := m.Update(context.Background(), auth); err != nil {
					t.Fatal(err)
				}
			}
			m.SetRetryConfig(1, 0, 1)
			m.SetTransientCredentialRetries(0)
			var accounts []string
			failure := func(ctx context.Context, auth *Auth) error {
				accounts = append(accounts, auth.ID)
				if len(accounts) == 1 {
					internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
					return overloadStatusError()
				}
				return nil
			}
			req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
			var err error
			if mode == "stream" {
				m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
					if err := failure(ctx, auth); err != nil {
						return nil, err
					}
					return successStreamResult(), nil
				}})
				var stream *cliproxyexecutor.StreamResult
				stream, err = m.ExecuteStream(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
				if stream != nil {
					for chunk := range stream.Chunks {
						if chunk.Err != nil {
							err = chunk.Err
						}
					}
				}
			} else {
				invoke := func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, failure(ctx, auth)
				}
				m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
				if mode == "count" {
					_, err = m.ExecuteCount(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
				} else {
					_, err = m.Execute(context.Background(), []string{"codex"}, req, cliproxyexecutor.Options{})
				}
			}
			if err != nil || len(accounts) != 2 || accounts[0] == accounts[1] {
				t.Fatalf("accounts=%v error=%v, want failover to a different account", accounts, err)
			}
		})
	}
}

func TestRetryHintExclusionsExpireWithoutWallClockWait(t *testing.T) {
	base := time.Unix(1000, 0)
	scope := &transientRequestScope{}
	scope.recordHint("auth-a", base.Add(time.Minute))
	scope.recordHint("auth-a", base.Add(30*time.Second))
	if _, ok := scope.activeHintExclusions(base.Add(45 * time.Second))["auth-a"]; !ok {
		t.Fatal("a shorter hint shortened the original deadline")
	}
	if _, ok := scope.activeHintExclusions(base.Add(time.Minute))["auth-a"]; ok {
		t.Fatal("expired hint still excludes the account")
	}
}

func TestCodexRetryHintAllowsConfiguredWait(t *testing.T) {
	for _, coolingDisabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "cooling-enabled", true: "cooling-disabled"}[coolingDisabled], func(t *testing.T) {
			previous := quotaCooldownDisabled.Load()
			quotaCooldownDisabled.Store(coolingDisabled)
			t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
			m, id := newTransientTestManager(t)
			m.SetRetryConfig(1, time.Minute, 1)
			ctx, scope := m.withTransientRequestScope(context.Background())
			deadline := time.Now().Add(30 * time.Second)
			scope.recordHint(id, deadline)
			delay := time.Until(deadline)
			m.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}, RetryAfter: &delay})
			err := &codexRetryAfterHintError{cause: overloadStatusError(), until: deadline}
			attempted := map[string]struct{}{id: {}}
			wait, retry := m.shouldRetryAfterErrorWithAttempted(ctx, cliproxyexecutor.Options{}, err, 0, []string{"codex"}, "gpt-5.6-terra", time.Minute, -1, 1, attempted)
			if !retry || wait < 29*time.Second || wait > 31*time.Second {
				t.Fatalf("retry=%v wait=%v, want wait of approximately 30s", retry, wait)
			}
			if _, retry := m.shouldRetryAfterErrorWithAttempted(ctx, cliproxyexecutor.Options{}, err, 0, []string{"codex"}, "gpt-5.6-terra", 0, -1, 1, attempted); retry {
				t.Fatal("retry must stop when configured maximum wait is zero")
			}
		})
	}
}

func TestRetryHintWaitUsesEarliestEligibleAccountDeadline(t *testing.T) {
	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 3)
	now := time.Now()
	model := "gpt-5.6-terra"
	m.mu.Lock()
	m.auths[ids[0]].ModelStates = map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: now.Add(45 * time.Second), LastError: &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}}}
	m.auths[ids[2]].Disabled = true
	m.mu.Unlock()
	hints := map[string]time.Time{ids[0]: now.Add(30 * time.Second), ids[1]: now.Add(time.Minute)}
	wait, found := m.closestCooldownWaitWithHints([]string{"codex"}, model, 0, authSelectionEligibility{}, "", 1, http.StatusBadGateway, nil, hints)
	if !found || wait < 44*time.Second || wait > 46*time.Second {
		t.Fatalf("found=%v wait=%v, want approximately 45s", found, wait)
	}
	m.mu.Lock()
	m.auths[ids[2]].Disabled = false
	m.mu.Unlock()
	if wait, found := m.closestCooldownWaitWithHints([]string{"codex"}, model, 0, authSelectionEligibility{}, "", 1, http.StatusBadGateway, nil, hints); !found || wait != 0 {
		t.Fatalf("available third account: found=%v wait=%v", found, wait)
	}
	m.mu.Lock()
	for _, id := range ids {
		m.auths[id].Disabled = true
	}
	m.mu.Unlock()
	if _, found := m.closestCooldownWaitWithHints([]string{"codex"}, model, 0, authSelectionEligibility{}, "", 1, http.StatusBadGateway, nil, hints); found {
		t.Fatal("disabled accounts cannot become retry candidates")
	}
}

func TestRetryHintPreserves429WaitFloor(t *testing.T) {
	for _, coolingDisabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "cooling-enabled", true: "cooling-disabled"}[coolingDisabled], func(t *testing.T) {
			previous := quotaCooldownDisabled.Load()
			quotaCooldownDisabled.Store(coolingDisabled)
			t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
			m, id := newTransientTestManager(t)
			hints := map[string]time.Time{id: time.Now().Add(time.Second)}
			wait, found := m.closestCooldownWaitWithHints([]string{"codex"}, "gpt-5.6-terra", 0, authSelectionEligibility{}, "", 1, http.StatusTooManyRequests, map[string]struct{}{id: {}}, hints)
			if !found || (!coolingDisabled && wait < minQuotaCooldownFloor) || (coolingDisabled && (wait <= 0 || wait > 2*time.Second)) {
				t.Fatalf("found=%v wait=%v coolingDisabled=%v", found, wait, coolingDisabled)
			}
		})
	}
}

func TestPoolHintRecoveryDoesNotMaskOtherRejections(t *testing.T) {
	m, id := newTransientTestManager(t)
	m.SetPluginScheduler(&exclusiveTransientTestScheduler{fakePluginScheduler: &fakePluginScheduler{}})
	ctx, scope := m.withTransientRequestScope(context.Background())
	scope.recordHint(id, time.Now().Add(30*time.Second))
	upstream := markUpstreamExecutionAttempt(overloadStatusError())
	opts := cliproxyexecutor.Options{}
	poolErr := &Error{Code: "account_pool_unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	wait, ok := m.retryHintAfterPoolRejection(ctx, opts, poolErr, upstream, "gpt-5.6-terra", 1, 1, nil, time.Minute)
	if !ok || wait < 29*time.Second || wait > 31*time.Second {
		t.Fatalf("eligible pool rejection: wait=%v ok=%v", wait, ok)
	}
	for _, code := range []string{"identity_missing", "policy_unavailable", "account_pool_disabled", "account_busy"} {
		if _, ok := m.retryHintAfterPoolRejection(ctx, opts, &Error{Code: code, Retryable: true, HTTPStatus: 503}, upstream, "gpt-5.6-terra", 1, 1, nil, time.Minute); ok {
			t.Fatalf("%s must not become a hint wait", code)
		}
	}
	if _, ok := m.retryHintAfterPoolRejection(ctx, opts, &Error{Code: "account_pool_unavailable", Retryable: false}, upstream, "gpt-5.6-terra", 1, 1, nil, time.Minute); ok {
		t.Fatal("non-retryable rejection must not wait")
	}
	if _, ok := m.retryHintAfterPoolRejection(ctx, opts, poolErr, upstream, "gpt-5.6-terra", 0, 1, nil, time.Minute); ok {
		t.Fatal("request-retry=0 must not gain an extra attempt")
	}
	if _, ok := m.retryHintAfterPoolRejection(ctx, opts, poolErr, upstream, "gpt-5.6-terra", 1, 1, map[string]struct{}{id: {}}, time.Minute); ok {
		t.Fatal("a newly selected credential must not trigger the no-selection fallback")
	}
	if _, ok := m.retryHintAfterPoolRejection(ctx, opts, poolErr, upstream, "gpt-5.6-terra", 1, 1, nil, time.Second); ok {
		t.Fatal("hint beyond remaining wait budget must not be waited")
	}
	if _, ok := m.retryHintAfterPoolRejection(ctx, opts, poolErr, nil, "gpt-5.6-terra", 1, 1, nil, time.Minute); ok {
		t.Fatal("no prior upstream attempt must not wait")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, ok := m.retryHintAfterPoolRejection(canceled, opts, poolErr, upstream, "gpt-5.6-terra", 1, 1, nil, time.Minute); ok {
		t.Fatal("a canceled request must not wait")
	}
	pinned := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: "another-auth"}}
	if _, ok := m.retryHintAfterPoolRejection(ctx, pinned, poolErr, upstream, "gpt-5.6-terra", 1, 1, nil, time.Minute); ok {
		t.Fatal("a hint from another credential must not bypass an explicit pin")
	}
	m.SetPluginScheduler(&fakePluginScheduler{})
	if _, ok := m.retryHintAfterPoolRejection(ctx, opts, poolErr, upstream, "gpt-5.6-terra", 1, 1, nil, time.Minute); ok {
		t.Fatal("non-exclusive schedulers must not use the pool-specific wait")
	}
}

type exclusiveTransientTestScheduler struct{ *fakePluginScheduler }

func (*exclusiveTransientTestScheduler) SchedulerExcludesFastPath(provider string) bool {
	return provider == "codex"
}

func TestRetryHintWaitWithBoundPluginScheduler(t *testing.T) {
	for _, mode := range []string{"execute", "count", "stream", "execute-fails-twice"} {
		for _, coolingDisabled := range []bool{false, true} {
			name := mode + "/" + map[bool]string{false: "cooling-enabled", true: "cooling-disabled"}[coolingDisabled]
			t.Run(name, func(t *testing.T) {
				previous := quotaCooldownDisabled.Load()
				quotaCooldownDisabled.Store(coolingDisabled)
				t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
				m := NewManager(nil, nil, nil)
				ids := registerOverloadAuths(t, m, 2)
				for _, id := range ids {
					auth, _ := m.GetByID(id)
					auth.Attributes[AttributeAuthKind] = AuthKindOAuth
					if _, err := m.Update(context.Background(), auth); err != nil {
						t.Fatal(err)
					}
				}
				m.SetRetryConfig(1, 2*time.Second, 1)
				m.SetTransientCredentialRetries(0)
				var candidates [][]string
				scheduler := &fakePluginScheduler{handled: true, pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
					var current []string
					for _, candidate := range req.Candidates {
						current = append(current, candidate.ID)
					}
					candidates = append(candidates, current)
					for _, id := range current {
						if id == ids[0] {
							return pluginapi.SchedulerPickResponse{Handled: true, AuthID: id}, true, nil
						}
					}
					return pluginapi.SchedulerPickResponse{}, true, &Error{Code: "account_pool_unavailable", Message: "bound account is not an eligible candidate", HTTPStatus: http.StatusServiceUnavailable, Retryable: true}
				}}
				m.SetPluginScheduler(&exclusiveTransientTestScheduler{fakePluginScheduler: scheduler})
				var selected []string
				failure := func(ctx context.Context, auth *Auth) error {
					cliproxyexecutor.MarkUpstreamAttempt(ctx)
					selected = append(selected, auth.ID)
					if len(selected) == 1 || mode == "execute-fails-twice" {
						return turnRateLimitHeaderError{customStatusError: customStatusError{code: http.StatusBadGateway, msg: `{"error":{"code":"server_is_overloaded"}}`}, headers: http.Header{"Retry-After": []string{"1"}}}
					}
					return nil
				}
				opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "synthetic-session"}}
				req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
				var err error
				if mode == "stream" {
					m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
						if err := failure(ctx, auth); err != nil {
							return nil, err
						}
						return successStreamResult(), nil
					}})
					var stream *cliproxyexecutor.StreamResult
					stream, err = m.ExecuteStream(context.Background(), []string{"codex"}, req, opts)
					if stream != nil {
						for chunk := range stream.Chunks {
							if chunk.Err != nil {
								err = chunk.Err
							}
						}
					}
				} else {
					invoke := func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, failure(ctx, auth)
					}
					m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
					if mode == "count" {
						_, err = m.ExecuteCount(context.Background(), []string{"codex"}, req, opts)
					} else {
						_, err = m.Execute(context.Background(), []string{"codex"}, req, opts)
					}
				}
				if (err == nil) == (mode == "execute-fails-twice") || len(selected) != 2 || selected[0] != ids[0] || selected[1] != ids[0] {
					t.Fatalf("selected=%v scheduler candidates=%v error=%v, want exactly two a attempts", selected, candidates, err)
				}
			})
		}
	}
}

type cooldownWaitObserver struct {
	context.Context
	entered chan struct{}
	once    sync.Once
	armed   atomic.Bool
	called  atomic.Bool
}

func (c *cooldownWaitObserver) Done() <-chan struct{} {
	if c.armed.Load() {
		c.called.Store(true)
		c.once.Do(func() { close(c.entered) })
	}
	return c.Context.Done()
}

func TestPoolRetryHintDoesNotDelayAPIKeyFallback(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 2)
	for index, id := range ids {
		auth, _ := m.GetByID(id)
		if index == 0 {
			auth.Attributes[AttributeAuthKind] = AuthKindOAuth
		} else {
			auth.Attributes[AttributeAuthKind] = AuthKindAPIKey
		}
		if _, err := m.Update(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	m.SetRetryConfig(1, 2*time.Second, 1)
	m.SetTransientCredentialRetries(0)
	scheduler := &fakePluginScheduler{handled: true, pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
		for _, id := range ids {
			for _, candidate := range req.Candidates {
				if candidate.ID == id {
					return pluginapi.SchedulerPickResponse{Handled: true, AuthID: id}, true, nil
				}
			}
		}
		return pluginapi.SchedulerPickResponse{}, true, &Error{Code: "account_pool_unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}}
	m.SetPluginScheduler(&exclusiveTransientTestScheduler{fakePluginScheduler: scheduler})
	var selected []string
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		selected = append(selected, auth.ID)
		if auth.ID == ids[0] {
			return cliproxyexecutor.Response{}, turnRateLimitHeaderError{customStatusError: customStatusError{code: http.StatusBadGateway, msg: `{"error":{"code":"server_is_overloaded"}}`}, headers: http.Header{"Retry-After": []string{"30"}}}
		}
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}})
	opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.CanonicalSessionIDMetadataKey: "synthetic-session"}}
	observed := &cooldownWaitObserver{Context: context.Background(), entered: make(chan struct{})}
	observed.armed.Store(true)
	_, err := m.Execute(observed, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, opts)
	if err != nil || len(selected) != 2 || selected[0] != ids[0] || selected[1] != ids[1] {
		t.Fatalf("selected=%v error=%v, want OAuth a then API key b", selected, err)
	}
	if observed.called.Load() {
		t.Fatal("API key fallback entered a positive retry wait")
	}
}

func TestPoolHintWaitRespectsCancellation(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 2)
	for _, id := range ids {
		auth, _ := m.GetByID(id)
		auth.Attributes[AttributeAuthKind] = AuthKindOAuth
		if _, err := m.Update(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	m.SetRetryConfig(1, time.Minute, 1)
	m.SetTransientCredentialRetries(0)
	var observed *cooldownWaitObserver
	scheduler := &fakePluginScheduler{handled: true, pick: func(_ context.Context, req pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
		for _, candidate := range req.Candidates {
			if candidate.ID == ids[0] {
				return pluginapi.SchedulerPickResponse{Handled: true, AuthID: ids[0]}, true, nil
			}
		}
		observed.armed.Store(true)
		return pluginapi.SchedulerPickResponse{}, true, &Error{Code: "account_pool_unavailable", Retryable: true, HTTPStatus: http.StatusServiceUnavailable}
	}}
	m.SetPluginScheduler(&exclusiveTransientTestScheduler{fakePluginScheduler: scheduler})
	attempts := 0
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		cliproxyexecutor.MarkUpstreamAttempt(ctx)
		attempts++
		return cliproxyexecutor.Response{}, turnRateLimitHeaderError{customStatusError: customStatusError{code: http.StatusBadGateway, msg: `{"error":{"code":"server_is_overloaded"}}`}, headers: http.Header{"Retry-After": []string{"30"}}}
	}})
	parent, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed = &cooldownWaitObserver{Context: parent, entered: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := m.Execute(observed, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
		done <- err
	}()
	select {
	case <-observed.entered:
		cancel()
	case <-time.After(5 * time.Second):
		t.Fatal("request did not enter its bounded cooldown wait")
	}
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) || attempts != 1 {
			t.Fatalf("error=%v attempts=%d, want cancellation after one upstream attempt", err, attempts)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("request did not return after cancellation")
	}
}

func TestCodexEarlierRetryHintSurvivesOtherAccountFailure(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	m := NewManager(nil, nil, nil)
	ids := registerOverloadAuths(t, m, 2)
	for _, id := range ids {
		auth, _ := m.GetByID(id)
		auth.Attributes[AttributeAuthKind] = AuthKindOAuth
		if _, err := m.Update(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	m.SetRetryConfig(2, 0, 1)
	m.SetTransientCredentialRetries(0)
	var accounts []string
	m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
		accounts = append(accounts, auth.ID)
		if len(accounts) == 1 {
			internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
		}
		if len(accounts) < 3 {
			return cliproxyexecutor.Response{}, overloadStatusError()
		}
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}})
	_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
	if err != nil || len(accounts) != 3 || accounts[0] == accounts[1] || accounts[1] != accounts[2] {
		t.Fatalf("accounts=%v error=%v, want hinted A, then B, then B", accounts, err)
	}
}

func TestCodexCredentialQuotaRemainsAccountScoped(t *testing.T) {
	m, id := newTransientTestManager(t)
	delay := time.Hour
	result := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", CredentialScope: true, RetryAfter: &delay,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: `{"error":{"type":"usage_limit_reached"}}`}}
	m.MarkResult(context.Background(), result)
	auth, _ := m.GetByID(id)
	if !auth.Unavailable || !auth.Quota.Exceeded || auth.Quota.Reason != "credential_quota" {
		t.Fatalf("explicit subscription exhaustion lost account-wide protection: %+v", auth.Quota)
	}
	if blocked, _, _ := isAuthBlockedForModel(auth, "gpt-other-model", time.Now()); !blocked {
		t.Fatal("credential quota should block other models on the same account")
	}
}

func TestCodexOverloadRetryAfterHonorsCoolingOverride(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	for _, forced := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "forced"}[forced], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			delay := 30 * time.Second
			err := &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}
			if forced {
				err.Code = ErrorCodeForceCooldown
			}
			m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: err, RetryAfter: &delay})
			if got := transientBlocked(t, m, id, "gpt-5.6-terra"); got != forced {
				t.Fatalf("blocked=%v, forced=%v", got, forced)
			}
		})
	}
}

func TestCodexForcedOverloadHonorsHTTPRetryAfter(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(true)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })
	m, id := newTransientTestManager(t)
	ctx := internallogging.WithFreshResponseHeadersHolder(context.Background())
	internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"300"}})
	m.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusServiceUnavailable, Code: ErrorCodeForceCooldown, Message: "overloaded"}})
	auth, _ := m.GetByID(id)
	state := existingModelState(auth, "gpt-5.6-terra")
	if state == nil || time.Until(state.NextRetryAfter) < 290*time.Second {
		t.Fatalf("forced cooldown shortened the explicit upstream hint: %+v", state)
	}
}

func TestAuthCredentialReplacementClearsTransientHistory(t *testing.T) {
	m, id := newTransientTestManager(t)
	failure := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}}
	m.MarkResult(context.Background(), failure)
	m.MarkResult(context.Background(), failure)
	snapshot, _ := m.GetByID(id)
	snapshot.Metadata = map[string]any{"access_token": "replacement-token"}
	if _, err := m.Update(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	current, _ := m.GetByID(id)
	failure.CredentialVersion = current.CredentialVersion
	failure.RegistrationEpoch = current.RegistrationEpoch
	m.MarkResult(context.Background(), failure)
	if transientBlocked(t, m, id, failure.Model) {
		t.Fatal("new credentials inherited the previous credential's transient failures")
	}
	m.MarkResult(context.Background(), failure)
	m.MarkResult(context.Background(), failure)
	if !transientBlocked(t, m, id, failure.Model) {
		t.Fatal("three new credential failures should trigger cooldown")
	}
}

func TestLateOldCredentialFailureCannotPolluteReplacement(t *testing.T) {
	m, id := newTransientTestManager(t)
	old, _ := m.GetByID(id)
	oldSeq := m.nextExecutionSeq()
	ctx, scope := m.withTransientRequestScope(context.Background())
	dispatched, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	go func() {
		close(dispatched)
		<-release
		m.MarkResult(ctx, Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "old request"},
			CredentialVersion: old.CredentialVersion, RegistrationEpoch: old.RegistrationEpoch, ExecutionSeq: oldSeq})
		scope.finish(ctx, m)
		close(completed)
	}()
	<-dispatched
	replacement, _ := m.GetByID(id)
	replacement.Metadata = map[string]any{"access_token": "replacement-token"}
	if _, err := m.Update(context.Background(), replacement); err != nil {
		close(release)
		<-completed
		t.Fatal(err)
	}
	close(release)
	<-completed
	current, _ := m.GetByID(id)
	fresh := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "new request"}, CredentialVersion: current.CredentialVersion, RegistrationEpoch: current.RegistrationEpoch}
	m.MarkResult(context.Background(), fresh)
	m.MarkResult(context.Background(), fresh)
	if transientBlocked(t, m, id, fresh.Model) {
		t.Fatal("old credential's late failure polluted the replacement")
	}
	m.MarkResult(context.Background(), fresh)
	if !transientBlocked(t, m, id, fresh.Model) {
		t.Fatal("three failures of the replacement credential should trigger cooldown")
	}
}

func TestStaleAuthUpdatePreservesActiveAndClearedCooldown(t *testing.T) {
	for _, cleared := range []bool{false, true} {
		t.Run(map[bool]string{false: "active-cooldown", true: "cleared-cooldown"}[cleared], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			failure := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}}
			m.MarkResult(context.Background(), failure)
			m.MarkResult(context.Background(), failure)
			if cleared {
				m.MarkResult(context.Background(), failure)
			}
			snapshot, _ := m.GetByID(id)
			if cleared {
				m.MarkResult(context.Background(), Result{AuthID: id, Provider: "codex", Model: failure.Model, Success: true})
			} else {
				m.MarkResult(context.Background(), failure)
			}
			if got := transientBlocked(t, m, id, failure.Model); got == cleared {
				t.Fatalf("unexpected pre-update cooldown=%v", got)
			}
			snapshot.Attributes["label"] = "stale metadata update"
			if _, err := m.Update(context.Background(), snapshot); err != nil {
				t.Fatal(err)
			}
			if got := transientBlocked(t, m, id, failure.Model); got == cleared {
				t.Fatalf("stale update changed cooldown to %v, cleared=%v", got, cleared)
			}
		})
	}
}

type credentialScopedTestError struct{ customStatusError }

func (credentialScopedTestError) IsCredentialScoped() bool { return true }

func TestCodexTransientRetryDoesNotMaskFinalCredentialError(t *testing.T) {
	for _, countTokens := range []bool{false, true} {
		for _, quota := range []bool{false, true} {
			name := map[bool]string{false: "execute", true: "count"}[countTokens] + "/" + map[bool]string{false: "unauthorized", true: "credential-quota"}[quota]
			t.Run(name, func(t *testing.T) {
				m, id := newTransientTestManager(t)
				m.SetRetryConfig(0, 0, 1)
				m.SetTransientCredentialRetries(1)
				attempts := 0
				invoke := func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
					attempts++
					if attempts == 1 {
						return cliproxyexecutor.Response{}, overloadStatusError()
					}
					if quota {
						return cliproxyexecutor.Response{}, credentialScopedTestError{customStatusError{code: http.StatusTooManyRequests, msg: `{"error":{"type":"usage_limit_reached"}}`}}
					}
					return cliproxyexecutor.Response{}, customStatusError{code: http.StatusUnauthorized, msg: `{"error":{"type":"authentication_error"}}`}
				}
				m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
				request := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
				var err error
				if countTokens {
					_, err = m.ExecuteCount(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
				} else {
					_, err = m.Execute(context.Background(), []string{"codex"}, request, cliproxyexecutor.Options{})
				}
				if err == nil || attempts != 2 {
					t.Fatalf("attempts=%d error=%v", attempts, err)
				}
				auth, _ := m.GetByID(id)
				if quota {
					if !auth.Quota.Exceeded || auth.Quota.Reason != "credential_quota" {
						t.Fatalf("final quota failure not recorded: %+v", auth.Quota)
					}
				} else {
					state := existingModelState(auth, request.Model)
					if state == nil || state.LastError == nil || state.LastError.HTTPStatus != http.StatusUnauthorized {
						t.Fatalf("final unauthorized failure not recorded: %+v", state)
					}
				}
			})
		}
	}
}

func TestCanceledNonStreamRetainsOnlyConfirmedHTTPRetryAfter(t *testing.T) {
	for _, mode := range []string{"execute", "count"} {
		for _, retryFirst := range []bool{false, true} {
			for _, hint := range []bool{false, true} {
				name := mode + "/" + map[bool]string{false: "initial", true: "retry"}[retryFirst] + "/" + map[bool]string{false: "no-hint", true: "retry-after"}[hint]
				t.Run(name, func(t *testing.T) {
					m, id := newTransientTestManager(t)
					m.SetRetryConfig(0, 0, 1)
					m.SetTransientCredentialRetries(1)
					attempts := 0
					var cancel context.CancelFunc
					invoke := func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
						attempts++
						if retryFirst && attempts == 1 {
							return cliproxyexecutor.Response{}, overloadStatusError()
						}
						if hint {
							internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
						}
						cancel()
						return cliproxyexecutor.Response{}, overloadStatusError()
					}
					m.RegisterExecutor(&mockCustomErrorExecutor{identifier: "codex", executeFn: invoke, countFn: invoke})
					for request := 0; request < 3; request++ {
						attempts = 0
						ctx, cancelRequest := context.WithCancel(context.Background())
						cancel = cancelRequest
						req := cliproxyexecutor.Request{Model: "gpt-5.6-terra"}
						if mode == "count" {
							_, _ = m.ExecuteCount(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
						} else {
							_, _ = m.Execute(ctx, []string{"codex"}, req, cliproxyexecutor.Options{})
						}
						cancelRequest()
						if got := transientBlocked(t, m, id, req.Model); got != hint {
							t.Fatalf("request %d: blocked=%v, hint=%v", request+1, got, hint)
						}
						if hint {
							break
						}
					}
				})
			}
		}
	}
}

func TestCanceledStreamRetainsConfirmedHTTPRetryAfter(t *testing.T) {
	for _, hint := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-hint", true: "retry-after"}[hint], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				if hint {
					internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
				}
				cancel()
				return nil, overloadStatusError()
			}})
			_, _ = m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
			if got := transientBlocked(t, m, id, "gpt-5.6-terra"); got != hint {
				t.Fatalf("blocked=%v, hint=%v", got, hint)
			}
		})
	}
}

func TestCanceledStreamRetryDoesNotCountEarlierTransientFailure(t *testing.T) {
	for _, hint := range []bool{false, true} {
		t.Run(map[bool]string{false: "no-hint", true: "retry-after"}[hint], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			m.SetRetryConfig(0, 0, 1)
			m.SetTransientCredentialRetries(1)
			attempts := 0
			var cancel context.CancelFunc
			m.RegisterExecutor(&customStreamMockExecutor{identifier: "codex", streamFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				attempts++
				if attempts == 1 {
					return nil, overloadStatusError()
				}
				if hint {
					internallogging.SetResponseHeaders(ctx, http.Header{"Retry-After": []string{"30"}})
				}
				cancel()
				return nil, overloadStatusError()
			}})
			for request := 0; request < 3; request++ {
				attempts = 0
				ctx, cancelRequest := context.WithCancel(context.Background())
				cancel = cancelRequest
				_, _ = m.ExecuteStream(ctx, []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-5.6-terra"}, cliproxyexecutor.Options{})
				cancelRequest()
				if got := transientBlocked(t, m, id, "gpt-5.6-terra"); got != hint {
					t.Fatalf("request %d: blocked=%v, hint=%v", request+1, got, hint)
				}
				if hint {
					break
				}
			}
		})
	}
}

func TestAuthUpdatePreservesNewerTransientHistory(t *testing.T) {
	for _, newerSuccess := range []bool{false, true} {
		t.Run(map[bool]string{false: "new-failures", true: "new-success"}[newerSuccess], func(t *testing.T) {
			m, id := newTransientTestManager(t)
			failure := Result{AuthID: id, Provider: "codex", Model: "gpt-5.6-terra", Error: &Error{HTTPStatus: http.StatusBadGateway, Message: "overloaded"}}
			success := Result{AuthID: id, Provider: "codex", Model: failure.Model, Success: true}
			m.MarkResult(context.Background(), success)
			if newerSuccess {
				m.MarkResult(context.Background(), failure)
				m.MarkResult(context.Background(), failure)
			}
			snapshot, _ := m.GetByID(id)
			if newerSuccess {
				m.MarkResult(context.Background(), success)
			} else {
				m.MarkResult(context.Background(), failure)
				m.MarkResult(context.Background(), failure)
			}
			snapshot.Attributes["label"] = "unchanged credentials"
			if _, err := m.Update(context.Background(), snapshot); err != nil {
				t.Fatal(err)
			}
			m.MarkResult(context.Background(), failure)
			if got := transientBlocked(t, m, id, failure.Model); got != !newerSuccess {
				t.Fatalf("cooldown after update=%v, want %v", got, !newerSuccess)
			}
		})
	}
}
