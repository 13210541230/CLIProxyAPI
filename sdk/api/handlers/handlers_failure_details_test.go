package handlers

import (
	"context"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestRequestCompletionIncludesStructuredSchedulerFailure(t *testing.T) {
	var got pluginapi.RequestCompletion
	host := &handlerInterceptorTestHost{completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) {
		got = completion
	}}
	ctx := context.Background()
	tracker := &requestLifecycleTracker{
		ctx:  ctx,
		host: host,
		completion: pluginapi.RequestCompletion{
			RequestID:    "request-1",
			SourceFormat: "openai-response",
			Model:        "gpt-5.6-luna",
		},
	}
	tracker.completeError(ctx, &interfaces.ErrorMessage{
		StatusCode: http.StatusServiceUnavailable,
		Error: &coreauth.Error{
			Code:       "account_pool_unavailable",
			Message:    "bound account is unavailable",
			HTTPStatus: http.StatusServiceUnavailable,
		},
	})

	if got.ErrorCode != "account_pool_unavailable" || got.ErrorSummary != "bound account is unavailable" {
		t.Fatalf("completion error details = code %q, type %q, summary %q", got.ErrorCode, got.ErrorType, got.ErrorSummary)
	}
	if got.StatusCode != http.StatusServiceUnavailable || got.Outcome != pluginapi.RequestCompletionFailed {
		t.Fatalf("completion terminal state = outcome %q, status %d", got.Outcome, got.StatusCode)
	}
}

func TestRequestCompletionExtractsReasonFromPluginTerminationBody(t *testing.T) {
	var got pluginapi.RequestCompletion
	host := &handlerInterceptorTestHost{completeRequest: func(_ context.Context, completion pluginapi.RequestCompletion) {
		got = completion
	}}
	ctx := context.Background()
	body := []byte(`{"error":{"type":"server_error","code":"account_busy","message":"bound account is saturated"}}`)
	tracker := &requestLifecycleTracker{
		ctx:  ctx,
		host: host,
		completion: pluginapi.RequestCompletion{
			RequestID:    "request-2",
			SourceFormat: "openai-response",
			Model:        "gpt-5.6-luna",
		},
	}
	tracker.completeError(ctx, &interfaces.ErrorMessage{
		StatusCode:     http.StatusServiceUnavailable,
		Error:          &coreexecutor.RequestTerminatedError{HTTPStatus: http.StatusServiceUnavailable, Body: body},
		DirectResponse: true,
	})

	if got.ErrorCode != "account_busy" || got.ErrorType != "server_error" || got.ErrorSummary != "account_busy: bound account is saturated" {
		t.Fatalf("completion error details = code %q, type %q, summary %q", got.ErrorCode, got.ErrorType, got.ErrorSummary)
	}
	if got.Outcome != pluginapi.RequestCompletionRejected {
		t.Fatalf("completion outcome = %q, want rejected", got.Outcome)
	}
}
