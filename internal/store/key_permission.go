package store

import (
	"strings"
	"time"
)

// KeyPermission defines API key model permissions
type KeyPermission struct {
	KeyID          string    `json:"key_id"`
	Models         []string  `json:"models"`
	ExcludedModels []string  `json:"excluded_models,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
	Label          string    `json:"label,omitempty"`
}

// IsModelAllowed checks if model is allowed for this key
func (kp *KeyPermission) IsModelAllowed(model string) bool {
	if kp == nil {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, excluded := range kp.ExcludedModels {
		if strings.ToLower(strings.TrimSpace(excluded)) == model {
			return false
		}
	}
	if len(kp.Models) > 0 {
		for _, allowed := range kp.Models {
			if strings.ToLower(strings.TrimSpace(allowed)) == model {
				return true
			}
		}
		return false
	}
	return true
}
