package opencode

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestSessionIDForSignalIsStableAndCanonical(t *testing.T) {
	first := SessionIDForSignal("uuid-1234")
	second := SessionIDForSignal("uuid-1234")
	if first != second {
		t.Fatalf("session id is not deterministic: %q vs %q", first, second)
	}
	if !IsSessionID(first) {
		t.Fatalf("session id %q has invalid shape", first)
	}
	if !isDescendingTimePart(first[4:16]) {
		t.Fatalf("session id %q lacks a descending time prefix", first)
	}
	official := "ses_ffffffffffff0123456789ABCD"
	if got := SessionIDForSignal(official); got != official {
		t.Fatalf("official session id rewritten: %q", got)
	}
	if !IsSessionID("ses_8000000000000123456789ABCD") {
		t.Fatal("canonical session id rejected")
	}
}

func TestProjectIDIsStableHex(t *testing.T) {
	first := ProjectID("ses_test")
	if first != ProjectID("ses_test") {
		t.Fatal("project id is not stable")
	}
	if len(first) != 40 {
		t.Fatalf("project id length = %d, want 40", len(first))
	}
}

func TestApplyHeadersMimicsOpenCodeCLI(t *testing.T) {
	req, err := http.NewRequest(http.MethodPost, "https://example.test/v1/chat/completions", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-opencode-request", "stale")
	ApplyHeaders(req, []byte(`{"stream":true}`), Identity{Session: SessionIDForSignal("abc"), Project: "abc"})

	if got := req.Header.Get("User-Agent"); got != UserAgentValue {
		t.Fatalf("User-Agent = %q", got)
	}
	if got := req.Header.Get("Accept"); got != "text/event-stream" {
		t.Fatalf("Accept = %q", got)
	}
	if got := req.Header.Get("x-opencode-client"); got != "cli" {
		t.Fatalf("x-opencode-client = %q", got)
	}
	if req.Header.Get("x-opencode-session") != req.Header.Get("x-opencode-session-id") ||
		req.Header.Get("x-opencode-session") != req.Header.Get("x-session-affinity") {
		t.Fatal("session headers are inconsistent")
	}
	if req.Header.Get("x-opencode-project") == "" {
		t.Fatal("missing x-opencode-project")
	}
	if req.Header.Get("x-opencode-request") != "" {
		t.Fatal("stale x-opencode-request was not stripped")
	}
}

func TestSetCredentialPerProtocol(t *testing.T) {
	chat, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
	SetCredential(chat, ProtocolChat, "oc_sk_test")
	if got := chat.Header.Get("Authorization"); got != "Bearer oc_sk_test" {
		t.Fatalf("chat Authorization = %q", got)
	}
	if chat.Header.Get("x-api-key") != "" {
		t.Fatal("chat request must not carry x-api-key")
	}

	anthropic, _ := http.NewRequest(http.MethodPost, "https://example.test", nil)
	SetCredential(anthropic, ProtocolAnthropic, "oc_sk_test")
	if got := anthropic.Header.Get("x-api-key"); got != "oc_sk_test" {
		t.Fatalf("anthropic x-api-key = %q", got)
	}
	if anthropic.Header.Get("Authorization") != "" {
		t.Fatal("anthropic request must not carry Authorization")
	}
}

func TestCatalogRefreshResolvesGoProtocols(t *testing.T) {
	capabilities := `{
	  "opencode-go": {"id":"opencode-go","api":"https://opencode.ai/zen/go","npm":"@ai-sdk/openai-compatible","models":{
	    "deepseek-v4.1-flash":{"id":"deepseek-v4.1-flash","limit":{"context":200000,"output":8192},"tool_call":true},
	    "gpt-native":{"id":"gpt-native","provider":{"npm":"@ai-sdk/openai"}},
	    "claude-native":{"id":"claude-native","provider":{"npm":"@ai-sdk/anthropic"}}
	  }},
	  "opencode-zen": {"id":"opencode-zen","api":"https://opencode.ai/zen","npm":"@ai-sdk/openai-compatible","models":{
	    "zen-only":{"id":"zen-only"}
	  }},
	  "opencode-inference": {"id":"opencode-inference","api":"https://opencode.ai/inference","models":{
	    "free-model":{"id":"free-model"}
	  }}
	}`
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api.json":
			_, _ = io.WriteString(w, capabilities)
		case "/v1/models":
			_, _ = io.WriteString(w, `{"data":[{"id":"extra-model"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	catalog := NewCatalog(map[string]Protocol{"gpt-native": ProtocolChat})
	if err := catalog.Refresh(context.Background(), server.Client(), server.URL+"/api.json", server.URL+"/v1/models", ""); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	cases := map[string]Protocol{
		"deepseek-v4.1-flash": ProtocolChat,
		"claude-native":       ProtocolAnthropic,
		"gpt-native":          ProtocolChat, // manual override wins
		"extra-model":         ProtocolChat, // live list fallback
	}
	for model, want := range cases {
		got, ok := catalog.Protocol(model)
		if !ok {
			t.Fatalf("model %q not resolved", model)
		}
		if got != want {
			t.Fatalf("model %q protocol = %q, want %q", model, got, want)
		}
	}
	if _, ok := catalog.Protocol("zen-only"); ok {
		t.Fatal("zen-only model leaked into the Go catalog")
	}
	if _, ok := catalog.Protocol("free-model"); ok {
		t.Fatal("anonymous inference model leaked into the Go catalog")
	}
	if !catalog.Loaded() || catalog.FetchedAt().IsZero() {
		t.Fatal("catalog did not record a successful load")
	}
}

func TestFetchQuotaParsesWindows(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer oc_sk_test" {
			t.Errorf("Authorization = %q", got)
		}
		_, _ = io.WriteString(w, `{"usage":{
		  "rolling":{"status":"ok","percent":12.5,"resetsAt":"2026-10-20T16:00:37Z"},
		  "weekly":{"status":"ok","percent":30},
		  "monthly":{"status":"ok","percent":45}}}`)
	}))
	defer server.Close()

	snapshot, err := FetchQuota(context.Background(), server.Client(), server.URL+"/zen/go/v1/usage", "oc_sk_test")
	if err != nil {
		t.Fatalf("fetch quota: %v", err)
	}
	if snapshot.Rolling.Percent != 12.5 || snapshot.Weekly.Percent != 30 || snapshot.Monthly.Percent != 45 {
		t.Fatalf("unexpected percentages: %+v", snapshot)
	}
	if snapshot.Rolling.ResetsAt.Year() != 2026 {
		t.Fatalf("resetsAt not parsed: %+v", snapshot.Rolling)
	}
	if snapshot.Exhausted() {
		t.Fatal("snapshot unexpectedly exhausted")
	}
}

func newTestExecutor(t *testing.T, server *httptest.Server) *Executor {
	t.Helper()
	e := New(&config.Config{})
	e.baseURL = server.URL
	return e
}

func testAuth() *coreauth.Auth {
	return &coreauth.Auth{ID: "opencode-acc", Provider: ProviderName, Metadata: map[string]any{"api_key": "oc_sk_test"}}
}

func TestExecuteChatPassthrough(t *testing.T) {
	var gotPath, gotAuth, gotUA, gotSession string
	var gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUA = r.Header.Get("User-Agent")
		gotSession = r.Header.Get("x-opencode-session")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","object":"chat.completion","choices":[]}`)
	}))
	defer server.Close()

	executor := newTestExecutor(t, server)
	payload := []byte(`{"model":"deepseek-v4.1-flash","messages":[]}`)
	response, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: payload,
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if gotPath != "/zen/go/v1/chat/completions" {
		t.Fatalf("upstream path = %q", gotPath)
	}
	if gotAuth != "Bearer oc_sk_test" {
		t.Fatalf("upstream Authorization = %q", gotAuth)
	}
	if gotUA != UserAgentValue {
		t.Fatalf("upstream User-Agent = %q", gotUA)
	}
	if !IsSessionID(gotSession) {
		t.Fatalf("upstream session header = %q", gotSession)
	}
	if gotBody != string(payload) {
		t.Fatalf("payload was rewritten: %q", gotBody)
	}
	if !strings.Contains(string(response.Payload), "chatcmpl-1") {
		t.Fatalf("unexpected response payload: %s", response.Payload)
	}
}

