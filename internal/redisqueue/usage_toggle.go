package redisqueue

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"sync/atomic"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
)

var usageStatisticsEnabled atomic.Bool
var usageExemptKeyHashes atomic.Value

func init() {
	usageStatisticsEnabled.Store(true)
	usageExemptKeyHashes.Store(map[string]struct{}{})
}

// SetUsageStatisticsEnabled toggles whether usage records are enqueued into the redisqueue payload buffer.
// This is controlled by the config field `usage-statistics-enabled` and the corresponding management API.
func SetUsageStatisticsEnabled(enabled bool) { usageStatisticsEnabled.Store(enabled) }

// UsageStatisticsEnabled reports whether the usage queue plugin should publish records.
func UsageStatisticsEnabled() bool { return usageStatisticsEnabled.Load() }

// SetUsageExemptKeyHashes replaces the configured client-key exemptions.
func SetUsageExemptKeyHashes(hashes []string) {
	exempt := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		exempt[strings.ToLower(strings.TrimSpace(hash))] = struct{}{}
	}
	if hash := strings.TrimSpace(os.Getenv(sdkaccess.UnmonitoredAPIKeyHashEnv)); hash != "" {
		exempt[strings.ToLower(hash)] = struct{}{}
	}
	usageExemptKeyHashes.Store(exempt)
}

func usageKeyExempt(key string) bool {
	exempt := usageExemptKeyHashes.Load().(map[string]struct{})
	if key == "" || len(exempt) == 0 {
		return false
	}
	sum := sha256.Sum256([]byte(key))
	_, ok := exempt[hex.EncodeToString(sum[:])]
	return ok
}
