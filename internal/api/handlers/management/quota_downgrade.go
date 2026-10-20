package management

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// PostDowngradeKey creates or refreshes a key's fallback-model state.
func (h *Handler) PostDowngradeKey(c *gin.Context) {
	qm := h.currentQuotaManager()
	if qm == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "quota is disabled"})
		return
	}

	var body struct {
		KeyHash          string `json:"key_hash"`
		Reason           string `json:"reason"`
		FallbackModel    string `json:"fallback_model"`
		ExpiresInSeconds int64  `json:"expires_in_seconds"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	keyHash := normalizeKeyHash(body.KeyHash)
	if keyHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_hash is required"})
		return
	}
	fallbackModel, err := validateFallbackModel(body.FallbackModel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	var expiresAt time.Time
	if body.ExpiresInSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(body.ExpiresInSeconds) * time.Second)
	}
	if err := qm.DowngradeKey(keyHash, body.Reason, fallbackModel, expiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PostResumeDowngradeKey removes a key's fallback-model state.
func (h *Handler) PostResumeDowngradeKey(c *gin.Context) {
	qm := h.currentQuotaManager()
	if qm == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "quota is disabled"})
		return
	}

	var body struct {
		KeyHash        string `json:"key_hash"`
		ExpectedReason string `json:"expected_reason"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	keyHash := normalizeKeyHash(body.KeyHash)
	if keyHash == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "key_hash is required"})
		return
	}
	if body.ExpectedReason != "" {
		err := qm.ResumeDowngradeKeyIfReason(keyHash, body.ExpectedReason)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else if err := qm.ResumeDowngradeKey(keyHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetDowngradedKeys returns active fallback-model states.
func (h *Handler) GetDowngradedKeys(c *gin.Context) {
	qm := h.currentQuotaManager()
	if qm == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "entries": []quota.DowngradeEntry{}})
		return
	}
	entries, err := qm.ListDowngraded()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// PostValidateFallbackModel validates a Codex text model before it is persisted remotely.
func (h *Handler) PostValidateFallbackModel(c *gin.Context) {
	var body struct {
		FallbackModel string `json:"fallback_model"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}
	fallbackModel, err := validateFallbackModel(body.FallbackModel)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok", "fallback_model": fallbackModel})
}

func (h *Handler) currentQuotaManager() *quota.Manager {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.quotaManager
}

func validateFallbackModel(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("fallback_model is required")
	}

	info := registry.LookupModelInfo(model, "codex")
	if info == nil || !isCodexModel(model) || !supportsTextInput(info) {
		return "", fmt.Errorf("invalid fallback_model")
	}
	return model, nil
}

func isCodexModel(model string) bool {
	if registry.GetGlobalRegistry().GetModelInfo(model, "codex") != nil {
		return true
	}
	for _, info := range registry.GetStaticModelDefinitionsByChannel("codex") {
		if info != nil && info.ID == model {
			return true
		}
	}
	return false
}

func supportsTextInput(info *registry.ModelInfo) bool {
	if info == nil || info.Type == registry.OpenAIImageModelType {
		return false
	}
	for _, modality := range info.SupportedInputModalities {
		if strings.EqualFold(strings.TrimSpace(modality), "text") {
			return true
		}
	}
	return false
}
