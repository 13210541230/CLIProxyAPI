package management

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/opencode"
)

const openCodeQuotaMaxAge = time.Minute

// ListOpenCodeAccounts returns OpenCode Go accounts together with their quota
// snapshot. The endpoint is read-only: it never returns plaintext API keys and
// never mutates account configuration.
func (h *Handler) ListOpenCodeAccounts(c *gin.Context) {
	if h == nil || h.authManager == nil {
		c.JSON(http.StatusOK, gin.H{"accounts": []opencode.AccountView{}})
		return
	}
	executor := opencode.Default()
	auths := h.authManager.List()
	if executor == nil {
		c.JSON(http.StatusOK, gin.H{"accounts": []opencode.AccountView{}})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	executor.RefreshQuotas(ctx, auths, openCodeQuotaMaxAge)
	c.JSON(http.StatusOK, gin.H{"accounts": executor.AccountViews(auths)})
}
