package opencode

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
)

// ProviderName is the auth-file "type" that selects this executor.
const ProviderName = "opencode"

const (
	defaultBaseURL          = "https://opencode.ai"
	defaultGoPathPrefix     = "/zen/go"
	defaultModelRefresh     = 6 * time.Hour
	defaultQuotaInterval    = 5 * time.Minute
	defaultCatalogModelURL  = "https://opencode.ai/zen/go/v1/models"
	goQuotaPath             = "/zen/go/v1/usage"
	refreshTimeout          = 30 * time.Second
	upstreamErrorBodyLimit  = 64 << 10
	registryClientID        = "opencode"
	envBaseURL              = "OPENCODE_BASE_URL"
	envCapabilitiesURL      = "OPENCODE_CAPABILITIES_URL"
	envProtocols            = "OPENCODE_PROTOCOLS"
	envModelRefresh         = "OPENCODE_MODEL_REFRESH_INTERVAL"
	envQuotaInterval        = "OPENCODE_QUOTA_INTERVAL"
	envDisableAccountHealth = "OPENCODE_DISABLE_ACCOUNT_HEALTH"
)

// Executor relays requests to the OpenCode Go plan on behalf of any harness.
type Executor struct {
	cfg         *config.Config
	baseURL     string
	goPrefix    string
	catalogURL  string
	modelsURL   string
	catalog     *Catalog
	quota       *QuotaTracker
	refreshEach time.Duration
	quotaEach   time.Duration

	learned sync.Map // model -> Protocol resolved from upstream endpoint availability

	loadMu      sync.Mutex
	mu          sync.Mutex
	lastRefresh time.Time
	lastQuota   time.Time
}

// New creates the OpenCode executor from configuration and environment overrides.
func New(cfg *config.Config) *Executor {
	baseURL := strings.TrimRight(envOr(envBaseURL, defaultBaseURL), "/")
	executor := &Executor{
		cfg:         cfg,
		baseURL:     baseURL,
		goPrefix:    defaultGoPathPrefix,
		catalogURL:  envOr(envCapabilitiesURL, DefaultCapabilitiesURL),
		modelsURL:   baseURL + "/zen/go/v1/models",
		catalog:     NewCatalog(parseProtocolOverrides(os.Getenv(envProtocols))),
		quota:       NewQuotaTracker(),
		refreshEach: envDuration(envModelRefresh, defaultModelRefresh),
		quotaEach:   envDuration(envQuotaInterval, defaultQuotaInterval),
	}
	defaultExecutor.Store(executor)
	// Publish the model catalog eagerly: routing resolves the provider from the
	// registry before any request reaches RequestToFormat, so a lazy-only refresh
	// would leave every model unresolvable on a cold start.
	// Load the catalog and publish models synchronously: the service's own model
	// registration for a new provider runs in a deferred pass, which would leave the
	// provider unresolvable for the first seconds after startup.
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	if err := executor.RefreshCatalog(ctx); err != nil {
		log.Warnf("[opencode] initial model catalog refresh failed: %v", err)
	}
	cancel()
	executor.RegisterModels()
	executor.registerAuthModels()
	return executor
}

// NewExecutor creates the OpenCode executor.
func NewExecutor(cfg *config.Config) *Executor { return New(cfg) }

// Identifier returns the provider key handled by this executor.
func (e *Executor) Identifier() string { return ProviderName }

// RequestToFormat declares the upstream wire format for the selected model so the
// core pipeline translates the inbound payload once, before execution.
func (e *Executor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if protocol, ok := e.learnedProtocol(req.Model); ok {
		return protocol.Format()
	}
	e.ensureCatalog()
	if protocol, ok := e.catalog.Protocol(req.Model); ok {
		return protocol.Format()
	}
	if _, ok := ProtocolForFormat(opts.SourceFormat); ok {
		return opts.SourceFormat
	}
	return sdktranslator.FormatOpenAI
}

