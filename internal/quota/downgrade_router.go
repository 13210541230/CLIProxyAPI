package quota

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// KeyHashMetadataKey identifies the short API-key hash carried in execution metadata.
const KeyHashMetadataKey = "quota_key_hash"

const (
	codexProvider          = "codex"
	codexAlphaSearchFormat = "codex-alpha-search"
	openAIFormat           = "openai"
	openAIResponseFormat   = "openai-response"
	claudeFormat           = "claude"
	geminiFormat           = "gemini"
	interactionsFormat     = "interactions"
)

// DowngradeRouter applies persisted quota fallback state before dynamic model routers.
type DowngradeRouter struct {
	manager *Manager
}

// QuotaDowngradeRouter is the stable descriptive name for the built-in router.
type QuotaDowngradeRouter = DowngradeRouter

// NewDowngradeRouter creates an in-process quota model router.
func NewDowngradeRouter(manager *Manager) *QuotaDowngradeRouter {
	return &DowngradeRouter{manager: manager}
}

// RouteModel implements pluginapi.ModelRouter for quota downgrade decisions.
func (r *DowngradeRouter) RouteModel(_ context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, error) {
	if r == nil || r.manager == nil || !isQuotaDowngradeEligibleRequest(req) {
		return pluginapi.ModelRouteResponse{}, nil
	}

	keyHash := metadataString(req.Metadata, KeyHashMetadataKey)
	if keyHash == "" {
		return pluginapi.ModelRouteResponse{}, nil
	}
	matched, entry, _ := r.manager.IsDowngraded(keyHash)
	if !matched || entry == nil {
		return pluginapi.ModelRouteResponse{}, nil
	}

	fallbackModel := strings.TrimSpace(entry.FallbackModel)
	if fallbackModel == "" || sameModelBase(req.RequestedModel, fallbackModel) {
		return pluginapi.ModelRouteResponse{}, nil
	}

	return pluginapi.ModelRouteResponse{
		Handled:     true,
		TargetKind:  pluginapi.ModelRouteTargetProvider,
		Target:      codexProvider,
		TargetModel: fallbackModel,
		Reason:      "quota downgrade: " + strings.TrimSpace(entry.Reason),
	}, nil
}

func metadataString(metadata map[string]any, key string) string {
	if metadata == nil {
		return ""
	}
	value, ok := metadata[key]
	if !ok {
		return ""
	}
	text, ok := value.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(text)
}

func sameModelBase(left, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if i := strings.IndexByte(left, '('); i >= 0 {
		left = strings.TrimSpace(left[:i])
	}
	if i := strings.IndexByte(right, '('); i >= 0 {
		right = strings.TrimSpace(right[:i])
	}
	return left != "" && strings.EqualFold(left, right)
}

func isQuotaDowngradeEligibleRequest(req pluginapi.ModelRouteRequest) bool {
	source := strings.ToLower(strings.TrimSpace(req.SourceFormat))
	switch source {
	case openAIFormat, openAIResponseFormat, claudeFormat, geminiFormat, interactionsFormat, codexAlphaSearchFormat:
	default:
		return false
	}

	path := strings.ToLower(strings.TrimSpace(metadataString(req.Metadata, "request_path")))
	if path == "" {
		return true
	}
	if strings.Contains(path, "/count_tokens") || strings.Contains(path, ":counttokens") {
		return false
	}
	for _, excluded := range []string{"/realtime", "/live", "/audio", "/images", "/videos", "/usage", "/management"} {
		if strings.Contains(path, excluded) {
			return false
		}
	}
	trimmedPath := strings.TrimRight(path, "/")
	if strings.HasSuffix(trimmedPath, "/models") {
		return false
	}
	return true
}
