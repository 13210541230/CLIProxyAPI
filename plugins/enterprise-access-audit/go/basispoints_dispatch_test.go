package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
)

func TestBasispointsDispatchUsesSourceAuthIDAndLogicalModel(t *testing.T) {
	root := t.TempDir()
	t.Cleanup(func() {
		_, _ = handleMethod("plugin.shutdown", nil)
		pluginState = state.New()
	})
	request := lifecycleRequest{ConfigYAML: []byte("data_dir: " + filepath.ToSlash(root) + "\n"), SchemaVersion: 2}
	rawRequest, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal registration: %v", err)
	}
	if _, err := handleMethod("plugin.register", rawRequest); err != nil {
		t.Fatalf("register: %v", err)
	}
	registrationRaw, err := handleMethod("model.register", nil)
	if err != nil {
		t.Fatalf("model.register: %v", err)
	}
	var registrationEnvelope envelope
	if err := json.Unmarshal(registrationRaw, &registrationEnvelope); err != nil || !registrationEnvelope.OK {
		t.Fatalf("model.register response = %s, error=%v", registrationRaw, err)
	}
	var modelResult struct {
		Provider string
		Models   []struct {
			ID string
		}
	}
	if err := json.Unmarshal(registrationEnvelope.Result, &modelResult); err != nil {
		t.Fatalf("decode model registration: %v", err)
	}
	if modelResult.Provider != "oai-basispoints" || len(modelResult.Models) != 2 || modelResult.Models[0].ID != "gpt-6-astra" || modelResult.Models[1].ID != "gpt-5.6-sol" {
		t.Fatalf("model registration = %+v", modelResult)
	}

	authPayload, err := json.Marshal(map[string]any{"access_token": "token", "account_id": "acct", "endpoint": "basispoints"})
	if err != nil {
		t.Fatalf("marshal auth payload: %v", err)
	}
	authRequest, err := json.Marshal(map[string]any{"Provider": "codex", "FileName": "pilot.json", "RawJSON": authPayload})
	if err != nil {
		t.Fatalf("marshal auth request: %v", err)
	}
	authRaw, err := handleMethod("auth.parse", authRequest)
	if err != nil {
		t.Fatalf("auth.parse: %v", err)
	}
	if !json.Valid(authRaw) || !containsJSON(authRaw, `"ID":"pilot.json"`) || !containsJSON(authRaw, `"Provider":"codex"`) || containsJSON(authRaw, `"Provider":"oai-basispoints"`) {
		t.Fatalf("auth.parse response = %s", authRaw)
	}
}

func containsJSON(raw []byte, want string) bool {
	return len(raw) > 0 && string(raw) != "" && strings.Contains(string(raw), want)
}
