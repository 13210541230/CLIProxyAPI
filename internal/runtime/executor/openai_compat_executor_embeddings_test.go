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

func TestOpenAICompatExecutorEmbeddingsPassthrough(t *testing.T) {
	var gotPath string
	var gotBody []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[{"object":"embedding","embedding":[0.1,0.2],"index":0}]}`))
	}))
	defer server.Close()

	executor := NewOpenAICompatExecutor("openai-compatibility", &config.Config{})
	auth := &cliproxyauth.Auth{Attributes: map[string]string{
		"base_url": server.URL + "/v1",
		"api_key":  "test",
	}}
	payload := []byte(`{"model":"BAAI/bge-m3","input":"hello"}`)

	resp, err := executor.Execute(context.Background(), auth, cliproxyexecutor.Request{
		Model:   "BAAI/bge-m3",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
		Stream:       false,
		Metadata: map[string]any{
			cliproxyexecutor.RequestPathMetadataKey: "/v1/embeddings",
		},
	})
	if err != nil {
		t.Fatalf("Execute error: %v", err)
	}
	if gotPath != "/v1/embeddings" {
		t.Fatalf("path = %q, want %q", gotPath, "/v1/embeddings")
	}
	if gjson.GetBytes(gotBody, "input").String() != "hello" {
		t.Fatalf("expected input passthrough, got body=%s", string(gotBody))
	}
	if !gjson.GetBytes(resp.Payload, "data.0.embedding.0").Exists() {
		t.Fatalf("expected embeddings response passthrough, got payload=%s", string(resp.Payload))
	}
}