func TestExecuteRoutesNativeProtocols(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{}`)
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)

	// Distinct models keep the learned-protocol cache from short-circuiting the
	// second iteration (a model served on one endpoint is translated thereafter).
	for format, wantPath := range map[sdktranslator.Format]string{
		sdktranslator.FormatOpenAIResponse: "/zen/go/v1/responses",
		sdktranslator.FormatClaude:         "/zen/go/v1/messages",
	} {
		model := "model-" + string(format)
		if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
			Model:   model,
			Payload: []byte(`{"model":"` + model + `"}`),
			Format:  format,
		}, cliproxyexecutor.Options{SourceFormat: format}); err != nil {
			t.Fatalf("execute %s: %v", format, err)
		}
		if paths[len(paths)-1] != wantPath {
			t.Fatalf("format %s routed to %q, want %q", format, paths[len(paths)-1], wantPath)
		}
	}
}

func TestExecuteStreamPassthrough(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)

	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash","stream":true}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	var collected []string
	for chunk := range result.Chunks {
		collected = append(collected, string(chunk.Payload))
	}
	joined := strings.Join(collected, "\n")
	if len(collected) != 1 || collected[0] != `{"choices":[{"delta":{"content":"hi"}}]}` {
		t.Fatalf("unexpected stream chunks: %q", joined)
	}
}

func TestExecuteUpstreamErrorCarriesStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"api key expired"}}`)
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)

	_, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "m",
		Payload: []byte(`{"model":"m"}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil {
		t.Fatal("expected an error")
	}
	statusErr, ok := err.(interface{ StatusCode() int })
	if !ok || statusErr.StatusCode() != http.StatusUnauthorized {
		t.Fatalf("status error = %#v", err)
	}
	if !strings.Contains(err.Error(), "api key expired") {
		t.Fatalf("error message lost upstream detail: %v", err)
	}
}

func TestRequestToFormatUsesCatalog(t *testing.T) {
	executor := New(&config.Config{})
	executor.catalog = NewCatalog(map[string]Protocol{"gpt-native": ProtocolResponses})
	if got := executor.RequestToFormat(cliproxyexecutor.Request{Model: "gpt-native"}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); got != sdktranslator.FormatOpenAIResponse {
		t.Fatalf("RequestToFormat = %q, want responses", got)
	}
	if got := executor.RequestToFormat(cliproxyexecutor.Request{Model: "unknown"}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}); got != sdktranslator.FormatClaude {
		t.Fatalf("unknown model should keep the inbound format, got %q", got)
	}
}

func TestMissingAPIKeyIsRejectedBeforeDial(t *testing.T) {
	executor := New(&config.Config{})
	executor.baseURL = "http://127.0.0.1:1"
	_, err := executor.Execute(context.Background(), &coreauth.Auth{ID: "no-key"}, cliproxyexecutor.Request{
		Model:   "m",
		Payload: []byte(`{"model":"m"}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI})
	if err == nil || !strings.Contains(err.Error(), "api_key") {
		t.Fatalf("expected missing api key error, got %v", err)
	}
}

