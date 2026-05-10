package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestOpenAICompatExecutorRerankPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"results":[{"index":0,"relevance_score":0.98}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"bge-reranker-v2-m3","query":"安装node","documents":["a","b"],"top_n":1}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "bge-reranker-v2-m3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/rerank",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/rerank" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/rerank")
	}
	if gjson.GetBytes(gotBody, "messages").Exists() {
		t.Fatalf("unexpected messages in rerank body: %s", string(gotBody))
	}
	if gjson.GetBytes(gotBody, "documents.0").String() != "a" {
		t.Fatalf("expected documents passthrough, got body=%s", string(gotBody))
	}
	if !gjson.GetBytes(resp.Payload, "results.0.relevance_score").Exists() {
		t.Fatalf("expected rerank response passthrough, got payload=%s", string(resp.Payload))
	}
}
