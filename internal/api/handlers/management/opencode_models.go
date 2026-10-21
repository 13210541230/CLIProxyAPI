package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/opencode"
)

// ListOpenCodeModels returns the full OpenCode Go model catalog together with the
// configured exclusion patterns, so the console can render per-model toggles for
// what CLIProxyAPI exposes downstream.
func (h *Handler) ListOpenCodeModels(c *gin.Context) {
	excluded := []string{}
	if h != nil && h.cfg != nil {
		if patterns := opencode.ExcludedModels(h.cfg); len(patterns) > 0 {
			excluded = patterns
		}
	}
	models := opencode.CatalogModels()
	if models == nil {
		models = []opencode.CatalogModel{}
	}
	c.JSON(http.StatusOK, gin.H{"models": models, "excluded": excluded})
}

// PutOpenCodeModels replaces the exposed-model selection and republishes the
// provider's models immediately. The generic oauth-excluded-models endpoint is not
// enough on its own: it skips API-key providers and does not trigger a model
// re-registration, so the change would only take effect after a restart.
func (h *Handler) PutOpenCodeModels(c *gin.Context) {
	var body struct {
		Excluded []string `json:"excluded"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config is unavailable"})
		return
	}
	normalized := config.NormalizeExcludedModels(body.Excluded)

	h.mu.Lock()
	if len(normalized) == 0 {
		if h.cfg.OAuthExcludedModels != nil {
			delete(h.cfg.OAuthExcludedModels, opencode.ProviderName)
			if len(h.cfg.OAuthExcludedModels) == 0 {
				h.cfg.OAuthExcludedModels = nil
			}
		}
	} else {
		if h.cfg.OAuthExcludedModels == nil {
			h.cfg.OAuthExcludedModels = make(map[string][]string)
		}
		h.cfg.OAuthExcludedModels[opencode.ProviderName] = normalized
	}
	err := config.SaveConfigPreserveComments(h.configFilePath, h.cfg, c.GetBool(ConfigV8ContextKey))
	published := 0
	if err == nil {
		published = opencode.Republish(h.cfg)
	}
	h.mu.Unlock()

	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "excluded": normalized, "published": published})
}