func TestQuotaSnapshotExhausted(t *testing.T) {
	snapshot := QuotaSnapshot{Rolling: Window{Percent: 100}, FetchedAt: time.Now()}
	if !snapshot.Exhausted() {
		t.Fatal("100% rolling window must be exhausted")
	}
	if got := snapshot.MinPercent(); got != 100 {
		t.Fatalf("MinPercent = %v", got)
	}
}

func TestAccountViewsMasksKeysAndProjectsQuota(t *testing.T) {
	executor := New(&config.Config{})
	executor.quota.Record("acc-1", QuotaSnapshot{
		Rolling:   Window{Status: "ok", Percent: 12.5, ResetsAt: time.Now().Add(time.Hour)},
		Weekly:    Window{Status: "ok", Percent: 30},
		Monthly:   Window{Status: "ok", Percent: 45},
		FetchedAt: time.Now(),
	})
	auths := []*coreauth.Auth{
		{ID: "acc-1", Provider: ProviderName, Label: "first", Status: "active", Metadata: map[string]any{"api_key": "oc_sk_supersecretvalue"}},
		{ID: "other", Provider: "codex"},
	}
	views := executor.AccountViews(auths)
	if len(views) != 1 {
		t.Fatalf("expected only opencode auths, got %d", len(views))
	}
	view := views[0]
	if strings.Contains(view.KeyHint, "supersecret") {
		t.Fatalf("api key leaked in hint: %q", view.KeyHint)
	}
	if view.Quota == nil || view.Quota.Rolling.Percent != 12.5 || view.Quota.Exhausted {
		t.Fatalf("quota not projected: %+v", view.Quota)
	}
}

