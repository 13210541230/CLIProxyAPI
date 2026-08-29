package management

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/model"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

const (
	Prefix = "/enterprise-access-audit"

	PoliciesPath      = Prefix + "/policies"
	BatchPoliciesPath = Prefix + "/policies/batch"
	PolicyPath        = Prefix + "/policy"
	AuditPath         = Prefix + "/audit"
	AuditDetailPath   = Prefix + "/audit/detail"
	SettingsPath      = Prefix + "/settings"
	ResourceUIPath    = "/ui"

	defaultPageSize = 50
	maxPageSize     = 100
	maxPage         = 100000
	maxHashes       = 1000
	maxModels       = 256
	maxModelBytes   = 256
	maxBodyBytes    = 1024 * 1024
)

//go:embed ui/index.html
var resourceUIHTML []byte

// ManagementRequest is the private JSON-equivalent of pluginapi.ManagementRequest.
type ManagementRequest struct {
	Method  string
	Path    string
	Headers http.Header
	Query   url.Values
	Body    []byte
}

// ManagementResponse is the private JSON-equivalent of pluginapi.ManagementResponse.
type ManagementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

// Route describes one fixed Management API route for the plugin ABI.
type Route struct {
	Method string
	Path   string
}

// Registration is the fixed route list returned by management.register.
type Resource struct {
	Path        string `json:"path"`
	Menu        string `json:"menu"`
	Description string `json:"description"`
}

type Registration struct {
	Routes    []Route    `json:"routes,omitempty"`
	Resources []Resource `json:"resources,omitempty"`
}

// Handler owns the plugin's authenticated Management API operations.
type Handler struct {
	state *state.Manager
	cfg   config.Config
}

// New creates a management handler backed by the lifecycle-safe plugin state.
func New(manager *state.Manager, cfg config.Config) *Handler {
	return &Handler{state: manager, cfg: cfg}
}

// Routes returns only literal routes; identifiers are carried in query or JSON bodies.
func Routes(basePath, resourceBasePath string) Registration {
	basePath = strings.TrimRight(strings.TrimSpace(basePath), "/")
	if basePath == "" {
		basePath = "/v0/management"
	}
	resourceBasePath = strings.TrimRight(strings.TrimSpace(resourceBasePath), "/")
	resourcePath := ResourceUIPath
	if resourceBasePath != "" {
		resourcePath = resourceBasePath + ResourceUIPath
	}
	return Registration{
		Routes: []Route{
			{Method: http.MethodGet, Path: basePath + PoliciesPath},
			{Method: http.MethodPut, Path: basePath + BatchPoliciesPath},
			{Method: http.MethodPut, Path: basePath + PolicyPath},
			{Method: http.MethodGet, Path: basePath + AuditPath},
			{Method: http.MethodGet, Path: basePath + AuditDetailPath},
			{Method: http.MethodGet, Path: basePath + SettingsPath},
			{Method: http.MethodPut, Path: basePath + SettingsPath},
		},
		Resources: []Resource{{
			Path:        resourcePath,
			Menu:        "Enterprise Access Audit",
			Description: "Review enterprise request audit records and manage access policies.",
		}},
	}
}

// Handle implements fixed-path dispatch and returns stable JSON errors for all client failures.
func (h *Handler) Handle(ctx context.Context, req ManagementRequest) ManagementResponse {
	path := normalizePath(req.Path)
	method := strings.ToUpper(strings.TrimSpace(req.Method))
	if !isKnownPath(path) {
		return errorResponse(http.StatusNotFound, "not_found", "management route was not found")
	}
	if !methodAllowed(path, method) {
		headers := jsonHeaders(true)
		headers.Set("Allow", allowedMethods(path))
		return ManagementResponse{StatusCode: http.StatusMethodNotAllowed, Headers: headers, Body: errorBody("method_not_allowed", "method is not allowed")}
	}
	if h == nil || h.state == nil {
		return errorResponse(http.StatusServiceUnavailable, "storage_unavailable", "plugin storage is unavailable")
	}
	switch {
	case path == ResourceUIPath:
		return h.resourceUI()
	case path == PoliciesPath:
		return h.listPolicies(ctx, req.Query)
	case path == BatchPoliciesPath:
		return h.batchPolicies(ctx, req.Body)
	case path == PolicyPath:
		return h.updatePolicy(ctx, req.Body)
	case path == AuditPath:
		return h.listAudit(ctx, req.Query)
	case path == AuditDetailPath:
		return h.auditDetail(ctx, req.Query)
	case path == SettingsPath && method == http.MethodGet:
		return h.getSettings(ctx)
	case path == SettingsPath:
		return h.updateSettings(ctx, req.Body)
	default:
		return errorResponse(http.StatusNotFound, "not_found", "management route was not found")
	}
}

