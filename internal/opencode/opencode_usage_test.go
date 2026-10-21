package opencode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// A streamed response without a usage chunk must still yield one record per
// request; otherwise opencode traffic vanishes from the statistics queue.
func TestExecuteStreamCountsRequestsWithoutUpstreamUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	capture := newUsageCapture(t)
	executor := newTestExecutor(t, server)

	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash","stream":true}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	for range result.Chunks {
	}

	record := capture.wait(t)
	if record.Provider != ProviderName || record.Failed {
		t.Fatalf("unexpected record: provider=%q failed=%v", record.Provider, record.Failed)
	}
}

// The responses protocol reports tokens under response.usage inside event
// payloads, which the chat-completions parser never sees.
func TestExecuteStreamReportsResponsesProtocolUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"type\":\"response.completed\",\"response\":{\"usage\":{\"input_tokens\":9,\"output_tokens\":5,\"total_tokens\":14}}}\n\n")
	}))
	defer server.Close()
	capture := newUsageCapture(t)
	executor := newTestExecutor(t, server)

	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "gpt-native",
		Payload: []byte(`{"model":"gpt-native","stream":true}`),
		Format:  sdktranslator.FormatOpenAIResponse,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: true})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	for range result.Chunks {
	}

	record := capture.wait(t)
	if record.Detail.InputTokens != 9 || record.Detail.OutputTokens != 5 {
		t.Fatalf("unexpected token detail: %+v", record.Detail)
	}
}

// Failed upstream calls are requests too and must appear in the statistics queue.
func TestExecuteFailureReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"api key expired"}}`)
	}))
	defer server.Close()
	capture := newUsageCapture(t)
	executor := newTestExecutor(t, server)

	if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash"}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err == nil {
		t.Fatal("expected upstream error")
	}

	record := capture.wait(t)
	if !record.Failed || record.Fail.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected failed record with 401, got failed=%v status=%d", record.Failed, record.Fail.StatusCode)
	}
}

// Request detail parity: the usage record must carry the same session identity and
// streaming flag that the upstream request uses, plus the served model.
func TestUsageRecordCarriesRequestDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"served-model","usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`)
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)
	capture := newUsageCapture(t)

	headers := http.Header{}
	headers.Set("X-Session-Id", "11111111-2222-3333-4444-555555555555")
	if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash"}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Headers: headers}); err != nil {
		t.Fatalf("execute: %v", err)
	}

	record := capture.wait(t)
	if record.Stream {
		t.Fatal("non-stream request reported as stream")
	}
	if record.ResponseModel != "served-model" {
		t.Fatalf("response model = %q, want served-model", record.ResponseModel)
	}
	if want := SessionIDForSignal("11111111-2222-3333-4444-555555555555"); record.SessionID != want {
		t.Fatalf("session id = %q, want %q", record.SessionID, want)
	}
	if record.AuthID != "opencode-acc" {
		t.Fatalf("auth id = %q, want opencode-acc", record.AuthID)
	}
	if record.Detail.InputTokens != 3 || record.Detail.OutputTokens != 4 {
		t.Fatalf("token detail = %+v", record.Detail)
	}
}

func TestStreamUsageRecordMarksStream(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"x\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)
	capture := newUsageCapture(t)

	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash","stream":true}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	for range result.Chunks {
	}
	record := capture.wait(t)
	if !record.Stream {
		t.Fatal("stream request not marked as stream in usage record")
	}
}

func TestUpstreamErrorRecordsFailureDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"api key expired"}}`)
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)
	capture := newUsageCapture(t)

	if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash"}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err == nil {
		t.Fatal("expected an upstream error")
	}
	record := capture.wait(t)
	if !record.Failed {
		t.Fatal("failed request not marked as failed")
	}
	if record.Fail.StatusCode != http.StatusUnauthorized {
		t.Fatalf("failure status = %d, want 401", record.Fail.StatusCode)
	}
}