func TestSelectAccountSkipsExhaustedAndPicksHeadroom(t *testing.T) {
	executor := New(&config.Config{})
	executor.quota.Record("tight", QuotaSnapshot{Rolling: Window{Percent: 95}, FetchedAt: time.Now()})
	executor.quota.Record("roomy", QuotaSnapshot{Rolling: Window{Percent: 10}, FetchedAt: time.Now()})
	executor.quota.Record("done", QuotaSnapshot{Monthly: Window{Percent: 100}, FetchedAt: time.Now()})
	auths := []*coreauth.Auth{
		{ID: "tight", Provider: ProviderName},
		{ID: "roomy", Provider: ProviderName},
		{ID: "done", Provider: ProviderName},
		{ID: "disabled", Provider: ProviderName, Disabled: true},
	}
	chosen := executor.SelectAccount(auths)
	if chosen == nil || chosen.ID != "roomy" {
		t.Fatalf("selected %v, want roomy", chosen)
	}
}

func TestExecuteFallsBackWhenEndpointUnavailable(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = io.WriteString(w, `{"error":{"message":"Endpoint is unavailable."}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer server.Close()
	executor := newTestExecutor(t, server)

	model := "switching-model"
	payload := []byte(`{"model":"switching-model","messages":[{"role":"user","content":"hi"}]}`)
	if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("first execute: %v", err)
	}
	if paths[0] != "/zen/go/v1/chat/completions" {
		t.Fatalf("first attempt = %q, want chat endpoint", paths[0])
	}
	if len(paths) < 2 || !strings.HasSuffix(paths[len(paths)-1], "/responses") {
		t.Fatalf("fallback attempts = %v, want a /responses retry", paths)
	}
	if learned, ok := executor.learnedProtocol(model); !ok || learned != ProtocolResponses {
		t.Fatalf("learned protocol = %v (%v), want responses", learned, ok)
	}

	// Second request for the same model must go straight to the learned endpoint.
	paths = nil
	if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   model,
		Payload: payload,
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("second execute: %v", err)
	}
	if len(paths) != 1 || !strings.HasSuffix(paths[0], "/responses") {
		t.Fatalf("second request attempts = %v, want a single /responses call", paths)
	}
}

func TestEndpointUnavailableDetection(t *testing.T) {
	if !isEndpointUnavailable(&upstreamError{code: http.StatusUnprocessableEntity, message: "opencode: Endpoint is unavailable."}) {
		t.Fatal("422 endpoint-unavailable must be retryable")
	}
	if isEndpointUnavailable(&upstreamError{code: http.StatusUnauthorized, message: "opencode: api key expired"}) {
		t.Fatal("401 must not be retried on another endpoint")
	}
	if isEndpointUnavailable(&upstreamError{code: http.StatusBadRequest, message: "opencode: Param Incorrect"}) {
		t.Fatal("generic 400 must not be retried")
	}
}

type usageCapture struct{ records chan usage.Record }

func (c *usageCapture) HandleUsage(_ context.Context, record usage.Record) {
	select {
	case c.records <- record:
	default:
	}
}

func newUsageCapture(t *testing.T) *usageCapture {
	t.Helper()
	capture := &usageCapture{records: make(chan usage.Record, 8)}
	usage.RegisterNamedPlugin("opencode-test-"+t.Name(), capture)
	return capture
}

func (c *usageCapture) wait(t *testing.T) usage.Record {
	t.Helper()
	select {
	case record := <-c.records:
		return record
	case <-time.After(3 * time.Second):
		t.Fatal("no usage record published")
		return usage.Record{}
	}
}

func TestExecuteReportsUsage(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"id":"chatcmpl-1","usage":{"prompt_tokens":5,"completion_tokens":7,"total_tokens":12},"choices":[]}`)
	}))
	defer server.Close()
	capture := newUsageCapture(t)
	executor := newTestExecutor(t, server)

	if _, err := executor.Execute(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash","messages":[]}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	record := capture.wait(t)
	if record.Provider != ProviderName {
		t.Fatalf("usage provider = %q, want %q", record.Provider, ProviderName)
	}
	if record.Detail.TotalTokens != 12 {
		t.Fatalf("usage total tokens = %d, want 12", record.Detail.TotalTokens)
	}
	if record.Model != "deepseek-v4.1-flash" {
		t.Fatalf("usage model = %q", record.Model)
	}
}

