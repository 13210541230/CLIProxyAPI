package intercept

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/cyberpolicy"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/extract"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/model"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

const (
	KeyHashMetadataKey          = "quota_key_hash"
	RequestPathMetadataKey      = "request_path"
	ExecutionSessionMetadataKey = "execution_session_id"
)

var geminiPath = regexp.MustCompile(`^/v1beta/models/[^/:]+:(generateContent|streamGenerateContent)$`)

// Request mirrors the public interceptor envelope without importing CPA's root module.
type Request struct {
	RequestID      string         `json:"RequestID"`
	TraceID        string         `json:"TraceID"`
	SourceFormat   string         `json:"SourceFormat"`
	ToFormat       string         `json:"ToFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Headers        http.Header    `json:"Headers"`
	Body           []byte         `json:"Body"`
	Metadata       map[string]any `json:"Metadata"`
}

// Completion mirrors the public lifecycle completion envelope.
type Completion struct {
	RequestID      string         `json:"RequestID"`
	TraceID        string         `json:"TraceID"`
	SourceFormat   string         `json:"SourceFormat"`
	Model          string         `json:"Model"`
	RequestedModel string         `json:"RequestedModel"`
	Stream         bool           `json:"Stream"`
	Outcome        string         `json:"Outcome"`
	StatusCode     int            `json:"StatusCode"`
	Error          string         `json:"Error"`
	Metadata       map[string]any `json:"Metadata"`
}

// Response mirrors the public interceptor response and keeps pass-through bytes unchanged.
type Response struct {
	Headers         http.Header `json:"Headers"`
	Body            []byte      `json:"Body"`
	ClearHeaders    []string    `json:"ClearHeaders,omitempty"`
	Terminate       bool        `json:"Terminate,omitempty"`
	StatusCode      int         `json:"StatusCode,omitempty"`
	ResponseHeaders http.Header `json:"ResponseHeaders,omitempty"`
	ResponseBody    []byte      `json:"ResponseBody,omitempty"`
}

// Handler applies phase-1 scope, policy enforcement, extraction, and lifecycle correlation.
type Handler struct {
	state *state.Manager
	cfg   config.Config
}

// New creates a request handler using T1's validated defaults.
func New(manager *state.Manager, cfg config.Config) *Handler {
	return &Handler{state: manager, cfg: cfg}
}

// Intercept performs the same policy check at both before-auth and after-auth stages.
func (h *Handler) Intercept(ctx context.Context, req Request) (Response, error) {
	response := Response{Headers: req.Headers, Body: req.Body}
	path, ok := phaseOnePath(req.SourceFormat, req.Metadata)
	if !ok || req.RequestID == "" {
		return response, nil
	}
	keyHash, errHash := metadataHash(req.Metadata)
	if errHash != nil {
		return response, nil
	}
	text, errExtract := extract.Extract(req.SourceFormat, path, req.Body)
	if errExtract != nil {
		// Keep a metadata-only row for a valid identity, but never store malformed bytes.
		text.Text = ""
		text.TextAvailable = false
		if text.TextUnavailableReason == "" {
			text.TextUnavailableReason = "malformed_json"
		}
	}
	modelName := firstModel(req.Model, req.RequestedModel)
	var denied bool
	errStore := h.state.WithStore(ctx, func(active *store.Store) error {
		settings, errSettings := active.GetSettings(ctx, store.SettingsFromConfig(h.cfg))
		if errSettings != nil {
			return fmt.Errorf("load audit settings: %w", errSettings)
		}
		policy, errPolicy := active.GetPolicyWithDefault(ctx, keyHash, settings.DefaultAuditEnabled)
		if errPolicy != nil {
			return fmt.Errorf("load policy: %w", errPolicy)
		}
		denied = deniedModel(policy, req.RequestedModel) || deniedModel(policy, req.Model)
		if !policy.AuditEnabled {
			return nil
		}
		record := store.AuditRecord{
			KeyHash:               keyHash,
			Model:                 modelName,
			SourceFormat:          req.SourceFormat,
			RequestID:             req.RequestID,
			Outcome:               "pending",
			Text:                  text.Text,
			TextAvailable:         text.TextAvailable,
			TextUnavailableReason: text.TextUnavailableReason,
		}
		if denied {
			record.Outcome = string("rejected")
			record.StatusCode = http.StatusForbidden
		}
		if errDraft := active.UpsertAuditDraft(ctx, record); errDraft != nil {
			return fmt.Errorf("persist audit draft: %w", errDraft)
		}
		return nil
	})
	if errStore != nil {
		return Response{}, errStore
	}
	if denied {
		return rejectedResponse(), nil
	}
	return response, nil
}

// Complete finalizes an existing draft and records terminal outcomes without request bodies.
func (h *Handler) Complete(ctx context.Context, completion Completion) error {
	_, ok := phaseOnePath(completion.SourceFormat, completion.Metadata)
	if !ok || completion.RequestID == "" {
		return nil
	}
	keyHash, errHash := metadataHash(completion.Metadata)
	if errHash != nil {
		return nil
	}
	if !validOutcome(completion.Outcome) {
		return fmt.Errorf("unsupported request completion outcome %q", completion.Outcome)
	}
	securitySignal := ""
	securityMessage := ""
	if completion.Outcome != "succeeded" && completion.Outcome != "rejected" {
		var detected bool
		securityMessage, detected = cyberpolicy.Extract(completion.Error)
		if detected {
			securitySignal = cyberpolicy.UpstreamCyberPolicyCode
		}
	}
	return h.state.WithStore(ctx, func(active *store.Store) error {
		settings, errSettings := active.GetSettings(ctx, store.SettingsFromConfig(h.cfg))
		if errSettings != nil {
			return fmt.Errorf("load audit settings for completion: %w", errSettings)
		}
		policy, errPolicy := active.GetPolicyWithDefault(ctx, keyHash, settings.DefaultAuditEnabled)
		if errPolicy != nil {
			return fmt.Errorf("load policy for completion: %w", errPolicy)
		}
		return active.FinalizeAudit(ctx, store.AuditRecord{
			KeyHash:         keyHash,
			Model:           firstModel(completion.Model, completion.RequestedModel),
			SourceFormat:    completion.SourceFormat,
			RequestID:       completion.RequestID,
			Outcome:         completion.Outcome,
			StatusCode:      completion.StatusCode,
			SecuritySignal:  securitySignal,
			SecurityMessage: securityMessage,
		}, policy.AuditEnabled)
	})
}

func phaseOnePath(sourceFormat string, metadata map[string]any) (string, bool) {
	path, ok := metadataString(metadata, RequestPathMetadataKey)
	if !ok || path == "" || metadataValuePresent(metadata, ExecutionSessionMetadataKey) {
		return "", false
	}
	switch path {
	case "/v1/chat/completions", "/v1/completions":
		return path, sourceFormat == "openai"
	case "/v1/responses", "/backend-api/codex/responses":
		return path, sourceFormat == "openai-response" || sourceFormat == "codex"
	case "/v1/messages":
		return path, sourceFormat == "claude"
	default:
		return path, sourceFormat == "gemini" && geminiPath.MatchString(path)
	}
}

func metadataHash(metadata map[string]any) (string, error) {
	value, ok := metadataString(metadata, KeyHashMetadataKey)
	if !ok {
		return "", fmt.Errorf("missing canonical API-key hash")
	}
	return model.NormalizeKeyHash(value)
}

func metadataString(metadata map[string]any, key string) (string, bool) {
	value, ok := metadata[key]
	if !ok {
		return "", false
	}
	text, ok := value.(string)
	return strings.TrimSpace(text), ok
}

func metadataValuePresent(metadata map[string]any, key string) bool {
	_, ok := metadata[key]
	return ok
}

func deniedModel(policy model.Policy, modelID string) bool {
	if strings.TrimSpace(modelID) == "" {
		return false
	}
	allowed, errAllow := policy.Allows(modelID)
	return errAllow == nil && !allowed
}

func firstModel(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func validOutcome(outcome string) bool {
	switch outcome {
	case "succeeded", "failed", "rejected", "canceled":
		return true
	default:
		return false
	}
}

func rejectedResponse() Response {
	body, _ := json.Marshal(map[string]any{"error": map[string]string{
		"type":    "model_not_allowed",
		"code":    "model_not_allowed",
		"message": "model is not allowed for this API key",
	}})
	return Response{
		Terminate:       true,
		StatusCode:      http.StatusForbidden,
		ResponseHeaders: http.Header{"Content-Type": []string{"application/json"}},
		ResponseBody:    body,
	}
}