// Execute performs a non-streaming request.
func (e *Executor) Execute(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	httpResp, upstreamFormat, err := e.doSend(ctx, auth, req, opts, false)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	defer httpResp.Body.Close()
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		return cliproxyexecutor.Response{}, &upstreamError{code: http.StatusBadGateway, message: "opencode: read upstream body failed"}
	}
	downstream := opts.ResponseFormat
	if downstream == "" {
		downstream = opts.SourceFormat
	}
	if downstream == upstreamFormat {
		return cliproxyexecutor.Response{Payload: data, Headers: httpResp.Header.Clone()}, nil
	}
	log.Infof("[opencode] translated non-stream response %s -> %s model=%s", upstreamFormat, downstream, req.Model)
	var param any
	out := sdktranslator.TranslateNonStream(ctx, upstreamFormat, downstream, req.Model, opts.OriginalRequest, req.Payload, data, &param)
	if len(out) == 0 {
		return cliproxyexecutor.Response{}, &upstreamError{code: http.StatusBadGateway, message: "opencode: response translation failed"}
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

// ExecuteStream performs a streaming request and relays SSE chunks downstream.
func (e *Executor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	httpResp, upstreamFormat, err := e.doSend(ctx, auth, req, opts, true)
	if err != nil {
		return nil, err
	}
	downstream := opts.ResponseFormat
	if downstream == "" {
		downstream = opts.SourceFormat
	}
	if downstream != upstreamFormat {
		log.Infof("[opencode] translated stream response %s -> %s model=%s", upstreamFormat, downstream, req.Model)
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(chunks)
		defer httpResp.Body.Close()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(make([]byte, 0, 64<<10), 8<<20)
		var param any
		for scanner.Scan() {
			line := bytes.TrimRight(scanner.Bytes(), "\r")
			if len(line) == 0 {
				continue
			}
			if !bytes.HasPrefix(line, []byte("data:")) {
				continue
			}
			payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
			if len(payload) == 0 {
				continue
			}
			if downstream == upstreamFormat {
				chunks <- cliproxyexecutor.StreamChunk{Payload: append([]byte("data: "), payload...)}
				continue
			}
			for _, translated := range sdktranslator.TranslateStream(ctx, upstreamFormat, downstream, req.Model, opts.OriginalRequest, req.Payload, payload, &param) {
				if len(translated) == 0 {
					continue
				}
				chunks <- cliproxyexecutor.StreamChunk{Payload: translated}
			}
		}
		if errScan := scanner.Err(); errScan != nil && ctx.Err() == nil {
			log.Warnf("[opencode] stream read error model=%s: %v", req.Model, errScan)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: chunks}, nil
}

// Refresh is a no-op: Go-plan credentials are static API keys.
func (e *Executor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

// CountTokens is not supported upstream; callers fall back to estimation.
func (e *Executor) CountTokens(context.Context, *coreauth.Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &upstreamError{code: http.StatusNotImplemented, message: "opencode: count tokens is not supported"}
}

// HttpRequest injects the OpenCode identity and credential into a prepared request.
func (e *Executor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, &upstreamError{code: http.StatusBadRequest, message: "opencode: request is nil"}
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	ApplyHeaders(httpReq, nil, Identity{Project: "cli"})
	SetCredential(httpReq, ProtocolChat, APIKey(auth))
	return helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0).Do(httpReq)
}

// doSend tries the preferred upstream protocol first and falls back to the other
// OpenCode endpoints when upstream reports that the model is not served there.
// The working protocol is remembered per model so later requests skip the probe.
func (e *Executor) doSend(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, stream bool) (*http.Response, sdktranslator.Format, error) {
	// CLIProxyAPI leaves req.Payload in the client's format; the executor owns the
	// conversion to whatever protocol the upstream model actually serves.
	payloadFormat := opts.SourceFormat
	if req.Format != "" {
		payloadFormat = req.Format
	}
	candidates := e.candidateProtocols(req.Model, payloadFormat)
	var lastErr error
	for index, protocol := range candidates {
		body := req.Payload
		if protocol.Format() != payloadFormat {
			translated := sdktranslator.TranslateRequest(payloadFormat, protocol.Format(), req.Model, req.Payload, stream)
			if len(translated) == 0 {
				log.Warnf("[opencode] request translation %s -> %s unavailable for model=%s", payloadFormat, protocol.Format(), req.Model)
				continue
			}
			body = translated
			log.Infof("[opencode] translated request %s -> %s model=%s", payloadFormat, protocol.Format(), req.Model)
		}
		resp, err := e.do(ctx, auth, req, opts, protocol, body, stream)
		if err == nil {
			if index > 0 {
				log.Infof("[opencode] learned upstream protocol %s for model=%s", protocol, req.Model)
			}
			e.rememberProtocol(req.Model, protocol)
			return resp, protocol.Format(), nil
		}
		lastErr = err
		if !isEndpointUnavailable(err) {
			return nil, protocol.Format(), err
		}
		log.Infof("[opencode] model=%s is not served on %s upstream, trying another endpoint", req.Model, protocol)
	}
	if lastErr == nil {
		lastErr = &upstreamError{code: http.StatusBadGateway, message: "opencode: no usable upstream protocol for model " + req.Model}
	}
	return nil, payloadFormat, lastErr
}

func (e *Executor) candidateProtocols(model string, sourceFormat sdktranslator.Format) []Protocol {
	preferred := ProtocolChat
	if protocol, ok := e.learnedProtocol(model); ok {
		preferred = protocol
	} else if protocol, ok := e.catalog.Protocol(model); ok {
		preferred = protocol
	} else if inbound, ok := ProtocolForFormat(sourceFormat); ok {
		preferred = inbound
	}
	order := []Protocol{preferred}
	for _, candidate := range []Protocol{ProtocolChat, ProtocolResponses, ProtocolAnthropic} {
		if candidate != preferred {
			order = append(order, candidate)
		}
	}
	return order
}

func (e *Executor) learnedProtocol(model string) (Protocol, bool) {
	if value, ok := e.learned.Load(strings.ToLower(strings.TrimSpace(model))); ok {
		if protocol, valid := value.(Protocol); valid {
			return protocol, true
		}
	}
	return "", false
}

func (e *Executor) rememberProtocol(model string, protocol Protocol) {
	key := strings.ToLower(strings.TrimSpace(model))
	if key == "" {
		return
	}
	e.learned.Store(key, protocol)
}

// isEndpointUnavailable recognizes responses that mean "this model is not served
// on this endpoint" rather than a request or credential problem.
func isEndpointUnavailable(err error) bool {
	statusErr, ok := err.(*upstreamError)
	if !ok {
		return false
	}
	if statusErr.StatusCode() != http.StatusNotFound && statusErr.StatusCode() != http.StatusUnprocessableEntity {
		return false
	}
	message := strings.ToLower(statusErr.Error())
	return strings.Contains(message, "endpoint is unavailable") ||
		strings.Contains(message, "not found") ||
		strings.Contains(message, "unsupported")
}

func (e *Executor) do(ctx context.Context, auth *coreauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, protocol Protocol, body []byte, stream bool) (*http.Response, error) {
	key := APIKey(auth)
	if key == "" {
		return nil, &upstreamError{code: http.StatusUnauthorized, message: "opencode: auth is missing api_key"}
	}
	endpoint := e.baseURL + e.goPrefix + protocol.Path()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, &upstreamError{code: http.StatusBadGateway, message: "opencode: build request failed"}
	}
	ApplyHeaders(httpReq, body, identityFor(opts))
	SetCredential(httpReq, protocol, key)

	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := client.Do(httpReq)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &upstreamError{code: http.StatusBadGateway, message: "opencode: upstream request failed"}
	}
	if resp.StatusCode >= 400 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamErrorBodyLimit))
		resp.Body.Close()
		e.noteAccountHealth(auth, resp.StatusCode)
		log.Warnf("[opencode] upstream HTTP %d model=%s protocol=%s", resp.StatusCode, req.Model, protocol)
		return nil, &upstreamError{code: resp.StatusCode, message: upstreamMessage(resp.StatusCode, detail)}
	}
	if stream && !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "event-stream") {
		// Some upstream failures still answer 200 with a JSON document.
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, upstreamErrorBodyLimit))
		resp.Body.Close()
		if detail := extractUpstreamError(peek); detail != "" {
			e.noteAccountHealth(auth, http.StatusBadGateway)
			return nil, &upstreamError{code: http.StatusBadGateway, message: detail}
		}
		return nil, &upstreamError{code: http.StatusBadGateway, message: "opencode: upstream did not return an event stream"}
	}
	return resp, nil
}

