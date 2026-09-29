package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef int (*cliproxy_host_call_fn)(void*, const char*, const uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_host_free_fn)(void*, size_t);

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	cliproxy_host_call_fn call;
	cliproxy_host_free_fn free_buffer;
} cliproxy_host_api;

static cliproxy_host_api stored_host;
static int host_ready;

static void store_host_api(const cliproxy_host_api* host) {
	if (host == NULL) {
		host_ready = 0;
		return;
	}
	stored_host = *host;
	host_ready = 1;
}

static int call_host_api(const char* method, const uint8_t* request, size_t request_len, cliproxy_buffer* response) {
	if (!host_ready || stored_host.call == NULL) {
		return 1;
	}
	return stored_host.call(stored_host.host_ctx, method, request, request_len, response);
}

static void free_host_buffer(void* ptr, size_t len) {
	if (host_ready && stored_host.free_buffer != NULL && ptr != NULL) {
		stored_host.free_buffer(ptr, len);
	}
}

typedef int (*cliproxy_plugin_call_fn)(char*, uint8_t*, size_t, cliproxy_buffer*);
typedef void (*cliproxy_plugin_free_fn)(void*, size_t);
typedef void (*cliproxy_plugin_shutdown_fn)(void);

typedef struct {
	uint32_t abi_version;
	cliproxy_plugin_call_fn call;
	cliproxy_plugin_free_fn free_buffer;
	cliproxy_plugin_shutdown_fn shutdown;
} cliproxy_plugin_api;

extern int cliproxyPluginCall(char*, uint8_t*, size_t, cliproxy_buffer*);
extern void cliproxyPluginFree(void*, size_t);
extern void cliproxyPluginShutdown(void);
*/
import "C"

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/accountpool"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/basispoints"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/intercept"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/management"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
)

const (
	abiVersion    = 1
	schemaVersion = 3
)

var (
	pluginState       = state.New()
	handlerMu         sync.RWMutex
	handler           *intercept.Handler
	managementHandler *management.Handler
	basispointsSvc    *basispoints.Service
	callbackMu        sync.RWMutex
)

type envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *envelopeError  `json:"error,omitempty"`
}

type envelopeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type lifecycleRequest struct {
	ConfigYAML    []byte `json:"config_yaml"`
	SchemaVersion uint32 `json:"schema_version"`
}

type registration struct {
	SchemaVersion uint32                 `json:"schema_version"`
	Metadata      metadata               `json:"metadata"`
	Capabilities  registrationCapability `json:"capabilities"`
}

type metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo"`
	ConfigFields     []configField `json:"ConfigFields"`
}

type configField struct {
	Name        string `json:"Name"`
	Type        string `json:"Type"`
	Description string `json:"Description"`
}

type registrationCapability struct {
	AuthProvider                bool     `json:"auth_provider,omitempty"`
	ModelProvider               bool     `json:"model_provider,omitempty"`
	ModelRouter                 bool     `json:"model_router,omitempty"`
	Executor                    bool     `json:"executor,omitempty"`
	ExecutorModelScope          string   `json:"executor_model_scope,omitempty"`
	ExecutorInputFormats        []string `json:"executor_input_formats,omitempty"`
	ExecutorOutputFormats       []string `json:"executor_output_formats,omitempty"`
	RequestInterceptor          bool     `json:"request_interceptor"`
	RequestLifecyclePlugin      bool     `json:"request_lifecycle_plugin"`
	ResponseInterceptor         bool     `json:"response_interceptor,omitempty"`
	ManagementAPI               bool     `json:"management_api"`
	Scheduler                   bool     `json:"scheduler,omitempty"`
	SchedulerExclusiveProviders []string `json:"scheduler_exclusive_providers,omitempty"`
	SchedulerAcrossPriorities   bool     `json:"scheduler_across_priorities,omitempty"`
}

type managementRegistrationRequest struct {
	BasePath         string `json:"BasePath"`
	ResourceBasePath string `json:"ResourceBasePath"`
}

type managementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

type managementResponse struct {
	StatusCode int         `json:"StatusCode"`
	Headers    http.Header `json:"Headers"`
	Body       []byte      `json:"Body"`
}