func TestExecuteStreamReportsUsageAndRequestsUsageChunk(t *testing.T) {
	var upstreamBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBody = string(raw)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":9,\"completion_tokens\":3,\"total_tokens\":12}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer server.Close()
	capture := newUsageCapture(t)
	executor := newTestExecutor(t, server)

	result, err := executor.ExecuteStream(context.Background(), testAuth(), cliproxyexecutor.Request{
		Model:   "deepseek-v4.1-flash",
		Payload: []byte(`{"model":"deepseek-v4.1-flash","stream":true,"messages":[]}`),
		Format:  sdktranslator.FormatOpenAI,
	}, cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAI, Stream: true})
	if err != nil {
		t.Fatalf("execute stream: %v", err)
	}
	for range result.Chunks {
	}
	record := capture.wait(t)
	if record.Detail.TotalTokens != 12 {
		t.Fatalf("stream usage total tokens = %d, want 12", record.Detail.TotalTokens)
	}
	if !strings.Contains(upstreamBody, `"include_usage":true`) {
		t.Fatalf("chat stream must request a usage chunk, upstream body=%s", upstreamBody)
	}
}

func TestEnsureStreamUsageOnlyForChat(t *testing.T) {
	body := []byte(`{"stream":true}`)
	chat := ensureStreamUsage(body, ProtocolChat)
	if !strings.Contains(string(chat), `"include_usage":true`) {
		t.Fatal("chat stream did not request usage")
	}
	if got := ensureStreamUsage(body, ProtocolResponses); string(got) != string(body) {
		t.Fatalf("responses stream must stay untouched, got %s", got)
	}
	if got := ensureStreamUsage(body, ProtocolAnthropic); string(got) != string(body) {
		t.Fatalf("anthropic stream must stay untouched, got %s", got)
	}
	kept := ensureStreamUsage([]byte(`{"stream":true,"stream_options":{"include_usage":false}}`), ProtocolChat)
	if strings.Contains(string(kept), "false") {
		t.Fatalf("explicit client value must be overridden, got %s", kept)
	}
}
