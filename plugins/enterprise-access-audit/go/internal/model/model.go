package model

import (
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const KeyHashLength = 8

// Policy is the normalized per-enterprise-key policy persisted by the plugin.
type Policy struct {
	KeyHash      string
	DeniedModels []string
	AuditEnabled bool
	UpdatedAt    int64
}

// PolicyPatch updates only the fields whose pointers are non-nil.
type PolicyPatch struct {
	KeyHash      string
	DeniedModels *[]string
	AuditEnabled *bool
}

// NormalizeKeyHash accepts only the short canonical hash carried by CPA metadata.
func NormalizeKeyHash(input string) (string, error) {
	value := strings.ToLower(strings.TrimSpace(input))
	if len(value) != KeyHashLength {
		return "", fmt.Errorf("api-key hash must be %d hexadecimal characters", KeyHashLength)
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return "", fmt.Errorf("api-key hash must be hexadecimal")
		}
	}
	return value, nil
}

// NormalizeModelID applies the shared exact-match normalization used by writes and reads.
func NormalizeModelID(input string) (string, error) {
	value := strings.TrimSpace(input)
	if value == "" {
		return "", fmt.Errorf("model ID must not be empty")
	}
	for _, char := range value {
		if unicode.IsControl(char) || unicode.IsSpace(char) {
			return "", fmt.Errorf("model ID must not contain control or whitespace characters")
		}
	}
	var builder strings.Builder
	builder.Grow(len(value))
	for _, char := range value {
		if char >= 'A' && char <= 'Z' {
			char += 'a' - 'A'
		}
		builder.WriteRune(char)
	}
	return builder.String(), nil
}

// NormalizeModelIDs returns a deterministic sorted set and rejects invalid entries.
func NormalizeModelIDs(input []string) ([]string, error) {
	if len(input) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(input))
	result := make([]string, 0, len(input))
	for _, raw := range input {
		value, errNormalize := NormalizeModelID(raw)
		if errNormalize != nil {
			return nil, errNormalize
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

// Allows reports whether an exact normalized model ID is not denied.
func (p Policy) Allows(modelID string) (bool, error) {
	model, errNormalize := NormalizeModelID(modelID)
	if errNormalize != nil {
		return false, errNormalize
	}
	for _, denied := range p.DeniedModels {
		if denied == model {
			return false, nil
		}
	}
	return true, nil
}

// DefaultPolicy returns the fail-open model policy with auditing enabled.
func DefaultPolicy(keyHash string) Policy {
	return Policy{KeyHash: keyHash, AuditEnabled: true}
}
