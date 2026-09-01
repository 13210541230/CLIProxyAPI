package management

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
)

// Quota exceeded toggles
func (h *Handler) GetSwitchProject(c *gin.Context) {
	c.JSON(200, gin.H{"switch-project": h.cfg.QuotaExceeded.SwitchProject})
}
func (h *Handler) PutSwitchProject(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchProject = v })
}

func (h *Handler) GetSwitchPreviewModel(c *gin.Context) {
	c.JSON(200, gin.H{"switch-preview-model": h.cfg.QuotaExceeded.SwitchPreviewModel})
}
func (h *Handler) PutSwitchPreviewModel(c *gin.Context) {
	h.updateBoolField(c, func(v bool) { h.cfg.QuotaExceeded.SwitchPreviewModel = v })
}

// normalizeKeyHash accepts either an 8-char hex key_hash or a raw API key (sk-xxx)
// and returns the canonical 8-char hex hash.
func normalizeKeyHash(input string) string {
	input = strings.TrimSpace(input)
	if input == "" {
		return ""
	}
	// Canonical short hashes are lower-case hexadecimal.
	if len(input) == 8 && isHexString(input) {
		return strings.ToLower(input)
	}
	// Otherwise treat as raw API key and hash it.
	return quota.KeyHash(input)
}

func isHexString(s string) bool {
	if len(s) == 0 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// PostPauseKey pauses an API key.
// Body: {"key_hash": "..."} where key_hash can be the 8-char hash or the raw sk-xxx key.
func (h *Handler) PostPauseKey(c *gin.Context) {
	h.mu.Lock()
	qm := h.quotaManager
	h.mu.Unlock()

	if qm == nil {
		c.JSON(http.StatusConflict, gin.H{"error": "quota is disabled"})
		return
	}

	var body struct {
		KeyHash          string `json:"key_hash"`
		Reason           string `json:"reason"`
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

	var expiresAt time.Time
	if body.ExpiresInSeconds > 0 {
		expiresAt = time.Now().Add(time.Duration(body.ExpiresInSeconds) * time.Second)
	}

	if err := qm.PauseKey(keyHash, body.Reason, expiresAt); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// PostResumeKey resumes a paused API key.
// Body: {"key_hash": "..."} where key_hash can be the 8-char hash or the raw sk-xxx key.
func (h *Handler) PostResumeKey(c *gin.Context) {
	h.mu.Lock()
	qm := h.quotaManager
	h.mu.Unlock()

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
		if err := qm.ResumeKeyIfReason(keyHash, body.ExpectedReason); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	} else if err := qm.ResumeKey(keyHash); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// GetPausedKeys returns all non-expired pause entries.
func (h *Handler) GetPausedKeys(c *gin.Context) {
	h.mu.Lock()
	qm := h.quotaManager
	h.mu.Unlock()

	if qm == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "entries": []quota.PauseEntry{}})
		return
	}

	entries, err := qm.ListPaused()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if entries == nil {
		entries = []quota.PauseEntry{}
	}
	c.JSON(http.StatusOK, gin.H{"entries": entries})
}

// GetQuotaConfig returns the current quota configuration.
func (h *Handler) GetQuotaConfig(c *gin.Context) {
	if h == nil || h.cfg == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "mode": "cost", "db_path": "", "default": gin.H{}, "overrides": []gin.H{}})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"enabled":   h.cfg.Quota.Enabled,
		"mode":      effectiveQuotaMode(h.cfg.Quota.Mode),
		"db_path":   h.cfg.Quota.DBPath,
		"default":   gin.H{"daily_cents": h.cfg.Quota.Default.DailyCents, "weekly_cents": h.cfg.Quota.Default.WeeklyCents, "daily_tokens": h.cfg.Quota.Default.DailyTokens, "weekly_tokens": h.cfg.Quota.Default.WeeklyTokens},
		"overrides": quotaOverridesToJSON(h.cfg.Quota.Overrides),
	})
}

func quotaOverridesToJSON(entries []quota.SpendLimitEntry) []gin.H {
	out := make([]gin.H, len(entries))
	for i, e := range entries {
		out[i] = gin.H{
			"apply_to":      e.ApplyTo,
			"apply_value":   e.ApplyValue,
			"daily_cents":   e.DailyCents,
			"weekly_cents":  e.WeeklyCents,
			"daily_tokens":  e.DailyTokens,
			"weekly_tokens": e.WeeklyTokens,
		}
	}
	return out
}

