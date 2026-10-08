package modeltrace

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

type rejectedTraceExecutor struct{}

func (rejectedTraceExecutor) ExecuteModelStream(context.Context, handlers.ModelExecutionRequest) (handlers.ModelExecutionStream, *interfaces.ErrorMessage) {
	return handlers.ModelExecutionStream{}, &interfaces.ErrorMessage{StatusCode: 503, Error: errors.New("sensitive credential Bearer private-token; full prompt")}
}

func TestModelTraceExecutionErrorsRetainSafeDiagnostics(t *testing.T) {
	_, rejected := Execute(context.Background(), rejectedTraceExecutor{}, testCredential("a"), "gpt-5.5", traceChallenges()[0])
	if rejected == nil || !strings.Contains(rejected.Error(), "HTTP 503") || strings.Contains(rejected.Error(), "private-token") {
		t.Fatalf("lost status or exposed sensitive rejection: %v", rejected)
	}
	chunks := make(chan handlers.ModelExecutionChunk, 1)
	chunks <- handlers.ModelExecutionChunk{Err: &handlers.ModelExecutionStreamError{StatusCode: 401, Message: "sensitive credential Bearer private-token"}}
	close(chunks)
	_, streamErr := readSDKStream(context.Background(), chunks)
	if streamErr == nil || !strings.Contains(streamErr.Error(), "HTTP 401") || strings.Contains(streamErr.Error(), "private-token") {
		t.Fatalf("lost status or exposed sensitive stream failure: %v", streamErr)
	}
	path := testPath(t)
	s := New(path, func(context.Context, Credential, string, Challenge) (Output, error) { return Output{}, rejected })
	if _, err := s.Start(testCredential("a"), "gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	s.Wait()
	r, ok := s.Record(testCredential("a"), "")
	if !ok || r.Status != "failed" || r.Verdict != "inconclusive" {
		t.Fatal(r)
	}
	for _, sample := range r.Samples {
		if !strings.Contains(sample.Error, "HTTP 503") {
			t.Fatalf("diagnostic discarded in record: %q", sample.Error)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "HTTP 503") || strings.Contains(string(raw), "private-token") || strings.Contains(string(raw), "full prompt") {
		t.Fatal("unsafe or missing persisted diagnostic")
	}
}
