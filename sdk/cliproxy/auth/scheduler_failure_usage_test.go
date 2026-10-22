package auth

import (
	"context"
	"net/http"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestSchedulerPickFailureRecordOnlyCoversFailClosed(t *testing.T) {
	failClosed := &Error{Code: "policy_unavailable", Message: "exclusive scheduler policy is unavailable: no serving scheduler record", HTTPStatus: http.StatusServiceUnavailable}

	record, ok := schedulerPickFailureRecord(context.Background(), "mixed", []string{"codex"}, "gpt-6.1-sol", cliproxyexecutor.Options{Stream: true}, failClosed)
	if !ok {
		t.Fatal("fail-closed pick produced no monitoring record")
	}
	if record.Provider != "codex" || record.Model != "gpt-6.1-sol" || !record.Failed || !record.Stream {
		t.Fatalf("record = %+v, want codex/gpt-6.1-sol failed streaming", record)
	}
	if record.ExecutorType != schedulerFailureExecutorType {
		t.Fatalf("executor type = %q, want %q", record.ExecutorType, schedulerFailureExecutorType)
	}
	if record.Fail.StatusCode != http.StatusServiceUnavailable || record.Fail.Body == "" {
		t.Fatalf("fail = %+v, want a 503 body", record.Fail)
	}

	for _, other := range []error{
		nil,
		&Error{Code: "auth_not_found", Message: "no auth available"},
		&Error{Code: "account_busy", Message: "caller is waiting for capacity"},
	} {
		if _, okOther := schedulerPickFailureRecord(context.Background(), "codex", nil, "gpt-6.1-sol", cliproxyexecutor.Options{}, other); okOther {
			t.Fatalf("pick failure %v produced a monitoring record; only fail-closed picks should", other)
		}
	}
}