type mergedManagementRegistration struct {
	Routes    []map[string]string `json:"routes,omitempty"`
	Resources []map[string]string `json:"resources,omitempty"`
}

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(host *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if host == nil || host.abi_version != C.uint32_t(abiVersion) || host.call == nil || host.free_buffer == nil || plugin == nil {
		return 1
	}
	callbackMu.Lock()
	C.store_host_api(host)
	callbackMu.Unlock()
	basispointsSvc = basispoints.NewService()
	basispointsSvc.SetHost(callHost)
	basispointsSvc.SetState(pluginState)
	plugin.abi_version = C.uint32_t(abiVersion)
	plugin.call = C.cliproxy_plugin_call_fn(C.cliproxyPluginCall)
	plugin.free_buffer = C.cliproxy_plugin_free_fn(C.cliproxyPluginFree)
	plugin.shutdown = C.cliproxy_plugin_shutdown_fn(C.cliproxyPluginShutdown)
	return 0
}

//export cliproxyPluginCall
func cliproxyPluginCall(method *C.char, request *C.uint8_t, requestLen C.size_t, response *C.cliproxy_buffer) C.int {
	if response != nil {
		response.ptr = nil
		response.len = 0
	}
	if method == nil {
		writeResponse(response, errorEnvelope("invalid_method", "method is required"))
		return 1
	}
	var requestBytes []byte
	if request != nil && requestLen > 0 {
		requestBytes = C.GoBytes(unsafe.Pointer(request), C.int(requestLen))
	}
	result, errHandle := handleMethod(C.GoString(method), requestBytes)
	if errHandle != nil {
		writeResponse(response, errorEnvelope("plugin_error", errHandle.Error()))
		return 1
	}
	writeResponse(response, result)
	return 0
}

//export cliproxyPluginFree
func cliproxyPluginFree(ptr unsafe.Pointer, length C.size_t) {
	if ptr != nil {
		C.free(ptr)
	}
	_ = length
}

//export cliproxyPluginShutdown
func cliproxyPluginShutdown() {
	if basispointsSvc != nil {
		_, _ = basispointsSvc.Handle("plugin.shutdown", nil)
	}
	basispointsSvc = nil
	handlerMu.Lock()
	handler = nil
	managementHandler = nil
	handlerMu.Unlock()
	_ = pluginState.Shutdown()
	callbackMu.Lock()
	C.store_host_api(nil)
	callbackMu.Unlock()
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if errConfigure := configure(raw); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case "plugin.quiesce":
		if basispointsSvc != nil {
			if _, errQuiesce := basispointsSvc.Handle(method, raw); errQuiesce != nil {
				return nil, errQuiesce
			}
		}
		return okEnvelope(struct{}{})
	case "plugin.shutdown":
		if basispointsSvc != nil {
			if _, errShutdown := basispointsSvc.Handle(method, raw); errShutdown != nil {
				return nil, errShutdown
			}
		}
		if errShutdown := pluginState.Shutdown(); errShutdown != nil {
			return nil, errShutdown
		}
		return okEnvelope(struct{}{})
	case "auth.identifier", "auth.parse", "auth.login.start", "auth.login.poll", "auth.refresh",
		"executor.identifier", "executor.execute", "executor.execute_stream", "executor.count_tokens", "executor.http_request",
		"model.register", "model.static", "model.for_auth", "model.route", "response.intercept_after":
		return handleBasispointsMethod(method, raw)
	case "management.register":
		return managementRegistration(raw)
	case "management.handle":
		return handleManagement(raw)
	case "scheduler.pick":
		return schedulerPick(raw)
	case "request.intercept_before":
		return interceptRequest(raw, false)
	case "request.intercept_after":
		return interceptRequest(raw, true)
	case "request.complete":
		return complete(raw)
	default:
		return errorEnvelope("unknown_method", "unknown method: "+method), nil
	}
}

