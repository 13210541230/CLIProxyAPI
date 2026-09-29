package basispoints

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

const requestLifecycleHeader = "X-Oai-Basispoints-Request-Id"

type requestScope struct {
	ctx    context.Context
	cancel context.CancelFunc
}

// 直接 WS 连接仍跟随 CPA 的原生请求生命周期；不通过轮询或额外网络请求感知取消。
func (s *Service) interceptUpstreamRequest(raw json.RawMessage) (any, error) {
	var request struct {
		RequestID             string
		SourceFormat          string
		AuthIndex             string
		AuthProvider          string
		AllowExecutorOverride bool
		Model                 string
		RequestedModel        string
	}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "invalid request lifecycle payload")
	}
	cfg := s.config()
	if !request.AllowExecutorOverride || request.AuthIndex == "" || !supportsBPSRequestFormat(request.SourceFormat) || !strings.EqualFold(strings.TrimSpace(request.AuthProvider), AuthProviderID) {
		return map[string]any{}, nil
	}
	model := strings.TrimSpace(request.RequestedModel)
	if model == "" {
		model = strings.TrimSpace(request.Model)
	}
	if !basispointsModelSupported(model, cfg) {
		model = strings.TrimSpace(request.Model)
		if !basispointsModelSupported(model, cfg) {
			return map[string]any{}, nil
		}
	}
	accountEnabled, err := s.basisPointsEnabled(context.Background(), request.AuthIndex)
	if err != nil {
		return map[string]any{
			"Terminate": true, "StatusCode": http.StatusServiceUnavailable,
			"ResponseHeaders": http.Header{"Content-Type": {"application/json"}},
			"ResponseBody": jsonBytes(map[string]any{"error": map[string]any{
				"type": "server_error", "code": "basispoints_routing_unavailable", "message": "Basis Points account routing settings are unavailable",
			}}),
		}, nil
	}
	if !accountEnabled {
		return map[string]any{}, nil
	}
	response := map[string]any{"ExecutorProvider": Provider}
	if cfg.UpstreamTransport == "http" {
		response["ClearHeaders"] = []string{requestLifecycleHeader}
		return response, nil
	}
	if request.RequestID == "" {
		return nil, fail(400, "request_id_missing", "host did not provide a request lifecycle ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, fail(503, "plugin_stopped", "oai-basispoints is shut down")
	}
	if s.requests == nil {
		s.requests = make(map[string]*requestScope)
	}
	if s.requests[request.RequestID] == nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.requests[request.RequestID] = &requestScope{ctx: ctx, cancel: cancel}
	}
	response["Headers"] = http.Header{requestLifecycleHeader: {request.RequestID}}
	return response, nil
}

func supportsBPSRequestFormat(format string) bool {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "openai-response", "codex":
		return true
	default:
		return false
	}
}

func (s *Service) completeUpstreamRequest(raw json.RawMessage) (any, error) {
	var completion struct{ RequestID string }
	if err := json.Unmarshal(raw, &completion); err != nil {
		return nil, fail(400, "invalid_request", "invalid request completion payload")
	}
	s.mu.Lock()
	scope := s.requests[completion.RequestID]
	delete(s.requests, completion.RequestID)
	s.mu.Unlock()
	if scope != nil {
		scope.cancel()
	}
	return map[string]any{}, nil
}

func (s *Service) startRun(request ExecutorRequest) (*runningStream, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil, fail(503, "plugin_stopped", "oai-basispoints is shut down")
	}
	parent := context.Background()
	if id := request.Headers.Get(requestLifecycleHeader); id != "" {
		scope := s.requests[id]
		if scope == nil {
			return nil, fail(499, "client_disconnected", "request already completed")
		}
		parent = scope.ctx
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(s.cfg.TimeoutSeconds)*time.Second)
	run := &runningStream{service: s, ctx: ctx, cancel: cancel, closeHookDone: make(chan struct{})}
	if s.streams == nil {
		s.streams = make(map[*runningStream]struct{})
	}
	s.streams[run] = struct{}{}
	s.streamWG.Add(1)
	run.stopCloseHook = context.AfterFunc(ctx, func() {
		run.closeUpstream()
		close(run.closeHookDone)
	})
	return run, nil
}
