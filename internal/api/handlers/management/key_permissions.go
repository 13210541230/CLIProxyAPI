package management

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/store"
)

// GetKeyPermissions returns all API key permissions
// managed api endpoint
func (h *Handler) GetKeyPermissions(c *gin.Context) {
	cache := getKeyPermissionCache(c)
	if cache == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "key_permission_cache_not_available",
			"message": "Key permission cache not initialized",
		})
		return
	}
	perms := cache.List()
	c.JSON(http.StatusOK, gin.H{"key_permissions": perms})
}

// GetKeyPermission returns a specific API key permission
// managed api endpoint
func (h *Handler) GetKeyPermission(c *gin.Context) {
	cache := getKeyPermissionCache(c)
	if cache == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "key_permission_cache_not_available",
			"message": "Key permission cache not initialized",
		})
		return
	}

	keyID := c.Param("key")
	if keyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "missing_key",
			"message": "API key ID is required",
		})
		return
	}

	perm := cache.Get(keyID)
	if perm == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "not_found",
			"message": "Key permission not found",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"key_permission": perm})
}

// PutKeyPermission creates or replaces a key permission
// managed api endpoint
func (h *Handler) PutKeyPermission(c *gin.Context) {
	cache := getKeyPermissionCache(c)
	if cache == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "key_permission_cache_not_available",
			"message": "Key permission cache not initialized",
		})
		return
	}

	keyID := c.Param("key")
	if keyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "missing_key",
			"message": "API key ID is required",
		})
		return
	}

	var perm store.KeyPermission
	if err := c.ShouldBindJSON(&perm); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_json",
			"message": err.Error(),
		})
		return
	}

	// Ensure KeyID matches URL parameter
	perm.KeyID = keyID

	if err := cache.Set(&perm); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "save_failed",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"key_permission": perm,
	})
}

// PatchKeyPermission partially updates a key permission
// managed api endpoint
func (h *Handler) PatchKeyPermission(c *gin.Context) {
	cache := getKeyPermissionCache(c)
	if cache == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "key_permission_cache_not_available",
			"message": "Key permission cache not initialized",
		})
		return
	}

	keyID := c.Param("key")
	if keyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "missing_key",
			"message": "API key ID is required",
		})
		return
	}

	// Get existing permission
	existing := cache.Get(keyID)
	if existing == nil {
		c.JSON(http.StatusNotFound, gin.H{
			"error":   "not_found",
			"message": "Key permission not found",
		})
		return
	}

	// Parse patch request
	var patch struct {
		Models         []string `json:"models,omitempty"`
		ExcludedModels []string `json:"excluded_models,omitempty"`
		Label          string   `json:"label,omitempty"`
	}
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "invalid_json",
			"message": err.Error(),
		})
		return
	}

	// Apply patches
	if patch.Models != nil {
		existing.Models = patch.Models
	}
	if patch.ExcludedModels != nil {
		existing.ExcludedModels = patch.ExcludedModels
	}
	if patch.Label != "" {
		existing.Label = patch.Label
	}

	if err := cache.Set(existing); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "save_failed",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"ok":             true,
		"key_permission": existing,
	})
}

// DeleteKeyPermission deletes a key permission
// managed api endpoint
func (h *Handler) DeleteKeyPermission(c *gin.Context) {
	cache := getKeyPermissionCache(c)
	if cache == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"error":   "key_permission_cache_not_available",
			"message": "Key permission cache not initialized",
		})
		return
	}

	keyID := c.Param("key")
	if keyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error":   "missing_key",
			"message": "API key ID is required",
		})
		return
	}

	if err := cache.Delete(keyID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error":   "delete_failed",
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// getKeyPermissionCache retrieves the KeyPermissionCache from the gin context
// This function should be set by the server during middleware setup
func getKeyPermissionCache(c *gin.Context) *store.KeyPermissionCache {
	if val, exists := c.Get("keyPermissionCache"); exists {
		if cache, ok := val.(*store.KeyPermissionCache); ok {
			return cache
		}
	}
	return nil
}