func (e *Executor) targetFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if req.Format != "" {
		return req.Format
	}
	return e.RequestToFormat(req, opts)
}

// ensureCatalog blocks until the catalog is loaded for the first time, because auth
// model registration happens during startup and must not race the warm-up. Later
// refreshes happen in the background once the interval elapses.
func (e *Executor) ensureCatalog() {
	if e.catalog.Loaded() {
		e.mu.Lock()
		stale := time.Since(e.lastRefresh) > e.refreshEach
		e.mu.Unlock()
		if stale {
			go e.refreshCatalogAsync()
		}
		return
	}
	e.loadMu.Lock()
	defer e.loadMu.Unlock()
	if e.catalog.Loaded() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	if err := e.RefreshCatalog(ctx); err != nil {
		log.Warnf("[opencode] model catalog initial load failed: %v", err)
	}
}

func (e *Executor) refreshCatalogAsync() {
	ctx, cancel := context.WithTimeout(context.Background(), refreshTimeout)
	defer cancel()
	if err := e.RefreshCatalog(ctx); err != nil {
		log.Warnf("[opencode] model catalog refresh failed: %v", err)
		return
	}
	e.RegisterModels()
	e.registerAuthModels()
}

// registerAuthModels publishes the catalog against each OpenCode auth file found in
// the auth directory. The service performs the same binding in a deferred pass, but
// until that runs the provider is reported as having no auth for every model, so the
// first requests after startup would fail with auth_not_found.
func (e *Executor) registerAuthModels() int {
	if e == nil || e.cfg == nil {
		return 0
	}
	infos := e.modelInfos()
	if len(infos) == 0 {
		return 0
	}
	dir, err := util.ResolveAuthDir(e.cfg.AuthDir)
	if err != nil || strings.TrimSpace(dir) == "" {
		return 0
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	registered := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(strings.ToLower(entry.Name()), ".json") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var probe struct {
			Type     string `json:"type"`
			Provider string `json:"provider"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(probe.Type))
		if kind == "" {
			kind = strings.ToLower(strings.TrimSpace(probe.Provider))
		}
		if kind != ProviderName {
			continue
		}
		registry.GetGlobalRegistry().RegisterClient(entry.Name(), ProviderName, infos)
		registered++
	}
	if registered > 0 {
		log.Infof("[opencode] bound %d models to %d opencode auth file(s)", len(infos), registered)
	}
	return registered
}

// RefreshCatalog reloads the model catalog and publishes models to the registry.
func (e *Executor) RefreshCatalog(ctx context.Context) error {
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, nil, refreshTimeout)
	if err := e.catalog.Refresh(ctx, client, e.catalogURL, e.modelsURL, ""); err != nil {
		return err
	}
	e.mu.Lock()
	e.lastRefresh = time.Now()
	e.mu.Unlock()
	// Model publication stays with the service's per-auth registration path
	// (ModelsForAuth): registering here would bump the registry epoch in the middle
	// of the service's own registration pass and void its fence.
	return nil
}

// RegisterModels publishes the resolved Go-plan models to the global registry so
// /v1/models lists them and routing resolves the "opencode" provider.
func (e *Executor) RegisterModels() []*registry.ModelInfo {
	infos := e.modelInfos()
	if len(infos) == 0 {
		return nil
	}
	registry.GetGlobalRegistry().RegisterClient(registryClientID, ProviderName, infos)
	return infos
}

// ModelsForAuth returns the catalog models to publish for one auth, preferring the
// live executor and falling back to an empty list when nothing is loaded yet.
func ModelsForAuth() []*registry.ModelInfo {
	executor := defaultExecutor.Load()
	if executor == nil {
		return nil
	}
	executor.ensureCatalog()
	return executor.modelInfos()
}

func (e *Executor) modelInfos() []*registry.ModelInfo {
	models := e.catalog.Models()
	if len(models) == 0 {
		return nil
	}
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{
			ID:          model.ID,
			Object:      "model",
			Created:     0,
			OwnedBy:     ProviderName,
			Type:        ProviderName,
			DisplayName: model.ID,
			Name:        model.ID,
		})
	}
	return infos
}

// Quota returns the quota tracker.
func (e *Executor) Quota() *QuotaTracker { return e.quota }

// Catalog exposes the model catalog.
func (e *Executor) Catalog() *Catalog { return e.catalog }

func (e *Executor) noteAccountHealth(auth *coreauth.Auth, status int) {
	if auth == nil {
		return
	}
	switch status {
	case http.StatusUnauthorized, http.StatusPaymentRequired, http.StatusTooManyRequests:
		if os.Getenv(envDisableAccountHealth) == "true" {
			return
		}
		log.Warnf("[opencode] account %s reported HTTP %d", auth.ID, status)
	}
}

// APIKey extracts the Go-plan API key from an auth record.
func APIKey(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	for _, key := range []string{"api_key", "apiKey", "key"} {
		if value, ok := auth.Metadata[key].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	return ""
}

func identityFor(opts cliproxyexecutor.Options) Identity {
	identity := Identity{Project: "cli"}
	if opts.Metadata != nil {
		identity.Session = firstMetadataString(opts.Metadata,
			cliproxyexecutor.CanonicalSessionIDMetadataKey,
			cliproxyexecutor.DerivedSessionIDMetadataKey,
			cliproxyexecutor.ExecutionSessionMetadataKey,
		)
		identity.ParentSession = firstMetadataString(opts.Metadata, cliproxyexecutor.ParentSessionIDMetadataKey)
	}
	if identity.Session == "" && opts.Headers != nil {
		identity.Session = firstHeader(opts.Headers, "X-Session-Id", "x-session-id", "X-Client-Request-Id")
		identity.ParentSession = firstHeader(opts.Headers, "x-parent-session-id")
	}
	if identity.Session != "" {
		identity.Session = SessionIDForSignal(identity.Session)
		identity.Project = identity.Session
	}
	return identity
}

func firstMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func upstreamMessage(status int, body []byte) string {
	if detail := extractUpstreamError(body); detail != "" {
		return detail
	}
	switch status {
	case http.StatusUnauthorized:
		return "opencode: upstream rejected the api key"
	case http.StatusTooManyRequests:
		return "opencode: upstream quota exhausted"
	}
	return fmt.Sprintf("opencode: upstream returned HTTP %d", status)
}

func extractUpstreamError(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	lower := strings.ToLower(trimmed)
	if idx := strings.Index(lower, `"message"`); idx >= 0 {
		if message := jsonString(trimmed[idx:]); message != "" {
			return "opencode: " + message
		}
	}
	if len(trimmed) > 240 {
		trimmed = trimmed[:240]
	}
	return "opencode: " + trimmed
}

func jsonString(fragment string) string {
	start := strings.Index(fragment, ":")
	if start < 0 {
		return ""
	}
	rest := strings.TrimSpace(fragment[start+1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	var builder strings.Builder
	for i := 0; i < len(rest); i++ {
		if rest[i] == '\\' && i+1 < len(rest) {
			i++
			builder.WriteByte(rest[i])
			continue
		}
		if rest[i] == '"' {
			break
		}
		builder.WriteByte(rest[i])
	}
	return builder.String()
}

func parseProtocolOverrides(raw string) map[string]Protocol {
	out := make(map[string]Protocol)
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pieces := strings.SplitN(part, "=", 2)
		if len(pieces) != 2 {
			continue
		}
		model := strings.TrimSpace(pieces[0])
		if _, ok := FormatForProtocol(pieces[1]); !ok || model == "" {
			continue
		}
		out[model] = Protocol(strings.ToLower(strings.TrimSpace(pieces[1])))
	}
	return out
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

// upstreamError carries an HTTP-like status so credential state can react to it.
type upstreamError struct {
	code    int
	message string
}

func (e *upstreamError) Error() string   { return e.message }
func (e *upstreamError) StatusCode() int { return e.code }
