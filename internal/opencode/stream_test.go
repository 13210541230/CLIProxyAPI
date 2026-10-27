package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	_ "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator/builtin"
)

func TestOpenCodeStreamNativeFraming(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format sdktranslator.Format
		wire   string
		want   string
	}{
		{
			name:   "chat payloads without SSE prefix or DONE",
			format: sdktranslator.FormatOpenAI,
			wire:   "data: {\"choices\":[{\"delta\":{\"content\":\"OK\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
			want:   `{"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}`,
		},
		{
			name:   "claude preserves event names comments and multiline data",
			format: sdktranslator.FormatClaude,
			wire:   ": ping\r\nid: 1\r\nevent: message_start\r\ndata: {\"type\":\"message_start\",\r\ndata: \"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":9}}}\r\n\r\nevent: message_stop\r\ndata: {\"type\":\"message_stop\"}\r\n\r\n",
			want:   ": ping\nid: 1\nevent: message_start\ndata: {\"type\":\"message_start\",\ndata: \"message\":{\"id\":\"msg_1\",\"usage\":{\"input_tokens\":9}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		},
		{
			name:   "responses preserves complete SSE frames",
			format: sdktranslator.FormatOpenAIResponse,
			wire:   "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n",
			want:   "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, tc.wire)
			}))
			defer server.Close()
			executor := &Executor{cfg: &config.Config{}, baseURL: server.URL, goPrefix: defaultGoPathPrefix, catalog: NewCatalog(nil)}
			request := []byte(`{"model":"stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{Model: "stream-model", Payload: request, Format: tc.format}, cliproxyexecutor.Options{SourceFormat: tc.format, ResponseFormat: tc.format, OriginalRequest: request, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			if got := output.String(); got != tc.want {
				t.Fatalf("executor framing mismatch:\ngot  %q\nwant %q", got, tc.want)
			}
		})
	}
}

func TestOpenCodeStreamReadError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Content-Length", "4096")
		_, _ = io.WriteString(w, "event: message_start\ndata: {\"type\":\"message_start\"}\n\n")
	}))
	defer server.Close()
	executor := &Executor{cfg: &config.Config{}, baseURL: server.URL, goPrefix: defaultGoPathPrefix, catalog: NewCatalog(nil)}
	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{Model: "stream-model", Payload: []byte(`{"model":"stream-model","stream":true}`), Format: sdktranslator.FormatClaude}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var streamErr error
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			streamErr = chunk.Err
		}
	}
	if !errors.Is(streamErr, io.ErrUnexpectedEOF) {
		t.Fatalf("got stream error %v, want unexpected EOF", streamErr)
	}
}

func TestOpenCodeStreamOwnsChatChunks(t *testing.T) {
	var want []string
	for i := 0; i < 256; i++ {
		data, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"delta": map[string]any{"content": strings.Repeat(string(rune('a'+i%26)), 512)}}}})
		if err != nil {
			t.Fatal(err)
		}
		want = append(want, string(data))
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		for _, data := range want {
			_, _ = io.WriteString(w, "data: "+data+"\n\n")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	executor := &Executor{cfg: &config.Config{}, baseURL: server.URL, goPrefix: defaultGoPathPrefix, catalog: NewCatalog(nil)}
	request := []byte(`{"model":"stream-model","stream":true,"messages":[]}`)
	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{Model: "stream-model", Payload: request, Format: sdktranslator.FormatOpenAI}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatal(err)
	}
	var retained [][]byte
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			t.Fatal(chunk.Err)
		}
		retained = append(retained, chunk.Payload)
	}
	if len(retained) != len(want) {
		t.Fatalf("got %d chunks, want %d", len(retained), len(want))
	}
	for i, data := range retained {
		if string(data) != want[i] {
			t.Fatalf("chunk %d changed after subsequent scanner reads", i)
		}
	}
}

func TestOpenCodeStreamChatToClaude(t *testing.T) {
	for _, multiline := range []bool{false, true} {
		name := "single-line data"
		if multiline {
			name = "multiline data"
		}
		t.Run(name, func(t *testing.T) {
			var paths []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				paths = append(paths, r.URL.Path)
				if r.URL.Path == "/zen/go/v1/messages" {
					w.WriteHeader(http.StatusUnprocessableEntity)
					_, _ = io.WriteString(w, `{"error":{"message":"Endpoint is unavailable."}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				for _, data := range []string{
					`{"id":"chat_1","model":"stream-model","choices":[{"index":0,"delta":{"role":"assistant","content":"OK"},"finish_reason":null}]}`,
					`{"id":"chat_1","model":"stream-model","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":\"x\"}"}}]},"finish_reason":null}]}`,
					`{"id":"chat_1","model":"stream-model","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":9,"completion_tokens":3,"total_tokens":12}}`,
					`[DONE]`,
				} {
					if multiline {
						data = strings.Replace(data, `,"model"`, ",\ndata: \"model\"", 1)
					}
					_, _ = io.WriteString(w, "data: "+data+"\n\n")
				}
			}))
			defer server.Close()
			executor := &Executor{cfg: &config.Config{}, baseURL: server.URL, goPrefix: defaultGoPathPrefix, catalog: NewCatalog(nil)}
			request := []byte(`{"model":"stream-model","stream":true,"max_tokens":64,"messages":[{"role":"user","content":"hi"}],"tools":[{"name":"lookup","input_schema":{"type":"object","properties":{"q":{"type":"string"}}}}]}`)
			result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{Model: "stream-model", Payload: request, Format: sdktranslator.FormatClaude}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, ResponseFormat: sdktranslator.FormatClaude, OriginalRequest: request, Stream: true})
			if err != nil {
				t.Fatal(err)
			}
			var output strings.Builder
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
				output.Write(chunk.Payload)
			}
			body := output.String()
			for _, want := range []string{"event: message_start\n", `"text":"OK"`, `"name":"lookup"`, `"stop_reason":"tool_use"`, `"output_tokens":3`, "event: message_stop\n"} {
				if !strings.Contains(body, want) {
					t.Errorf("missing %q in Claude stream: %s", want, body)
				}
			}
			if strings.Count(body, "event: message_stop\n") != 1 {
				t.Errorf("expected one terminal event: %s", body)
			}
			for _, line := range strings.Split(body, "\n") {
				if strings.HasPrefix(line, "data:") && !json.Valid([]byte(strings.TrimPrefix(line, "data:"))) {
					t.Errorf("invalid SSE JSON: %q", line)
				}
			}
			if strings.Join(paths, ",") != "/zen/go/v1/messages,/zen/go/v1/chat/completions" {
				t.Errorf("unexpected fallback path: %v", paths)
			}
		})
	}
}