func normalizePath(path string) string {
	path = strings.TrimSpace(path)
	if strings.HasPrefix(path, "/v0/management/") {
		path = strings.TrimPrefix(path, "/v0/management")
	} else if strings.HasPrefix(path, "/v0/resource/plugins/enterprise-access-audit/") {
		path = strings.TrimPrefix(path, "/v0/resource/plugins/enterprise-access-audit")
	}
	return strings.TrimRight(path, "/")
}

func isKnownPath(path string) bool {
	switch path {
	case ResourceUIPath, PoliciesPath, BatchPoliciesPath, PolicyPath, AuditPath, AuditDetailPath, SettingsPath:
		return true
	default:
		return false
	}
}

func methodAllowed(path, method string) bool {
	if path == ResourceUIPath || path == PoliciesPath || path == AuditPath || path == AuditDetailPath {
		return method == http.MethodGet
	}
	return method == http.MethodPut || (path == SettingsPath && method == http.MethodGet)
}

func allowedMethods(path string) string {
	if path == ResourceUIPath {
		return http.MethodGet
	}
	if path == SettingsPath {
		return http.MethodGet + ", " + http.MethodPut
	}
	if path == PoliciesPath || path == AuditPath || path == AuditDetailPath {
		return http.MethodGet
	}
	return http.MethodPut
}

type policyResponse struct {
	KeyHash      string   `json:"key_hash"`
	DeniedModels []string `json:"denied_models"`
	AuditEnabled bool     `json:"audit_enabled"`
	UpdatedAt    int64    `json:"updated_at"`
}

func (h *Handler) resourceUI() ManagementResponse {
	return ManagementResponse{
		StatusCode: http.StatusOK,
		Headers:    http.Header{"Content-Type": []string{"text/html; charset=utf-8"}, "Cache-Control": []string{"no-store"}},
		Body:       append([]byte(nil), resourceUIHTML...),
	}
}

func (h *Handler) listPolicies(ctx context.Context, query url.Values) ManagementResponse {
	hashes, errHashes := queryHashes(query, "key_hash")
	if errHashes != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errHashes.Error())
	}
	var result []model.Policy
	errStore := h.state.WithStore(ctx, func(active *store.Store) error {
		settings, err := active.GetSettings(ctx, store.SettingsFromConfig(h.cfg))
		if err != nil {
			return err
		}
		result, err = active.ListPoliciesWithDefault(ctx, hashes, settings.DefaultAuditEnabled)
		return err
	})
	if errStore != nil {
		return storageResponse(errStore)
	}
	rows := make([]policyResponse, 0, len(result))
	for _, policy := range result {
		denied := append([]string(nil), policy.DeniedModels...)
		if denied == nil {
			denied = []string{}
		}
		rows = append(rows, policyResponse{KeyHash: policy.KeyHash, DeniedModels: denied, AuditEnabled: policy.AuditEnabled, UpdatedAt: policy.UpdatedAt})
	}
	return jsonResponse(http.StatusOK, map[string]any{"policies": rows})
}

type batchPolicyRequest struct {
	KeyHashes    []string        `json:"key_hashes"`
	DeniedModels json.RawMessage `json:"denied_models"`
	AuditEnabled json.RawMessage `json:"audit_enabled"`
}

type singlePolicyRequest struct {
	KeyHash      string          `json:"key_hash"`
	DeniedModels json.RawMessage `json:"denied_models"`
	AuditEnabled json.RawMessage `json:"audit_enabled"`
}

