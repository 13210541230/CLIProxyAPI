package basispoints

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSourceAuthSettingsRepairsLegacyBasisPointsType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pilot.json")
	if err := os.WriteFile(path, []byte(`{"type":"oai-basispoints","endpoint":"basispoints","access_token":"token"}`), 0600); err != nil {
		t.Fatalf("write source auth: %v", err)
	}

	service := NewService()
	service.authDir = dir
	service.SetHost(func(method string, _ any, out any) error {
		if method != "host.auth.list" {
			t.Fatalf("unexpected host callback: %s", method)
		}
		return json.Unmarshal(jsonBytes(map[string]any{"files": []sourceAuthEntry{{
			Index: "pilot", Name: "pilot.json", Provider: Provider, Path: path,
		}}}), out)
	})

	_, err := service.saveSourceAuthSettings(sourceAuthManagementRequest{
		Body: jsonBytes(map[string]any{"auth_index": "pilot", "endpoint": EndpointBasisPoint}),
	})
	if err != nil {
		t.Fatalf("saveSourceAuthSettings() error = %v", err)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read source auth: %v", err)
	}
	var fields map[string]any
	if err := json.Unmarshal(saved, &fields); err != nil {
		t.Fatalf("decode source auth: %v", err)
	}
	if fields["type"] != AuthProviderID {
		t.Fatalf("source type = %v, want %q", fields["type"], AuthProviderID)
	}
}

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
