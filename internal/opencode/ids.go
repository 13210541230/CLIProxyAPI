// Package opencode implements the OpenCode (Zen Go) provider for CLIProxyAPI.
//
// Accounts are ordinary auth files whose "type" is "opencode" and which carry an
// "api_key" entry. The executor reproduces the OpenCode CLI request identity
// (User-Agent, session headers, project header) so that any downstream harness
// looks like a single OpenCode CLI session to the upstream service.
package opencode

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math/big"
	"strings"
)

const sessionIDLength = 30

// SessionIDForSignal derives a stable OpenCode session identifier from an
// arbitrary client supplied session signal. Official ses_ identifiers that
// already carry a descending time prefix are preserved verbatim; anything else
// is deterministically re-encoded so multi-turn conversations stay stable.
func SessionIDForSignal(signal string) string {
	signal = strings.TrimSpace(signal)
	if signal == "" {
		return ""
	}
	if IsSessionID(signal) && isDescendingTimePart(signal[4:16]) {
		return signal
	}
	sum := sha256.Sum256([]byte("ses\x00" + signal))
	return "ses_" + descendingTimePart(sum[:6]) + base62Fixed(new(big.Int).SetBytes(sum[6:16]), 14)
}

// IsSessionID reports whether value has the canonical OpenCode session shape.
func IsSessionID(value string) bool {
	if len(value) != sessionIDLength || !strings.HasPrefix(value, "ses_") {
		return false
	}
	for i := 4; i < len(value); i++ {
		c := value[i]
		if i < 16 {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return false
			}
			continue
		}
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z')) {
			return false
		}
	}
	return true
}

// ProjectID derives the OpenCode project header from a project signal.
func ProjectID(project string) string {
	project = strings.TrimSpace(project)
	if project == "" {
		project = "cli"
	}
	sum := sha256.Sum256([]byte("opencode-project\x00" + project))
	return hex.EncodeToString(sum[:20])
}

func isDescendingTimePart(value string) bool {
	if len(value) != 12 || value[9] != 'f' || value[10] < 'c' || value[10] > 'f' {
		return false
	}
	for i := range value {
		c := value[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

func descendingTimePart(hash []byte) string {
	current := (uint64(hash[0])<<28 | uint64(hash[1])<<20 | uint64(hash[2])<<12 |
		uint64(hash[3])<<4 | uint64(hash[4])>>4) << 12
	current |= 1
	return fmt.Sprintf("%012x", ^current&0xFFFFFFFFFFFF)
}

func base62Fixed(value *big.Int, width int) string {
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	base := big.NewInt(int64(len(alphabet)))
	result := make([]byte, width)
	for i := width - 1; i >= 0; i-- {
		remainder := new(big.Int)
		value.QuoRem(value, base, remainder)
		result[i] = alphabet[remainder.Int64()]
	}
	return string(result)
}