func managementRegistration(raw []byte) ([]byte, error) {
	var request managementRegistrationRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management registration request: %w", errUnmarshal)
		}
	}
	baseRaw, errMarshal := json.Marshal(management.Routes(request.BasePath, request.ResourceBasePath))
	if errMarshal != nil {
		return nil, errMarshal
	}
	var merged mergedManagementRegistration
	if errUnmarshal := json.Unmarshal(baseRaw, &merged); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	ensureBasispointsService()
	bpsRaw, errBasispoints := basispointsSvc.Handle("management.register", raw)
	if errBasispoints != nil {
		return nil, errBasispoints
	}
	bpsJSON, errMarshal := json.Marshal(bpsRaw)
	if errMarshal != nil {
		return nil, errMarshal
	}
	var bpsRegistration mergedManagementRegistration
	if errUnmarshal := json.Unmarshal(bpsJSON, &bpsRegistration); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	merged.Routes = append(merged.Routes, bpsRegistration.Routes...)
	merged.Resources = append(merged.Resources, bpsRegistration.Resources...)
	return okEnvelope(merged)
}

func handleBasispointsMethod(method string, raw []byte) ([]byte, error) {
	ensureBasispointsService()
	result, errHandle := basispointsSvc.Handle(method, raw)
	if errHandle != nil {
		return nil, errHandle
	}
	return okEnvelope(result)
}

func configure(raw []byte) error {
	ensureBasispointsService()
	if basispointsSvc != nil {
		if _, errConfigure := basispointsSvc.Handle("plugin.register", raw); errConfigure != nil {
			return fmt.Errorf("configure basispoints module: %w", errConfigure)
		}
	}
	var request lifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return fmt.Errorf("decode lifecycle request: %w", errUnmarshal)
		}
	}
	if request.SchemaVersion < 2 {
		return fmt.Errorf("request lifecycle plugin requires host schema version 2 or newer")
	}
	workingDir, errWorkingDir := os.Getwd()
	if errWorkingDir != nil {
		return fmt.Errorf("resolve plugin working directory: %w", errWorkingDir)
	}
	cfg, errConfig := config.ParseYAML(request.ConfigYAML, workingDir)
	if errConfig != nil {
		return errConfig
	}
	if errConfigure := pluginState.Configure(context.Background(), cfg); errConfigure != nil {
		return errConfigure
	}
	handlerMu.Lock()
	handler = intercept.New(pluginState, cfg)
	managementHandler = management.New(pluginState, cfg)
	handlerMu.Unlock()
	return nil
}

func ensureBasispointsService() {
	if basispointsSvc != nil {
		return
	}
	basispointsSvc = basispoints.NewService()
	basispointsSvc.SetHost(callHost)
	basispointsSvc.SetState(pluginState)
}

func pluginRegistration() registration {
	version := "0.3.0"
	capabilities := registrationCapability{
		AuthProvider:           true,
		ModelProvider:          true,
		ModelRouter:            true,
		Executor:               true,
		ExecutorModelScope:     "oauth",
		ExecutorInputFormats:   []string{"openai-response", "codex"},
		ExecutorOutputFormats:  []string{"openai-response", "codex"},
		RequestInterceptor:     true,
		RequestLifecyclePlugin: true,
		ResponseInterceptor:    true,
		ManagementAPI:          true,
	}
	providers, across := schedulerCapabilityFor(pluginState.Config())
	if len(providers) > 0 {
		capabilities.Scheduler = true
		capabilities.SchedulerExclusiveProviders = providers
		capabilities.SchedulerAcrossPriorities = across
	}
	return registration{
		SchemaVersion: schemaVersion,
		Metadata: metadata{
			Name:             "enterprise-access-audit",
			Version:          version,
			Author:           "router-for-me",
			GitHubRepository: "https://github.com/router-for-me/CLIProxyAPI",
			Logo:             "https://raw.githubusercontent.com/router-for-me/CLIProxyAPI/main/docs/logo.png",
			ConfigFields: []configField{
				{Name: "data_dir", Type: "string", Description: "Plugin data directory; defaults below the CPA working directory."},
				{Name: "database_path", Type: "string", Description: "SQLite database path; defaults to enterprise-access-audit.sqlite in data_dir."},
				{Name: "retention_days", Type: "integer", Description: "Audit retention period in days (1-3650)."},
				{Name: "audit_enabled", Type: "boolean", Description: "Global request-audit switch; disabled by default. Access-deny policies remain enforced when disabled."},
				{Name: "default_audit_enabled", Type: "boolean", Description: "Default audit state for an absent policy."},
				{Name: "max_text_bytes", Type: "integer", Description: "Maximum persisted user-text bytes (1-1048576)."},
				{Name: "cleanup_interval_seconds", Type: "integer", Description: "Automatic cleanup interval in seconds."},
				{Name: "account_pool.enabled", Type: "boolean", Description: "Enable account-pool Codex scheduling and per-account concurrency limits; when disabled, a retained exclusive claim delegates to CPA's builtin scheduler."},
				{Name: "account_pool.data_dir", Type: "string", Description: "Account-pool state directory; defaults to <data_dir>/account-pool."},
				{Name: "account_pool.reserve_seconds", Type: "integer", Description: "Scheduler reservation seconds guarding against pick bursts (default 10)."},
				{Name: "account_pool.window_seconds", Type: "integer", Description: "Default rolling admission window seconds (default 15)."},
				{Name: "account_pool.max_wait_seconds", Type: "integer", Description: "Maximum admission wait before a retryable busy rejection (default 30)."},
				{Name: "upstream_transport", Type: "string", Description: "Basis Points transport: auto or http."},
				{Name: "ws_handshake_timeout_seconds", Type: "integer", Description: "Basis Points WebSocket handshake timeout."},
				{Name: "responses_url", Type: "string", Description: "Basis Points Responses endpoint."},
				{Name: "upstream_model", Type: "string", Description: "Default upstream model for Basis Points."},
				{Name: "models", Type: "array", Description: "Logical model names also served by the Basis Points provider."},
				{Name: "model_mappings", Type: "object", Description: "Logical model to Basis Points upstream model mappings."},
				{Name: "timeout_seconds", Type: "integer", Description: "Basis Points upstream timeout."},
				{Name: "max_response_bytes", Type: "integer", Description: "Maximum Basis Points response size."},
				{Name: "auth_mode", Type: "string", Description: "Basis Points authentication mode."},
				{Name: "tools_version_id", Type: "string", Description: "Optional Basis Points tools catalog version."},
			},
		},
		Capabilities: capabilities,
	}
}

