package main

/*
#include <stdint.h>
#include <stdlib.h>

typedef struct {
	void* ptr;
	size_t len;
} cliproxy_buffer;

typedef struct {
	uint32_t abi_version;
	void* host_ctx;
	void* call;
	void* free_buffer;
} cliproxy_host_api;

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
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"unsafe"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/accountpool"
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
	RequestInterceptor          bool     `json:"request_interceptor"`
	RequestLifecyclePlugin      bool     `json:"request_lifecycle_plugin"`
	ManagementAPI               bool     `json:"management_api"`
	Scheduler                   bool     `json:"scheduler,omitempty"`
	SchedulerExclusiveProviders []string `json:"scheduler_exclusive_providers,omitempty"`
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

func main() {}

//export cliproxy_plugin_init
func cliproxy_plugin_init(_ *C.cliproxy_host_api, plugin *C.cliproxy_plugin_api) C.int {
	if plugin == nil {
		return 1
	}
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
	handlerMu.Lock()
	handler = nil
	managementHandler = nil
	handlerMu.Unlock()
	_ = pluginState.Shutdown()
}

func handleMethod(method string, raw []byte) ([]byte, error) {
	switch method {
	case "plugin.register", "plugin.reconfigure":
		if errConfigure := configure(raw); errConfigure != nil {
			return nil, errConfigure
		}
		return okEnvelope(pluginRegistration())
	case "plugin.shutdown":
		if errShutdown := pluginState.Shutdown(); errShutdown != nil {
			return nil, errShutdown
		}
		return okEnvelope(struct{}{})
	case "management.register":
		var request managementRegistrationRequest
		if len(raw) > 0 {
			if errUnmarshal := json.Unmarshal(raw, &request); errUnmarshal != nil {
				return nil, fmt.Errorf("decode management registration request: %w", errUnmarshal)
			}
		}
		return okEnvelope(management.Routes(request.BasePath, request.ResourceBasePath))
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

func configure(raw []byte) error {
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

func pluginRegistration() registration {
	version := "0.2.0"
	capabilities := registrationCapability{RequestInterceptor: true, RequestLifecyclePlugin: true, ManagementAPI: true}
	if pluginState.Config().AccountPool.Enabled {
		capabilities.Scheduler = true
		capabilities.SchedulerExclusiveProviders = []string{accountpool.ExclusiveProvider}
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
				{Name: "account_pool.enabled", Type: "boolean", Description: "Enable account-pool Codex scheduling and admission (registers the scheduler capability)."},
				{Name: "account_pool.data_dir", Type: "string", Description: "Account-pool state directory; defaults to <data_dir>/account-pool."},
				{Name: "account_pool.reserve_seconds", Type: "integer", Description: "Scheduler reservation seconds guarding against pick bursts (default 10)."},
				{Name: "account_pool.window_seconds", Type: "integer", Description: "Default rolling admission window seconds (default 15)."},
				{Name: "account_pool.max_wait_seconds", Type: "integer", Description: "Maximum admission wait before a retryable busy rejection (default 30)."},
			},
		},
		Capabilities: capabilities,
	}
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
	return okEnvelope(result)
}

// accountPoolAdmit gates a selected request when account-pool scheduling is active.
func accountPoolAdmit(request intercept.Request) *intercept.Response {
	svc := pluginState.AccountPool()
	if svc == nil {
		return nil
	}
	result := svc.AdmitIntercept(request.RequestID, request.Metadata)
	if result == nil {
		return nil
	}
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
