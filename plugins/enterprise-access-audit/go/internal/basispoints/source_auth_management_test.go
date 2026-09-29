package basispoints

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/state"
)

func newBasispointsManagementState(t *testing.T) *state.Manager {
	t.Helper()
	root := t.TempDir()
	cfg, err := config.Normalize(config.Default(), root)
	if err != nil {
		t.Fatalf("config.Normalize() error = %v", err)
	}
	cfg.DatabasePath = filepath.Join(root, "enterprise.sqlite")
	manager := state.New()
	if err := manager.Configure(context.Background(), cfg); err != nil {
		t.Fatalf("Configure() error = %v", err)
	}
	t.Cleanup(func() { _ = manager.Shutdown() })
	return manager
}

func TestSourceAuthSettingsPersistDatabaseFlagWithoutReadingOrChangingCredential(t *testing.T) {
	root := t.TempDir()
	authPath := filepath.Join(root, "pilot.json")
	authJSON := []byte(`{"type":"codex","access_token":"unchanged"}`)
	if err := os.WriteFile(authPath, authJSON, 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	service := NewService()
	service.SetState(newBasispointsManagementState(t))
	service.SetHost(func(method string, _ any, out any) error {
		if method != "host.auth.list" {
			t.Fatalf("unexpected host callback %q; management must not fetch credential JSON", method)
		}
		return json.Unmarshal(jsonBytes(map[string]any{"files": []sourceAuthEntry{{
			Index: "stable-auth-index", Name: "pilot.json", Provider: AuthProviderID, Path: authPath,
		}}}), out)
	})

	listed, err := service.listSourceAuthSettings("")
	if err != nil {
		t.Fatalf("listSourceAuthSettings() error = %v", err)
	}
	files := listed.(map[string]any)["files"].([]map[string]any)
	if len(files) != 1 || files[0]["basispoints_enabled"] != false {
		t.Fatalf("default settings = %#v, want one disabled account", files)
	}

	_, err = service.saveSourceAuthSettings(sourceAuthManagementRequest{
		Body: jsonBytes(map[string]any{"auth_index": "stable-auth-index", "basispoints_enabled": true}),
	})
	if err != nil {
		t.Fatalf("enable Basis Points: %v", err)
	}
	listed, err = service.listSourceAuthSettings("")
	if err != nil {
		t.Fatalf("list enabled settings: %v", err)
	}
	files = listed.(map[string]any)["files"].([]map[string]any)
	if files[0]["basispoints_enabled"] != true {
		t.Fatalf("enabled settings = %#v", files[0])
	}

	if _, err := service.saveSourceAuthSettings(sourceAuthManagementRequest{
		Body: jsonBytes(map[string]any{"auth_index": "stable-auth-index", "basispoints_enabled": false}),
	}); err != nil {
		t.Fatalf("disable Basis Points: %v", err)
	}
	actual, err := os.ReadFile(authPath)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if string(actual) != string(authJSON) {
		t.Fatalf("auth file changed during endpoint configuration: %s", actual)
	}
}

func TestListSourceAuthSettingsExcludesSyntheticCodexRecords(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	if err := os.WriteFile(realPath, []byte(`{"type":"codex"}`), 0600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	service := NewService()
	service.SetState(newBasispointsManagementState(t))
	service.SetHost(func(method string, _ any, out any) error {
		if method != "host.auth.list" {
			t.Fatalf("unexpected host callback: %s", method)
		}
		return json.Unmarshal(jsonBytes(map[string]any{"files": []sourceAuthEntry{
			{Index: "synthetic", Name: "codex-api.json", Provider: AuthProviderID},
			{Index: "missing", Name: "missing.json", Provider: AuthProviderID},
			{Index: "real", Name: "real.json", Provider: AuthProviderID, Path: realPath},
		}}), out)
	})

	result, err := service.listSourceAuthSettings("")
	if err != nil {
		t.Fatalf("listSourceAuthSettings() error = %v", err)
	}
	files := result.(map[string]any)["files"].([]map[string]any)
	if len(files) != 1 || files[0]["name"] != "real.json" {
		t.Fatalf("files = %#v, want only real source auth", files)
	}
}
