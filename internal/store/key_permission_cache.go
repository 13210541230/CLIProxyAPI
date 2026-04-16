package store

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// KeyPermissionCache manages API key to model permissions mapping with hot-reload support
type KeyPermissionCache struct {
	mu          sync.RWMutex
	permissions map[string]*KeyPermission // keyID -> KeyPermission
	filePath    string
	lastModTime time.Time
	lastSize    int64
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

// NewKeyPermissionCache creates a new cache instance with the given permissions file path
func NewKeyPermissionCache(filePath string) *KeyPermissionCache {
	return &KeyPermissionCache{
		permissions: make(map[string]*KeyPermission),
		filePath:    filePath,
		stopCh:      make(chan struct{}),
	}
}

// Load loads permissions from the file
func (c *KeyPermissionCache) Load() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if file exists
	info, err := os.Stat(c.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist yet, clear cache
			c.permissions = make(map[string]*KeyPermission)
			c.lastModTime = time.Time{}
			c.lastSize = 0
			return nil
		}
		return fmt.Errorf("key permission cache: stat file: %w", err)
	}

	// Check if file changed
	if info.ModTime().Equal(c.lastModTime) && info.Size() == c.lastSize {
		return nil // No change
	}

	// Read file
	data, err := os.ReadFile(c.filePath)
	if err != nil {
		return fmt.Errorf("key permission cache: read file: %w", err)
	}

	// Parse JSON
	var perms []*KeyPermission
	if err := json.Unmarshal(data, &perms); err != nil {
		return fmt.Errorf("key permission cache: unmarshal JSON: %w", err)
	}

	// Update cache
	newPerms := make(map[string]*KeyPermission)
	for _, p := range perms {
		if p.KeyID != "" {
			newPerms[strings.TrimSpace(p.KeyID)] = p
		}
	}
	c.permissions = newPerms
	c.lastModTime = info.ModTime()
	c.lastSize = info.Size()

	log.Infof("Key permission cache loaded: %d entries", len(c.permissions))
	return nil
}

// Save persists the current permissions to the file
func (c *KeyPermissionCache) Save() error {
	c.mu.RLock()
	perms := make([]*KeyPermission, 0, len(c.permissions))
	for _, p := range c.permissions {
		perms = append(perms, p)
	}
	c.mu.RUnlock()

	// Ensure directory exists
	dir := filepath.Dir(c.filePath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("key permission cache: create directory: %w", err)
	}

	// Marshal to JSON with indentation
	data, err := json.MarshalIndent(perms, "", "  ")
	if err != nil {
		return fmt.Errorf("key permission cache: marshal JSON: %w", err)
	}

	// Write to temp file and rename for atomic update
	tempFile := c.filePath + ".tmp"
	if err := os.WriteFile(tempFile, data, 0o600); err != nil {
		return fmt.Errorf("key permission cache: write temp file: %w", err)
	}

	if err := os.Rename(tempFile, c.filePath); err != nil {
		_ = os.Remove(tempFile)
		return fmt.Errorf("key permission cache: rename file: %w", err)
	}

	// Update mod time
	info, err := os.Stat(c.filePath)
	if err == nil {
		c.mu.Lock()
		c.lastModTime = info.ModTime()
		c.lastSize = info.Size()
		c.mu.Unlock()
	}

	return nil
}

// Get retrieves a key's permission by keyID
func (c *KeyPermissionCache) Get(keyID string) *KeyPermission {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.permissions[strings.TrimSpace(keyID)]
}

// Set adds or updates a key's permission
func (c *KeyPermissionCache) Set(perm *KeyPermission) error {
	if perm == nil || perm.KeyID == "" {
		return fmt.Errorf("key permission cache: invalid permission")
	}

	now := time.Now()
	perm.KeyID = strings.TrimSpace(perm.KeyID)
	if perm.CreatedAt.IsZero() {
		perm.CreatedAt = now
	}
	perm.UpdatedAt = now

	c.mu.Lock()
	c.permissions[perm.KeyID] = perm
	c.mu.Unlock()

	return c.Save()
}

// Delete removes a key's permission
func (c *KeyPermissionCache) Delete(keyID string) error {
	keyID = strings.TrimSpace(keyID)
	if keyID == "" {
		return fmt.Errorf("key permission cache: empty key ID")
	}

	c.mu.Lock()
	delete(c.permissions, keyID)
	c.mu.Unlock()

	return c.Save()
}

// List returns all permissions
func (c *KeyPermissionCache) List() []*KeyPermission {
	c.mu.RLock()
	defer c.mu.RUnlock()

	perms := make([]*KeyPermission, 0, len(c.permissions))
	for _, p := range c.permissions {
		perms = append(perms, p)
	}
	return perms
}

// IsModelAllowed checks if a model is allowed for a given key
func (c *KeyPermissionCache) IsModelAllowed(keyID, model string) bool {
	perm := c.Get(keyID)
	if perm == nil {
		return true // No restriction if no permission entry
	}
	return perm.IsModelAllowed(model)
}

// StartReloadLoop starts a background goroutine that periodically reloads permissions
func (c *KeyPermissionCache) StartReloadLoop(interval time.Duration) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				if err := c.Load(); err != nil {
					log.Errorf("Key permission cache reload failed: %v", err)
				}
			case <-c.stopCh:
				return
			}
		}
	}()
}

// Stop stops the reload loop
func (c *KeyPermissionCache) Stop() {
	close(c.stopCh)
	c.wg.Wait()
}

// EnsureDefault creates the permissions file if it doesn't exist
func (c *KeyPermissionCache) EnsureDefault() error {
	_, err := os.Stat(c.filePath)
	if err == nil {
		return nil // File exists
	}
	if !os.IsNotExist(err) {
		return err
	}

	// Create empty permissions file
	return c.Save()
}
