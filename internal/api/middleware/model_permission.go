// Package middleware provides HTTP middleware components for the CLI Proxy API server.
package middleware

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store"
)

// ModelPermissionMiddleware creates a Gin middleware that validates API key permissions against requested models.
// It extracts the model from the request body and checks if the API key has permission to access it.
// Supports OpenAI (/v1/chat/completions, /v1/completions), Claude (/v1/messages), and Gemini (/v1beta) formats.
func ModelPermissionMiddleware(cache *store.KeyPermissionCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		if cache == nil {
			c.Next()
			return
		}

		// Get the API key from the Authorization header
		authHeader := c.GetHeader("Authorization")
		if authHeader == "" {
			c.Next()
			return
		}

		// Extract keyID (remove "Bearer " prefix)
		keyID := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer"))
		if keyID == "" {
			c.Next()
			return
		}

		// Check if key has any restrictions
		perm := cache.Get(keyID)
		if perm == nil {
			c.Next()
			return
		}

		// Extract model from request based on content type and path
		model := extractModelFromRequest(c)
		if model == "" {
			c.Next()
			return
		}

		// Check if model is allowed
		if !cache.IsModelAllowed(keyID, model) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error": gin.H{
					"message": "API key does not have permission to access model: " + model,
					"type":    "forbidden",
					"code":    "model_not_allowed",
				},
			})
			return
		}

		c.Next()
	}
}

// extractModelFromRequest extracts the model name from the request body based on the endpoint
func extractModelFromRequest(c *gin.Context) string {
	path := c.Request.URL.Path

	// Only check POST requests with JSON bodies
	if c.Request.Method != http.MethodPost {
		return ""
	}

	// Skip if no body
	if c.Request.Body == nil {
		return ""
	}

	// Read body
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return ""
	}
	// Restore body for subsequent handlers
	c.Request.Body = io.NopCloser(strings.NewReader(string(body)))

	// Try to extract model from JSON
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}

	// Extract model based on endpoint type
	switch {
	// OpenAI format: /v1/chat/completions, /v1/completions
	case strings.HasSuffix(path, "/chat/completions") || strings.HasSuffix(path, "/completions"):
		if model, ok := payload["model"].(string); ok {
			return strings.TrimSpace(model)
		}

	// Claude format: /v1/messages, /v1/messages/count_tokens
	case strings.HasSuffix(path, "/messages") || strings.HasSuffix(path, "/messages/count_tokens"):
		// Claude format uses "model" field
		if model, ok := payload["model"].(string); ok {
			return strings.TrimSpace(model)
		}
		// Some versions use model ID in the payload
		if model, ok := payload["model_id"].(string); ok {
			return strings.TrimSpace(model)
		}

	// OpenAI Responses format: /v1/responses
	case strings.HasSuffix(path, "/responses"):
		if model, ok := payload["model"].(string); ok {
			return strings.TrimSpace(model)
		}

	// Gemini format: /v1beta/models/{model}:*, but model is also in body
	case strings.HasPrefix(path, "/v1beta/models/"):
		// Extract model from URL path if present
		modelFromPath := extractGeminiModelFromPath(path)
		if modelFromPath != "" {
			return modelFromPath
		}
		// Fallback to body
		if model, ok := payload["model"].(string); ok {
			return strings.TrimSpace(model)
		}

	// Default: try to get "model" field
	default:
		if model, ok := payload["model"].(string); ok {
			return strings.TrimSpace(model)
		}
	}

	return ""
}

// extractGeminiModelFromPath extracts model name from Gemini path like /v1beta/models/{model}:action
func extractGeminiModelFromPath(path string) string {
	prefix := "/v1beta/models/"
	if !strings.HasPrefix(path, prefix) {
		return ""
	}

	// Remove prefix
	remaining := strings.TrimPrefix(path, prefix)
	// Remove leading "models/" if present in the remaining path
	remaining = strings.TrimPrefix(remaining, "models/")

	// Find action separator (:)
	idx := strings.Index(remaining, ":")
	if idx > 0 {
		return strings.TrimSpace(remaining[:idx])
	}

	// Also handle paths like /v1beta/models/{model}
	idx = strings.Index(remaining, "/")
	if idx > 0 {
		return strings.TrimSpace(remaining[:idx])
	}

	return strings.TrimSpace(remaining)
}
