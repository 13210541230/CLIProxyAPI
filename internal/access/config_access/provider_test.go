package configaccess

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"testing"

	sdkaccess "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestPrivateKeyEnvironmentAuthentication(t *testing.T) {
	hash := sha256.Sum256([]byte("private-key"))
	t.Setenv("CPA_UNMONITORED_API_KEY_SHA256", hex.EncodeToString(hash[:]))
	t.Cleanup(func() { sdkaccess.UnregisterProvider(sdkaccess.AccessProviderTypeConfigAPIKey) })
	manager := sdkaccess.NewManager()

	apply := func(keys []string) {
		Register(&sdkconfig.SDKConfig{APIKeys: keys})
		manager.SetProviders(sdkaccess.RegisteredProviders())
	}
	check := func(key string, allowed bool) {
		t.Helper()
		req := httptest.NewRequest("GET", "/v1/models", nil)
		if key != "" {
			req.Header.Set("Authorization", "Bearer "+key)
		}
		result, err := manager.Authenticate(context.Background(), req)
		if allowed && (err != nil || result == nil || result.Principal != key) {
			t.Fatalf("key %q: result=%v error=%v", key, result, err)
		}
		if !allowed && err == nil {
			t.Fatalf("key %q unexpectedly allowed: result=%v", key, result)
		}
	}

	apply(nil)
	check("", false)
	check("wrong", false)
	check("private-key", true)
	apply([]string{"normal-key"})
	check("normal-key", true)
	check("private-key", true)
	t.Setenv("CPA_UNMONITORED_API_KEY_SHA256", "invalid")
	apply(nil)
	check("", false)
	check("private-key", false)
	apply([]string{"normal-key"})
	check("normal-key", true)
	check("private-key", false)
}
