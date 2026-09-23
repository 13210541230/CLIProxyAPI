package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexec "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

// admissionProbeInterceptor records the after-auth intercept the alpha-search
// entry must issue, and can terminate to emulate a full concurrency slot.
type admissionProbeInterceptor struct {
	mu          sync.Mutex
	afterAuth   []pluginapi.RequestInterceptRequest
	terminate   bool
	completions []pluginapi.RequestCompletion
	completeCh  chan pluginapi.RequestCompletion
}

func (p *admissionProbeInterceptor) InterceptRequestBeforeAuth(context.Context, pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *admissionProbeInterceptor) InterceptRequestAfterAuth(_ context.Context, req pluginapi.RequestInterceptRequest) (pluginapi.RequestInterceptResponse, error) {
	p.mu.Lock()
	p.afterAuth = append(p.afterAuth, req)
	terminate := p.terminate
	p.mu.Unlock()
	if terminate {
		return pluginapi.RequestInterceptResponse{
			Terminate:       true,
			StatusCode:      http.StatusServiceUnavailable,
			ResponseBody:    []byte(`{"error":"account_busy"}`),
			ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		}, nil
	}
	return pluginapi.RequestInterceptResponse{}, nil
}

func (p *admissionProbeInterceptor) HandleRequestComplete(_ context.Context, completion pluginapi.RequestCompletion) error {
	p.mu.Lock()
	p.completions = append(p.completions, completion)
	p.mu.Unlock()
	if p.completeCh != nil {
		p.completeCh <- completion
	}
	return nil
}

// awaitCompletion blocks until the lifecycle completion arrives. Completions
// are dispatched asynchronously by the host, so tests must synchronize on the
// event instead of asserting immediately after ServeHTTP returns.
func (p *admissionProbeInterceptor) awaitCompletion(t *testing.T) pluginapi.RequestCompletion {
	t.Helper()
	select {
	case completion := <-p.completeCh:
		return completion
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle completion never arrived")
		return pluginapi.RequestCompletion{}
	}
}

// newAlphaSearchRequest builds the canonical alpha-search probe request.
func newAlphaSearchRequest(t *testing.T) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/v1/alpha/search", strings.NewReader(`{"query":"GPT-5.6"}`))
	req.Header.Set("Authorization", "Bearer test-key")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Session_id", "alpha-session")
	return req
}

func newAdmissionProbeServer(t *testing.T, probe *admissionProbeInterceptor) (*Server, *codexSearchCaptureExecutor) {
	t.Helper()
	host := pluginhost.New()
	host.RegisterPluginForTest("admission-probe", pluginapi.Plugin{
		Metadata: pluginapi.Metadata{Name: "admission-probe"},
		Capabilities: pluginapi.Capabilities{
			RequestInterceptor:     probe,
			RequestLifecyclePlugin: probe,
		},
	})
	server := newTestServerWithOptions(t, WithPluginHost(host))
	executor := &codexSearchCaptureExecutor{}
	server.handlers.AuthManager.RegisterExecutor(executor)
	credential := &auth.Auth{
		ID:       "codex-auth",
		Provider: "codex",
		Status:   auth.StatusActive,
		Metadata: map[string]any{"access_token": "codex-token", "account_id": "account-123"},
	}
	if _, errRegister := server.handlers.AuthManager.Register(context.Background(), credential); errRegister != nil {
		t.Fatalf("register Codex auth: %v", errRegister)
	}
	return server, executor
}

// TestCodexAlphaSearchRunsAfterAuthAdmission locks the P1-4 fix: the alpha
// search entry routes its selected credential through the same after-auth
// admission the executor pipeline uses, so explicit per-account concurrency
// limits cannot be bypassed on this entry.
func TestCodexAlphaSearchRunsAfterAuthAdmission(t *testing.T) {
	probe := &admissionProbeInterceptor{completeCh: make(chan pluginapi.RequestCompletion, 4)}
	server, executor := newAdmissionProbeServer(t, probe)

	req := newAlphaSearchRequest(t)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	completion := probe.awaitCompletion(t)
	probe.mu.Lock()
	afterAuthCount := len(probe.afterAuth)
	var admitted *pluginapi.RequestInterceptRequest
	if afterAuthCount > 0 {
		captured := probe.afterAuth[afterAuthCount-1]
		admitted = &captured
	}
	probe.mu.Unlock()

	if admitted == nil {
		t.Fatal("after-auth admission never ran for alpha search")
	}
	selected, ok := admitted.Metadata[coreexec.SelectedAuthMetadataKey].(string)
	if !ok || selected == "" {
		t.Fatalf("admission metadata selected_auth_id = %v, want the selected credential id", admitted.Metadata[coreexec.SelectedAuthMetadataKey])
	}
	if selected != "codex-auth" {
		t.Fatalf("admission selected auth = %q, want codex-auth", selected)
	}
	if admitted.SourceFormat != "codex-alpha-search" {
		t.Fatalf("admission source format = %q", admitted.SourceFormat)
	}
	if admitted.RequestID == "" {
		t.Fatal("admission request id is empty; completion would be unlinkable")
	}
	if completion.RequestID != admitted.RequestID {
		t.Fatalf("completion request id %q != admission %q", completion.RequestID, admitted.RequestID)
	}
	if completion.Outcome != pluginapi.RequestCompletionSucceeded {
		t.Fatalf("completion outcome = %q, want succeeded", completion.Outcome)
	}
	if executor.request == nil {
		t.Fatal("executor did not receive the request")
	}
}

func TestCodexAlphaSearchReportsUpstreamFailureCompletion(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusBadGateway} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			probe := &admissionProbeInterceptor{completeCh: make(chan pluginapi.RequestCompletion, 4)}
			server, executor := newAdmissionProbeServer(t, probe)
			executor.statuses = []int{status}

			rr := httptest.NewRecorder()
			server.engine.ServeHTTP(rr, newAlphaSearchRequest(t))
			if rr.Code != status {
				t.Fatalf("status = %d, want %d", rr.Code, status)
			}
			completion := probe.awaitCompletion(t)
			if completion.Outcome != pluginapi.RequestCompletionFailed {
				t.Fatalf("completion outcome = %q, want failed", completion.Outcome)
			}
			if completion.StatusCode != status {
				t.Fatalf("completion status = %d, want %d", completion.StatusCode, status)
			}
		})
	}
}

// TestCodexAlphaSearchAdmissionTerminateBlocksUpstream proves a terminated
// admission (full account) stops the request before the upstream and still
// releases the admission slot with a rejected outcome.
func TestCodexAlphaSearchAdmissionTerminateBlocksUpstream(t *testing.T) {
	probe := &admissionProbeInterceptor{terminate: true, completeCh: make(chan pluginapi.RequestCompletion, 4)}
	server, executor := newAdmissionProbeServer(t, probe)

	req := newAlphaSearchRequest(t)
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d; body=%s", rr.Code, http.StatusServiceUnavailable, rr.Body.String())
	}
	if got := rr.Body.String(); !strings.Contains(got, "account_busy") {
		t.Fatalf("body = %q, want account_busy passthrough", got)
	}
	if executor.request != nil {
		t.Fatal("upstream executor was called despite terminated admission")
	}
	completion := probe.awaitCompletion(t)
	if completion.Outcome != pluginapi.RequestCompletionRejected {
		t.Fatalf("completion outcome = %q, want rejected", completion.Outcome)
	}
	if completion.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("completion status = %d, want %d", completion.StatusCode, http.StatusServiceUnavailable)
	}
}