// schedulerCapabilityFor decides scheduler registration from plugin config.
// Keep an explicit host-side claim advertised while the pool is disabled so
// the plugin can delegate picks back to CPA's builtin scheduler safely.
func schedulerCapabilityFor(cfg config.Config) (providers []string, acrossPriorities bool) {
	providers = normalizeRegistrationProviders(cfg.ExclusiveSchedulerProviders)
	if len(providers) == 0 && cfg.AccountPool.Enabled {
		providers = []string{accountpool.ExclusiveProvider}
	}
	if len(providers) == 0 {
		return nil, false
	}
	// Layered api-key/OAuth scheduling needs the full candidate set across
	// priority tiers; without it the host pre-filters to one tier.
	return providers, true
}

// normalizeRegistrationProviders lowercases, trims, and dedupes the exclusive
// provider claim list reported by the host config.
func normalizeRegistrationProviders(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		provider := strings.ToLower(strings.TrimSpace(value))
		if provider == "" {
			continue
		}
		if _, exists := seen[provider]; exists {
			continue
		}
		seen[provider] = struct{}{}
		out = append(out, provider)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func schedulerPick(raw []byte) ([]byte, error) {
	var request accountpool.SchedulerPickRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode scheduler pick request: %w", errUnmarshal)
		}
	}
	svc := pluginState.AccountPool()
	if svc == nil {
		return okEnvelope(accountpool.SchedulerPickUnavailable())
	}
	return okEnvelope(svc.Pick(request))
}

func interceptRequest(raw []byte, afterAuth bool) ([]byte, error) {
	var request intercept.Request
	if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
		return nil, fmt.Errorf("decode intercept request: %w", errUnmarshal)
	}
	handlerMu.RLock()
	active := handler
	handlerMu.RUnlock()
	if active == nil {
		return nil, state.ErrUnavailable
	}
	if afterAuth {
		if rejected := accountPoolAdmit(request); rejected != nil {
			return okEnvelope(*rejected)
		}
	}
	result, errIntercept := active.Intercept(context.Background(), request)
	if errIntercept != nil {
		return nil, errIntercept
	}
	if !result.Terminate && basispointsSvc != nil {
		method := "request.intercept_after"
		if !afterAuth {
			method = "request.intercept_before"
		}
		bpsRaw, errBasispoints := basispointsSvc.Handle(method, raw)
		if errBasispoints != nil {
			return nil, errBasispoints
		}
		var bpsResult intercept.Response
		encoded, errMarshal := json.Marshal(bpsRaw)
		if errMarshal != nil {
			return nil, errMarshal
		}
		if errUnmarshal := json.Unmarshal(encoded, &bpsResult); errUnmarshal != nil {
			return nil, fmt.Errorf("decode basispoints interceptor response: %w", errUnmarshal)
		}
		result = mergeInterceptResponses(result, bpsResult)
	}
	return okEnvelope(result)
}

