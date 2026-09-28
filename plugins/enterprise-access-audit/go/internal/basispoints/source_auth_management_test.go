package basispoints

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestListSourceAuthSettingsExcludesSyntheticCodexRecords(t *testing.T) {
	dir := t.TempDir()
	realPath := filepath.Join(dir, "real.json")
	raw := []byte(`{"type":"codex","access_token":"token","account_id":"account"}`)
	if err := os.WriteFile(realPath, raw, 0600); err != nil {
		t.Fatalf("write source auth: %v", err)
	}

	service := NewService()
	service.authDir = dir
	service.SetHost(func(method string, _ any, out any) error {
		switch method {
		case "host.auth.list":
			return json.Unmarshal(jsonBytes(map[string]any{"files": []sourceAuthEntry{
				{Index: "synthetic", Name: "codex-api.json", Provider: AuthProviderID},
				{Index: "missing", Name: "missing.json", Provider: AuthProviderID, Path: filepath.Join(dir, "missing.json")},
				{Index: "real", Name: "real.json", Provider: AuthProviderID, Path: realPath},
			}}), out)
		case "host.auth.get":
			return json.Unmarshal(jsonBytes(sourceAuthJSON{Name: "real.json", Path: realPath, JSON: raw}), out)
		default:
			t.Fatalf("unexpected host callback: %s", method)
			return nil
		}
	})

	result, err := service.listSourceAuthSettings("")
	if err != nil {
		t.Fatalf("listSourceAuthSettings() error = %v", err)
	}
	payload, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("result = %#v, want object", result)
	}
	files, ok := payload["files"].([]map[string]any)
	if !ok {
		t.Fatalf("files = %#v, want file list", payload["files"])
	}
	if len(files) != 1 || files[0]["name"] != "real.json" {
		t.Fatalf("files = %#v, want only real source auth", files)
	}
}
