package management

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/modeltrace"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// InitializeModelTrace attaches the native executor and config-adjacent store.
// It is called once during server construction, never on auth/config reload.
func (h *Handler) InitializeModelTrace(executor modeltrace.ModelExecutor) {
	var execute modeltrace.ExecuteFunc
	if executor != nil {
		execute = func(ctx context.Context, c modeltrace.Credential, model string, ch modeltrace.Challenge) (modeltrace.Output, error) {
			// Re-read the selected account before each challenge. File replacement must
			// not silently charge another identity under an unchanged filename/AuthID.
			h.mu.Lock()
			manager := h.authManager
			h.mu.Unlock()
			if manager == nil {
				return modeltrace.Output{}, fmt.Errorf("auth manager unavailable")
			}
			a, ok := manager.GetByID(c.ID)
			if !ok || modelTraceIdentity(a) != c.Identity || !modelTraceAvailable(a, model) {
				return modeltrace.Output{}, fmt.Errorf("selected credential unavailable")
			}
			return modeltrace.Execute(ctx, executor, c, model, ch)
		}
	}
	path := ""
	if strings.TrimSpace(h.configFilePath) != "" {
		path = filepath.Join(filepath.Dir(h.configFilePath), "modeltrace-state.json")
	}
	h.modelTrace = modeltrace.New(path, execute)
}
func (h *Handler) StopModelTrace(ctx context.Context) error {
	if h == nil || h.modelTrace == nil {
		return nil
	}
	return h.modelTrace.Stop(ctx)
}

// Identity is derived exclusively from non-secret account identifiers. Refuse
// unknown identity rather than letting filename-only records survive replacement.
func modelTraceIdentity(a *coreauth.Auth) string {
	if a == nil || a.Provider != "codex" {
		return ""
	}
	account := ""
	for _, key := range []string{"account_id", "chatgpt_account_id"} {
		if value, ok := a.Metadata[key].(string); ok && strings.TrimSpace(value) != "" {
			account = "id:" + strings.TrimSpace(value)
			break
		}
		if value := strings.TrimSpace(a.Attributes[key]); value != "" {
			account = "id:" + value
			break
		}
	}
	if account == "" {
		if claims := extractCodexIDTokenClaims(a); claims != nil {
			if id, ok := claims["chatgpt_account_id"].(string); ok && id != "" {
				account = "id:" + id
			}
		}
	}
	if account == "" {
		if email := authEmail(a); email != "" {
			account = "email:" + strings.ToLower(email)
		}
	}
	if account == "" {
		return ""
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(a.Provider+"\x00"+a.ID+"\x00"+account)))
}
func modelTraceAvailable(a *coreauth.Auth, model string) bool {
	if a == nil || a.Provider != "codex" || a.Disabled || a.Status == coreauth.StatusDisabled {
		return false
	}
	now := time.Now().UTC()
	unavailable, _, _, _ := reconcileAuthFileCooldownState(a, now)
	if unavailable || isPersistentAuthFailure(a, now) || isModelStateBlocked(a.ModelStates[model], now) {
		return false
	}
	for _, m := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
		if m.ID == model {
			return true
		}
	}
	return false
}
func (h *Handler) modelTraceCredential(index string) (modeltrace.Credential, *coreauth.Auth) {
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager == nil {
		return modeltrace.Credential{}, nil
	}
	for _, a := range manager.List() {
		if a.Provider == "codex" && lockedAuthIndex(a) == index {
			return modeltrace.Credential{Index: index, ID: a.ID, Identity: modelTraceIdentity(a)}, a
		}
	}
	return modeltrace.Credential{}, nil
}
func (h *Handler) modelTraceReady(c *gin.Context) bool {
	if h == nil || h.modelTrace == nil {
		c.JSON(503, gin.H{"error": "ModelTrace unavailable"})
		return false
	}
	return true
}
func (h *Handler) GetModelTrace(c *gin.Context) {
	if !h.modelTraceReady(c) {
		return
	}
	states := map[string]modeltrace.State{}
	h.mu.Lock()
	manager := h.authManager
	h.mu.Unlock()
	if manager != nil {
		for _, a := range manager.List() {
			if a.Provider != "codex" {
				continue
			}
			identity := modelTraceIdentity(a)
			if identity == "" {
				continue
			}
			cred := modeltrace.Credential{Index: lockedAuthIndex(a), ID: a.ID, Identity: identity}
			state := h.modelTrace.State(cred)
			if state.Running != nil || state.Latest != nil {
				states[cred.Index] = state
			}
		}
	}
	response := gin.H{"bank_revision": modeltrace.BankRevision(), "models": modeltrace.Models(), "states": states}
	if err := h.modelTrace.StorageError(); err != "" {
		response["storage_error"] = err
	}
	c.JSON(200, response)
}
func (h *Handler) StartModelTrace(c *gin.Context) {
	if !h.modelTraceReady(c) {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 4096)
	var body struct {
		AuthIndex string `json:"auth_index"`
		Model     string `json:"model"`
	}
	if c.ShouldBindJSON(&body) != nil || strings.TrimSpace(body.AuthIndex) == "" || body.Model == "" {
		c.JSON(400, gin.H{"error": "auth_index and model are required"})
		return
	}
	cred, a := h.modelTraceCredential(strings.TrimSpace(body.AuthIndex))
	if a == nil {
		c.JSON(404, gin.H{"error": "unknown Codex credential"})
		return
	}
	if !modeltrace.Supports(body.Model) {
		c.JSON(400, gin.H{"error": "target model is not covered by the candidate bank"})
		return
	}
	supported := false
	for _, m := range registry.GetGlobalRegistry().GetModelsForClient(a.ID) {
		if m.ID == body.Model {
			supported = true
			break
		}
	}
	if !supported {
		c.JSON(400, gin.H{"error": "target model is not supported by this credential"})
		return
	}
	if cred.Identity == "" || !modelTraceAvailable(a, body.Model) {
		c.JSON(409, gin.H{"error": "selected credential disabled, unavailable, or missing account identity"})
		return
	}
	run, err := h.modelTrace.Start(cred, body.Model)
	if err != nil {
		c.JSON(modeltrace.ErrorCode(err), gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"run": run})
}
func (h *Handler) CancelModelTrace(c *gin.Context) {
	if !h.modelTraceReady(c) {
		return
	}
	index := strings.TrimSpace(c.Query("auth_index"))
	if index == "" {
		c.JSON(400, gin.H{"error": "auth_index is required"})
		return
	}
	cred, a := h.modelTraceCredential(index)
	if a == nil {
		c.JSON(404, gin.H{"error": "unknown Codex credential"})
		return
	}
	c.JSON(200, gin.H{"cancelled": h.modelTrace.Cancel(cred)})
}
func (h *Handler) GetModelTraceRecord(c *gin.Context) {
	if !h.modelTraceReady(c) {
		return
	}
	index := strings.TrimSpace(c.Query("auth_index"))
	if index == "" {
		c.JSON(400, gin.H{"error": "auth_index is required"})
		return
	}
	cred, a := h.modelTraceCredential(index)
	if a == nil {
		c.JSON(404, gin.H{"error": "unknown Codex credential"})
		return
	}
	record, ok := h.modelTrace.Record(cred, strings.TrimSpace(c.Query("id")))
	if !ok {
		c.JSON(404, gin.H{"error": "ModelTrace record not found"})
		return
	}
	c.JSON(200, gin.H{"record": record})
}