func mergeInterceptResponses(base, extra intercept.Response) intercept.Response {
	if base.Headers == nil {
		base.Headers = make(http.Header)
	}
	for key, values := range extra.Headers {
		base.Headers[key] = append([]string(nil), values...)
	}
	base.ClearHeaders = append(base.ClearHeaders, extra.ClearHeaders...)
	if extra.Terminate {
		base.Terminate = true
		base.StatusCode = extra.StatusCode
		base.ResponseHeaders = extra.ResponseHeaders
		base.ResponseBody = extra.ResponseBody
	}
	if len(extra.Body) > 0 {
		base.Body = extra.Body
	}
	if base.ExecutorProvider == "" {
		base.ExecutorProvider = strings.TrimSpace(extra.ExecutorProvider)
	}
	return base
}

// admitDebug appends one admission diagnostic line when ACCOUNTPOOL_DEBUG_FILE
// is set (the smoke harness wires it); it is a no-op otherwise.
func admitDebug(format string, args ...any) {
	path := strings.TrimSpace(os.Getenv("ACCOUNTPOOL_DEBUG_FILE"))
	if path == "" {
		return
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	_, _ = fmt.Fprintf(file, "[%s] %s\n", time.Now().Format("15:04:05.000"), fmt.Sprintf(format, args...))
}

// accountPoolAdmit gates a selected request at the after-auth stage.
func accountPoolAdmit(request intercept.Request) *intercept.Response {
	svc := pluginState.AccountPool()
	authID, _ := request.Metadata["selected_auth_id"].(string)
	callerHash, _ := request.Metadata["quota_key_hash"].(string)
	admitDebug("enter req=%q auth=%q hash=%q svc_nil=%v", request.RequestID, authID, callerHash, svc == nil)
	if svc == nil {
		return nil
	}
	result := svc.AdmitIntercept(request.RequestID, request.Headers, request.Metadata)
	if result == nil {
		admitDebug("pass req=%q", request.RequestID)
		return nil
	}
	admitDebug("reject req=%q status=%d body=%s", request.RequestID, result.StatusCode, result.Body)
	status := result.StatusCode
	if status <= 0 {
		status = http.StatusServiceUnavailable
	}
	return &intercept.Response{
		Terminate:       true,
		StatusCode:      status,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    result.Body,
	}
}

func handleManagement(raw []byte) ([]byte, error) {
	var request managementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	if basispointsManagementPath(request.Path) && basispointsSvc != nil {
		result, errBasispoints := basispointsSvc.Handle("management.handle", raw)
		if errBasispoints != nil {
			return nil, errBasispoints
		}
		return okEnvelope(result)
	}
	handlerMu.RLock()
	active := managementHandler
	handlerMu.RUnlock()
	if active == nil {
		return okEnvelope(managementResponse{StatusCode: 503, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"error":{"code":"storage_unavailable","message":"plugin storage is unavailable"}}`)})
	}
	if accountPoolManagementPath(request.Path) {
		svc := pluginState.AccountPool()
		if svc == nil {
			return okEnvelope(managementResponse{StatusCode: 503, Headers: http.Header{"Content-Type": []string{"application/json"}}, Body: []byte(`{"error":{"code":"storage_unavailable","message":"account pool service is unavailable"}}`)})
		}
		response := svc.HandleManagement(accountpool.ManagementRequest{Method: request.Method, Path: request.Path, Headers: request.Headers, Query: request.Query, Body: request.Body})
		return okEnvelope(managementResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body})
	}
	response := active.Handle(context.Background(), management.ManagementRequest{Method: request.Method, Path: request.Path, Headers: request.Headers, Query: request.Query, Body: request.Body})
	return okEnvelope(managementResponse{StatusCode: response.StatusCode, Headers: response.Headers, Body: response.Body})
}

// accountPoolManagementPath reports whether a management path targets account-pool routes.
func accountPoolManagementPath(path string) bool {
	normalized := strings.TrimRight(strings.TrimSpace(path), "/")
	return strings.HasSuffix(normalized, accountpool.Prefix) || strings.Contains(normalized, accountpool.Prefix+"/")
}

func basispointsManagementPath(path string) bool {
	normalized := strings.TrimRight(strings.TrimSpace(path), "/")
	return strings.Contains(normalized, "/enterprise-access-audit/basispoints/") ||
		strings.HasSuffix(normalized, "/enterprise-access-audit/basispoints") ||
		strings.HasSuffix(normalized, "/basispoints/source-auths")
}

func complete(raw []byte) ([]byte, error) {
	var request intercept.Completion
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
			return nil, fmt.Errorf("decode completion request: %w", errUnmarshal)
		}
	}
	handlerMu.RLock()
	active := handler
	handlerMu.RUnlock()
	if active == nil {
		return nil, state.ErrUnavailable
	}
	if basispointsSvc != nil {
		if _, errComplete := basispointsSvc.Handle("request.complete", raw); errComplete != nil {
			return nil, errComplete
		}
	}
	if svc := pluginState.AccountPool(); svc != nil {
		svc.Complete(request.RequestID)
	}
	if errComplete := active.Complete(context.Background(), request); errComplete != nil {
		return nil, errComplete
	}
	return okEnvelope(struct{}{})
}

func okEnvelope(value any) ([]byte, error) {
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		return nil, fmt.Errorf("encode plugin result: %w", errMarshal)
	}
	return json.Marshal(envelope{OK: true, Result: raw})
}

func errorEnvelope(code, message string) []byte {
	raw, errMarshal := json.Marshal(envelope{OK: false, Error: &envelopeError{Code: code, Message: message}})
	if errMarshal != nil {
		return []byte(`{"ok":false,"error":{"code":"plugin_error","message":"encode error"}}`)
	}
	return raw
}

func callHost(method string, payload any, out any) error {
	if strings.TrimSpace(method) == "" {
		return fmt.Errorf("host callback method is required")
	}
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return fmt.Errorf("encode host callback %s: %w", method, errMarshal)
	}
	if len(rawPayload) > math.MaxInt32 {
		return fmt.Errorf("host callback %s request is too large", method)
	}
	cMethod := C.CString(method)
	defer C.free(unsafe.Pointer(cMethod))
	cPayload := C.CBytes(rawPayload)
	defer C.free(cPayload)
	callbackMu.RLock()
	var response C.cliproxy_buffer
	callCode := C.call_host_api(cMethod, (*C.uint8_t)(cPayload), C.size_t(len(rawPayload)), &response)
	if response.ptr == nil || response.len > C.size_t(math.MaxInt32) {
		if response.ptr != nil {
			C.free_host_buffer(response.ptr, response.len)
		}
		callbackMu.RUnlock()
		return fmt.Errorf("host callback %s returned invalid buffer, code=%d", method, int(callCode))
	}
	rawResponse := C.GoBytes(response.ptr, C.int(response.len))
	C.free_host_buffer(response.ptr, response.len)
	callbackMu.RUnlock()
	if callCode != 0 {
		return fmt.Errorf("host callback %s returned code=%d", method, int(callCode))
	}
	var responseEnvelope envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &responseEnvelope); errUnmarshal != nil {
		return fmt.Errorf("decode host callback %s: %w", method, errUnmarshal)
	}
	if !responseEnvelope.OK {
		if responseEnvelope.Error != nil {
			return fmt.Errorf("host callback %s failed: %s", method, responseEnvelope.Error.Message)
		}
		return fmt.Errorf("host callback %s failed", method)
	}
	if out != nil && len(responseEnvelope.Result) > 0 {
		if errUnmarshal := json.Unmarshal(responseEnvelope.Result, out); errUnmarshal != nil {
			return fmt.Errorf("decode host callback %s result: %w", method, errUnmarshal)
		}
	}
	return nil
}

func writeResponse(response *C.cliproxy_buffer, raw []byte) {
	if response == nil || len(raw) == 0 {
		return
	}
	ptr := C.CBytes(raw)
	if ptr == nil {
		return
	}
	response.ptr = ptr
	response.len = C.size_t(len(raw))
}
