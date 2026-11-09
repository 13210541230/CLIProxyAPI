package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageExemptKeysAtStartupAndReload(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "usage-exemption-test")
	hash := sha256.Sum256([]byte("exempt-key"))
	cfg := &config.Config{UsageExemptAPIKeySHA256: []string{hex.EncodeToString(hash[:])}}
	server := newTestServerWithConfig(t, cfg)
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		redisqueue.SetUsageExemptKeyHashes(nil)
		redisqueue.SetEnabled(false)
	})

	check := func(exempt bool) {
		t.Helper()
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(true)
		events, unsubscribe := redisqueue.SubscribeUsage()
		defer unsubscribe()
		<-events // support refresh marker

		coreusage.PublishRecord(coreusage.WithAPIKey(context.Background(), "exempt-key"), coreusage.Record{Model: "dedicated"})
		coreusage.PublishRecord(coreusage.WithAPIKey(context.Background(), "normal-key"), coreusage.Record{Model: "normal"})
		want := []string{"dedicated", "normal"}
		if exempt {
			want = []string{"normal"}
		}
		for _, model := range want {
			select {
			case data := <-events:
				var event struct {
					Model string `json:"model"`
				}
				if err := json.Unmarshal(data, &event); err != nil || event.Model != model {
					t.Fatalf("event = %s, want model %s: %v", data, model, err)
				}
			case <-time.After(5 * time.Second):
				t.Fatalf("no usage event for %s", model)
			}
		}
	}

	check(true)
	next := server.cfg.CloneForRuntime()
	next.UsageExemptAPIKeySHA256 = nil
	server.UpdateClients(next)
	redisqueue.SetUsageStatisticsEnabled(true)
	check(false)
	next.UsageExemptAPIKeySHA256 = cfg.UsageExemptAPIKeySHA256
	server.UpdateClients(next)
	redisqueue.SetUsageStatisticsEnabled(true)
	check(true)
}
