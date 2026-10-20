package handlers

import (
	"context"
	"strings"
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAccountPoolSelectionFailurePublishesFailedUsage(t *testing.T) {
	plugin := newCapturePluginExecutorUsagePlugin("codex")
	registerUsagePluginForTest(t, "test-account-pool-selection-failure", plugin)

	err := &coreauth.Error{
		Code:       "account_pool_unavailable",
		Message:    "bound account is unavailable; deferring to the api-key provider if configured",
		HTTPStatus: 503,
	}
	publishAccountPoolSelectionFailureUsage(context.Background(), []string{"codex"}, "gpt-5.6-luna", true, err)

	record := plugin.waitRecord(t)
	if record.Provider != "codex" || record.Model != "gpt-5.6-luna" || !record.Stream {
		t.Fatalf("record routing fields = %+v", record)
	}
	if !record.Failed || record.Fail.StatusCode != 503 {
		t.Fatalf("record failure = %+v, want failed HTTP 503", record.Fail)
	}
	if !strings.Contains(record.Fail.Body, "account_pool_unavailable") {
		t.Fatalf("record failure body = %q, want scheduler reason", record.Fail.Body)
	}
	if record.Detail.TotalTokens != 0 {
		t.Fatalf("failure unexpectedly has token usage: %+v", record.Detail)
	}
}

func TestAccountPoolAdmissionFailureIsNotReportedAsSelectionFailure(t *testing.T) {
	plugin := newCapturePluginExecutorUsagePlugin("codex")
	registerUsagePluginForTest(t, "test-account-pool-admission-failure", plugin)

	err := &coreexecutor.RequestTerminatedError{HTTPStatus: 503, Body: []byte(`{"error":{"code":"account_busy","message":"admission wait timed out"}}`)}
	publishAccountPoolSelectionFailureUsage(context.Background(), []string{"codex"}, "gpt-5.6-luna", true, err)
	plugin.assertNoRecord(t)
}

func TestAccountPoolSelectionUsageDoesNotCaptureUnrelatedErrors(t *testing.T) {
	plugin := newCapturePluginExecutorUsagePlugin("codex")
	registerUsagePluginForTest(t, "test-account-pool-selection-nonpool", plugin)

	err := &coreauth.Error{Code: "upstream_unavailable", Message: "upstream failed", HTTPStatus: 503}
	publishAccountPoolSelectionFailureUsage(context.Background(), []string{"codex"}, "gpt-5.6-luna", false, err)
	plugin.assertNoRecord(t)
}
