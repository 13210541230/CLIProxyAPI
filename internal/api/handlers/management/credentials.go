package management

import (
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// ListAuthCredentials returns every registered credential, including
// config-sourced provider entries (codex-api-key, openai-compatibility,
// claude-api-key, ...), unlike /auth-files which only lists on-disk
// authentication files. It feeds per-account views such as the
// concurrency-limit editor so any provider credential can be rate limited.
func (h *Handler) ListAuthCredentials(c *gin.Context) {
	if h == nil || c == nil {
		return
	}
	observedAt := time.Now().UTC()
	if h.authManager == nil {
		c.JSON(200, gin.H{"observed_at": observedAt, "credentials": []gin.H{}})
		return
	}
	auths := h.authManager.List()
	credentials := make([]gin.H, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		provider := strings.ToLower(strings.TrimSpace(auth.Provider))
		label := strings.TrimSpace(auth.Label)
		name := label
		if name == "" {
			name = strings.TrimSpace(auth.FileName)
		}
		if name == "" {
			name = auth.ID
		}
		credentials = append(credentials, gin.H{
			"id":         auth.ID,
			"provider":   provider,
			"name":       name,
			"label":      label,
			"auth_kind":  auth.AuthKind(),
			"source":     auth.AuthSourceKind(),
			"status":     string(auth.Status),
			"disabled":   auth.Disabled || auth.Status == coreauth.StatusDisabled,
			"created_at": auth.CreatedAt,
		})
	}
	sort.SliceStable(credentials, func(i, j int) bool {
		providerI, _ := credentials[i]["provider"].(string)
		providerJ, _ := credentials[j]["provider"].(string)
		if providerI != providerJ {
			return providerI < providerJ
		}
		nameI, _ := credentials[i]["name"].(string)
		nameJ, _ := credentials[j]["name"].(string)
		return strings.ToLower(nameI) < strings.ToLower(nameJ)
	})
	c.JSON(200, gin.H{"observed_at": observedAt, "credentials": credentials})
}