func (h *Handler) batchPolicies(ctx context.Context, body []byte) ManagementResponse {
	var request batchPolicyRequest
	if err := decodeBody(body, &request); err != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", err.Error())
	}
	if len(request.KeyHashes) == 0 || len(request.KeyHashes) > maxHashes {
		return errorResponse(http.StatusBadRequest, "invalid_input", "key_hashes must contain between 1 and 1000 entries")
	}
	hashes, errHashes := normalizeUniqueHashes(request.KeyHashes)
	if errHashes != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errHashes.Error())
	}
	if len(request.DeniedModels) == 0 || string(request.DeniedModels) == "null" {
		return errorResponse(http.StatusBadRequest, "invalid_input", "denied_models must be an array")
	}
	models, errModels := decodeModels(request.DeniedModels)
	if errModels != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errModels.Error())
	}
	audit, errAudit := parseOptionalBool(request.AuditEnabled, true)
	if errAudit != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errAudit.Error())
	}
	patches := make([]model.PolicyPatch, 0, len(hashes))
	for _, hash := range hashes {
		patches = append(patches, model.PolicyPatch{KeyHash: hash, DeniedModels: &models, AuditEnabled: audit})
	}
	if errStore := h.state.WithStore(ctx, func(active *store.Store) error { return active.ReplacePolicies(ctx, patches) }); errStore != nil {
		return storageResponse(errStore)
	}
	return jsonResponse(http.StatusOK, map[string]any{"updated": hashes})
}

func (h *Handler) updatePolicy(ctx context.Context, body []byte) ManagementResponse {
	var request singlePolicyRequest
	if err := decodeBody(body, &request); err != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", err.Error())
	}
	hash, errHash := model.NormalizeKeyHash(request.KeyHash)
	if errHash != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errHash.Error())
	}
	if len(request.DeniedModels) == 0 && len(request.AuditEnabled) == 0 {
		return errorResponse(http.StatusBadRequest, "invalid_input", "at least one policy field is required")
	}
	var denied *[]string
	if len(request.DeniedModels) > 0 {
		if string(request.DeniedModels) == "null" {
			empty := []string{}
			denied = &empty
		} else {
			models, errModels := decodeModels(request.DeniedModels)
			if errModels != nil {
				return errorResponse(http.StatusBadRequest, "invalid_input", errModels.Error())
			}
			denied = &models
		}
	}
	audit, errAudit := parseOptionalBool(request.AuditEnabled, true)
	if errAudit != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errAudit.Error())
	}
	patch := model.PolicyPatch{KeyHash: hash, DeniedModels: denied, AuditEnabled: audit}
	if errStore := h.state.WithStore(ctx, func(active *store.Store) error { return active.ReplacePolicies(ctx, []model.PolicyPatch{patch}) }); errStore != nil {
		return storageResponse(errStore)
	}
	return jsonResponse(http.StatusOK, map[string]any{"updated": hash})
}

func (h *Handler) listAudit(ctx context.Context, query url.Values) ManagementResponse {
	filter, page, pageSize, errFilter := parseAuditQuery(query)
	if errFilter != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", errFilter.Error())
	}
	var result store.AuditPage
	errStore := h.state.WithStore(ctx, func(active *store.Store) error {
		var err error
		result, err = active.ListAudit(ctx, filter, page, pageSize)
		return err
	})
	if errStore != nil {
		return storageResponse(errStore)
	}
	settings := h.cfg.MaxTextBytes
	_ = h.state.WithStore(ctx, func(active *store.Store) error {
		current, err := active.GetSettings(ctx, store.SettingsFromConfig(h.cfg))
		if err == nil {
			settings = current.MaxTextBytes
		}
		return nil
	})
	rows := make([]auditResponse, 0, len(result.Records))
	for _, record := range result.Records {
		rows = append(rows, auditJSON(record, settings))
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"records":    rows,
		"pagination": map[string]any{"page": result.Page, "page_size": result.PageSize, "total": result.Total, "has_next": result.HasNext},
	})
}

