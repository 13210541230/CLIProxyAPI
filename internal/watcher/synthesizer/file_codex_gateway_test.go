package synthesizer

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestFileSynthesizer_Synthesize_CodexBaseURLFromAuthFile(t *testing.T) {
	tempDir := t.TempDir()
	authData := map[string]any{
		"type":         "codex",
		"access_token": "codex-token",
		"base_url":     "https://codex-relay.example.com/backend-api/codex",
	}
	data, errMarshal := json.Marshal(authData)
	if errMarshal != nil {
		t.Fatalf("marshal codex auth: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filepath.Join(tempDir, "codex-auth.json"), data, 0644); errWrite != nil {
		t.Fatalf("write codex auth file: %v", errWrite)
	}

	auths, err := NewFileSynthesizer().Synthesize(&SynthesisContext{
		Config:  &config.Config{},
		AuthDir: tempDir,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth, got %d", len(auths))
	}
	if auths[0].Provider != "codex" {
		t.Fatalf("provider = %q, want codex", auths[0].Provider)
	}
	if got := auths[0].Attributes["base_url"]; got != "https://codex-relay.example.com/backend-api/codex" {
		t.Fatalf("base_url = %q, want relay gateway from auth file", got)
	}
}
