package api

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestPrivateEnvironmentKeyStaysOutOfManagementAndUsage(t *testing.T) {
	const privateKey, normalKey, managementKey = "private-key", "normal-key", "usage-exemption-management"
	hash := sha256.Sum256([]byte(privateKey))
	digest := hex.EncodeToString(hash[:])
	t.Setenv("CPA_UNMONITORED_API_KEY_SHA256", digest)
	t.Setenv("MANAGEMENT_PASSWORD", managementKey)
	cfg := &config.Config{}
	cfg.APIKeys = []string{normalKey}
	server := newTestServerWithConfig(t, cfg)
	if err := os.WriteFile(server.configFilePath, []byte("access:\n  api-keys: [normal-key]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() {
		_ = os.Unsetenv("CPA_UNMONITORED_API_KEY_SHA256")
		redisqueue.SetUsageExemptKeyHashes(nil)
		redisqueue.SetEnabled(false)
	})

	request := func(path, key string) *httptest.ResponseRecorder {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		response := httptest.NewRecorder()
		server.engine.ServeHTTP(response, req)
		return response
	}
	for _, tc := range []struct {
		key    string
		status int
	}{
		{privateKey, http.StatusOK}, {normalKey, http.StatusOK}, {"wrong-key", http.StatusUnauthorized}, {"", http.StatusUnauthorized},
	} {
		if response := request("/v1/models", tc.key); response.Code != tc.status {
			t.Fatalf("models key %q: status %d, want %d", tc.key, response.Code, tc.status)
		}
	}
	for _, path := range []string{"/v0/management/api-keys", "/v0/management/config", "/v0/management/config.yaml"} {
		response := request(path, managementKey)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), privateKey) || strings.Contains(response.Body.String(), digest) {
			t.Fatalf("management %s: status %d, body %s", path, response.Code, response.Body.String())
		}
	}

	events, unsubscribe := redisqueue.SubscribeUsage()
	defer unsubscribe()
	<-events
	coreusage.PublishRecord(coreusage.WithAPIKey(context.Background(), privateKey), coreusage.Record{Model: "private"})
	coreusage.PublishRecord(coreusage.WithAPIKey(context.Background(), normalKey), coreusage.Record{Model: "normal"})
	select {
	case data := <-events:
		var event struct {
			Model string `json:"model"`
		}
		if err := json.Unmarshal(data, &event); err != nil || event.Model != "normal" {
			t.Fatalf("usage event = %s, want normal: %v", data, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("normal usage event not received")
	}
}

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