func (h *Handler) auditDetail(ctx context.Context, query url.Values) ManagementResponse {
	value := strings.TrimSpace(query.Get("id"))
	id, errParse := strconv.ParseInt(value, 10, 64)
	if errParse != nil || id < 1 {
		return errorResponse(http.StatusBadRequest, "invalid_input", "id must be a positive integer")
	}
	var record store.AuditRecord
	errStore := h.state.WithStore(ctx, func(active *store.Store) error {
		var err error
		record, err = active.GetAuditByID(ctx, id)
		return err
	})
	if errors.Is(errStore, sql.ErrNoRows) {
		return errorResponse(http.StatusNotFound, "not_found", "audit record was not found")
	}
	if errStore != nil {
		return storageResponse(errStore)
	}
	settings := h.cfg.MaxTextBytes
	_ = h.state.WithStore(ctx, func(active *store.Store) error {
		current, err := active.GetSettings(ctx, store.SettingsFromConfig(h.cfg))
		if err == nil {
			settings = current.MaxTextBytes
		}
		return nil
	})
	return jsonResponse(http.StatusOK, auditJSON(record, settings))
}

type settingsResponse struct {
	RetentionDays       int  `json:"retention_days"`
	DefaultAuditEnabled bool `json:"default_audit_enabled"`
	MaxTextBytes        int  `json:"max_text_bytes"`
}

type settingsRequest struct {
	RetentionDays       json.RawMessage `json:"retention_days"`
	DefaultAuditEnabled json.RawMessage `json:"default_audit_enabled"`
	MaxTextBytes        json.RawMessage `json:"max_text_bytes"`
}

func (h *Handler) getSettings(ctx context.Context) ManagementResponse {
	var settings store.Settings
	errStore := h.state.WithStore(ctx, func(active *store.Store) error {
		var err error
		settings, err = active.GetSettings(ctx, store.SettingsFromConfig(h.cfg))
		return err
	})
	if errStore != nil {
		return storageResponse(errStore)
	}
	return jsonResponse(http.StatusOK, settingsResponse{RetentionDays: settings.RetentionDays, DefaultAuditEnabled: settings.DefaultAuditEnabled, MaxTextBytes: settings.MaxTextBytes})
}

func (h *Handler) updateSettings(ctx context.Context, body []byte) ManagementResponse {
	var request settingsRequest
	if err := decodeBody(body, &request); err != nil {
		return errorResponse(http.StatusBadRequest, "invalid_input", err.Error())
	}
	if len(request.RetentionDays) == 0 && len(request.DefaultAuditEnabled) == 0 && len(request.MaxTextBytes) == 0 {
		return errorResponse(http.StatusBadRequest, "invalid_input", "at least one setting is required")
	}
	patch := store.SettingsPatch{}
	if len(request.RetentionDays) > 0 {
		value, err := parseIntField(request.RetentionDays, "retention_days")
		if err != nil || value < config.MinRetentionDays || value > config.MaxRetentionDays {
			return errorResponse(http.StatusBadRequest, "invalid_input", fmt.Sprintf("retention_days must be between %d and %d", config.MinRetentionDays, config.MaxRetentionDays))
		}
		patch.RetentionDays = &value
	}
	if len(request.DefaultAuditEnabled) > 0 {
		value, err := parseRequiredBool(request.DefaultAuditEnabled)
		if err != nil {
			return errorResponse(http.StatusBadRequest, "invalid_input", "default_audit_enabled must be boolean")
		}
		patch.DefaultAuditEnabled = &value
	}
	if len(request.MaxTextBytes) > 0 {
		value, err := parseIntField(request.MaxTextBytes, "max_text_bytes")
		if err != nil || value < config.MinMaxTextBytes || value > config.MaxMaxTextBytes {
			return errorResponse(http.StatusBadRequest, "invalid_input", fmt.Sprintf("max_text_bytes must be between %d and %d", config.MinMaxTextBytes, config.MaxMaxTextBytes))
		}
		patch.MaxTextBytes = &value
	}
	if errStore := h.state.WithStore(ctx, func(active *store.Store) error {
		if err := active.UpdateSettings(ctx, patch); err != nil {
			return err
		}
		_, err := active.CleanupExpired(ctx, time.Now())
		return err
	}); errStore != nil {
		return storageResponse(errStore)
	}
	return h.getSettings(ctx)
}