// PutQuotaConfig updates the quota configuration and persists to disk.
func (h *Handler) PutQuotaConfig(c *gin.Context) {
	h.mu.Lock()
	defer h.mu.Unlock()

	var body struct {
		Enabled   *bool                 `json:"enabled"`
		DBPath    *string               `json:"db_path"`
		Mode      *string               `json:"mode"`
		Default   *spendLimitBody       `json:"default"`
		Overrides []spendLimitEntryBody `json:"overrides"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body"})
		return
	}

	if body.Enabled != nil {
		h.cfg.Quota.Enabled = *body.Enabled
	}
	if body.DBPath != nil {
		h.cfg.Quota.DBPath = *body.DBPath
	}
	if body.Mode != nil {
		h.cfg.Quota.Mode = effectiveQuotaMode(*body.Mode)
	}
	if body.Default != nil {
		if body.Default.DailyCents != nil {
			h.cfg.Quota.Default.DailyCents = *body.Default.DailyCents
		}
		if body.Default.WeeklyCents != nil {
			h.cfg.Quota.Default.WeeklyCents = *body.Default.WeeklyCents
		}
		if body.Default.DailyTokens != nil {
			h.cfg.Quota.Default.DailyTokens = *body.Default.DailyTokens
		}
		if body.Default.WeeklyTokens != nil {
			h.cfg.Quota.Default.WeeklyTokens = *body.Default.WeeklyTokens
		}
	}
	if body.Overrides != nil {
		h.cfg.Quota.Overrides = make([]quota.SpendLimitEntry, len(body.Overrides))
		for i, o := range body.Overrides {
			h.cfg.Quota.Overrides[i] = quota.SpendLimitEntry{
				ApplyTo:      o.ApplyTo,
				ApplyValue:   o.ApplyValue,
				DailyCents:   o.DailyCents,
				WeeklyCents:  o.WeeklyCents,
				DailyTokens:  o.DailyTokens,
				WeeklyTokens: o.WeeklyTokens,
			}
		}
	}

	h.persistLocked(c)

	// Notify the quota manager about config changes for hot-reload.
	if h.quotaManager != nil {
		h.quotaManager.UpdateConfig(h.cfg.Quota)
	}
}

type spendLimitBody struct {
	DailyCents   *int64 `json:"daily_cents"`
	WeeklyCents  *int64 `json:"weekly_cents"`
	DailyTokens  *int64 `json:"daily_tokens"`
	WeeklyTokens *int64 `json:"weekly_tokens"`
}

type spendLimitEntryBody struct {
	ApplyTo      string `json:"apply_to"`
	ApplyValue   string `json:"apply_value"`
	DailyCents   int64  `json:"daily_cents"`
	WeeklyCents  int64  `json:"weekly_cents"`
	DailyTokens  int64  `json:"daily_tokens"`
	WeeklyTokens int64  `json:"weekly_tokens"`
}

func effectiveQuotaMode(mode string) string {
	if strings.EqualFold(strings.TrimSpace(mode), "tokens") {
		return "tokens"
	}
	return "cost"
}

// ResetQuota clears quota/cooldown routing state for one auth index.
func (h *Handler) ResetQuota(c *gin.Context) {
	if h.authManager == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "core auth manager unavailable"})
		return
	}

	var req struct {
		AuthIndex string `json:"auth_index"`
	}
	if errBindJSON := c.ShouldBindJSON(&req); errBindJSON != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	authIndex := strings.TrimSpace(req.AuthIndex)
	if authIndex == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "auth_index is required"})
		return
	}

	auth := h.authByIndex(authIndex)
	if auth == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}

	updated, models, errReset := h.authManager.ResetQuota(c.Request.Context(), auth.ID)
	if errReset != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to reset quota: %v", errReset)})
		return
	}
	if updated == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "auth not found"})
		return
	}
	updated.EnsureIndex()

	c.JSON(http.StatusOK, gin.H{
		"status":     "ok",
		"auth_index": updated.Index,
		"models":     models,
	})
}