type auditResponse struct {
	ID                    int64  `json:"id"`
	KeyHash               string `json:"key_hash"`
	CreatedAt             string `json:"created_at"`
	Model                 string `json:"model"`
	SourceFormat          string `json:"source_format"`
	RequestID             string `json:"request_id"`
	Outcome               string `json:"outcome"`
	StatusCode            int    `json:"status_code"`
	Text                  string `json:"text"`
	TextAvailable         bool   `json:"text_available"`
	TextUnavailableReason string `json:"text_unavailable_reason,omitempty"`
	TextTruncated         bool   `json:"text_truncated"`
	SecuritySignal        string `json:"security_signal,omitempty"`
	SecurityMessage       string `json:"security_message,omitempty"`
}

func auditJSON(record store.AuditRecord, maxTextBytes int) auditResponse {
	text := record.Text
	if maxTextBytes > 0 && len([]byte(text)) > maxTextBytes {
		text = string([]byte(text)[:maxTextBytes])
		record.TextTruncated = true
	}
	return auditResponse{ID: record.ID, KeyHash: record.KeyHash, CreatedAt: record.CreatedAt.UTC().Format(time.RFC3339), Model: record.Model, SourceFormat: record.SourceFormat, RequestID: record.RequestID, Outcome: record.Outcome, StatusCode: record.StatusCode, Text: text, TextAvailable: record.TextAvailable, TextUnavailableReason: record.TextUnavailableReason, TextTruncated: record.TextTruncated, SecuritySignal: record.SecuritySignal, SecurityMessage: record.SecurityMessage}
}

func queryHashes(query url.Values, name string) ([]string, error) {
	values := query[name]
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > maxHashes {
		return nil, fmt.Errorf("too many %s values", name)
	}
	return normalizeUniqueHashes(values)
}

func normalizeUniqueHashes(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		hash, err := model.NormalizeKeyHash(value)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		result = append(result, hash)
	}
	return result, nil
}

func decodeModels(raw json.RawMessage) ([]string, error) {
	var values []string
	if err := json.Unmarshal(raw, &values); err != nil || values == nil {
		return nil, fmt.Errorf("denied_models must be an array of strings")
	}
	if len(values) > maxModels {
		return nil, fmt.Errorf("denied_models contains too many entries")
	}
	for _, value := range values {
		if len([]byte(value)) > maxModelBytes {
			return nil, fmt.Errorf("model ID is too long")
		}
	}
	return model.NormalizeModelIDs(values)
}

func parseOptionalBool(raw json.RawMessage, nullPreserves bool) (*bool, error) {
	if len(raw) == 0 || (nullPreserves && string(raw) == "null") {
		return nil, nil
	}
	value, err := parseRequiredBool(raw)
	if err != nil {
		return nil, err
	}
	return &value, nil
}

func parseRequiredBool(raw json.RawMessage) (bool, error) {
	if strings.TrimSpace(string(raw)) == "null" {
		return false, errors.New("boolean must not be null")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func parseIntField(raw json.RawMessage, name string) (int, error) {
	var value int
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("%s must be an integer", name)
	}
	return value, nil
}

func parseAuditQuery(query url.Values) (store.AuditFilter, int, int, error) {
	filter := store.AuditFilter{}
	if value := strings.TrimSpace(query.Get("from")); value != "" {
		parsed, err := parseTime(value)
		if err != nil {
			return filter, 0, 0, fmt.Errorf("invalid from time")
		}
		filter.From = &parsed
	}
	if value := strings.TrimSpace(query.Get("to")); value != "" {
		parsed, err := parseTime(value)
		if err != nil {
			return filter, 0, 0, fmt.Errorf("invalid to time")
		}
		filter.To = &parsed
	}
	if filter.From != nil && filter.To != nil && filter.From.After(*filter.To) {
		return filter, 0, 0, fmt.Errorf("from must not be after to")
	}
	if value := strings.TrimSpace(query.Get("key_hash")); value != "" {
		hash, err := model.NormalizeKeyHash(value)
		if err != nil {
			return filter, 0, 0, err
		}
		filter.KeyHash = hash
	}
	if value := strings.TrimSpace(query.Get("model")); value != "" {
		if len([]byte(value)) > maxModelBytes {
			return filter, 0, 0, fmt.Errorf("model ID is too long")
		}
		modelID, err := model.NormalizeModelID(value)
		if err != nil {
			return filter, 0, 0, err
		}
		filter.Model = modelID
	}
	filter.SourceFormat = strings.TrimSpace(query.Get("source_format"))
	if filter.SourceFormat != "" && !allowedSourceFormats[filter.SourceFormat] {
		return filter, 0, 0, fmt.Errorf("invalid source_format")
	}
	filter.Outcome = strings.ToLower(strings.TrimSpace(query.Get("outcome")))
	if filter.Outcome != "" && !allowedOutcomes[filter.Outcome] {
		return filter, 0, 0, fmt.Errorf("invalid outcome")
	}
	filter.SecuritySignal = strings.ToLower(strings.TrimSpace(query.Get("security_signal")))
	if filter.SecuritySignal != "" && filter.SecuritySignal != "cyber_policy" {
		return filter, 0, 0, fmt.Errorf("invalid security_signal")
	}
	page, errPage := boundedQueryInt(query.Get("page"), 1, maxPage, 1)
	if errPage != nil {
		return filter, 0, 0, fmt.Errorf("invalid page")
	}
	pageSize, errPageSize := boundedQueryInt(query.Get("page_size"), 1, maxPageSize, defaultPageSize)
	if errPageSize != nil {
		return filter, 0, 0, fmt.Errorf("invalid page_size")
	}
	return filter, page, pageSize, nil
}

var allowedOutcomes = map[string]bool{"pending": true, "succeeded": true, "failed": true, "rejected": true, "canceled": true}
var allowedSourceFormats = map[string]bool{"openai": true, "openai-response": true, "codex": true, "claude": true, "gemini": true}

func boundedQueryInt(raw string, min, max, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < min || value > max {
		return 0, errors.New("out of bounds")
	}
	return value, nil
}

func parseTime(value string) (time.Time, error) {
	if unix, err := strconv.ParseInt(value, 10, 64); err == nil {
		return time.Unix(unix, 0), nil
	}
	return time.Parse(time.RFC3339Nano, value)
}

func decodeBody(body []byte, target any) error {
	if len(body) == 0 || len(body) > maxBodyBytes {
		return errors.New("request body is missing or too large")
	}
	decoder := json.NewDecoder(strings.NewReader(string(body)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("request body must be valid JSON")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func storageResponse(err error) ManagementResponse {
	if errors.Is(err, state.ErrUnavailable) || errors.Is(err, store.ErrClosed) {
		return errorResponse(http.StatusServiceUnavailable, "storage_unavailable", "plugin storage is unavailable")
	}
	if errors.Is(err, sql.ErrNoRows) {
		return errorResponse(http.StatusNotFound, "not_found", "record was not found")
	}
	return errorResponse(http.StatusServiceUnavailable, "storage_unavailable", "plugin storage operation failed")
}

func jsonHeaders(contentType bool) http.Header {
	headers := http.Header{}
	if contentType {
		headers.Set("Content-Type", "application/json")
	}
	return headers
}

func jsonResponse(status int, value any) ManagementResponse {
	body, err := json.Marshal(value)
	if err != nil {
		return errorResponse(http.StatusServiceUnavailable, "storage_unavailable", "failed to encode response")
	}
	return ManagementResponse{StatusCode: status, Headers: jsonHeaders(true), Body: body}
}

func errorResponse(status int, code, message string) ManagementResponse {
	return ManagementResponse{StatusCode: status, Headers: jsonHeaders(true), Body: errorBody(code, message)}
}

func errorBody(code, message string) []byte {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{"code": code, "message": message}})
	return body
}
