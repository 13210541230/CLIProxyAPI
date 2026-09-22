package executor

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCodexCredsGatewayResolution(t *testing.T) {
	const gateway = "https://codex-relay.example.com/backend-api/codex"
	const explicit = "https://explicit.example.com/backend-api/codex"

	e := NewCodexExecutor(&config.Config{Codex: config.CodexConfig{BaseURL: gateway}})

	// A per-credential base-url always wins over the gateway.
	withAttr := &coreauth.Auth{Attributes: map[string]string{"base_url": explicit}}
	if _, got := e.codexCreds(withAttr); got != explicit {
		t.Fatalf("explicit base-url = %q, want %q", got, explicit)
	}

	// Credentials without a base-url fall back to the configured gateway.
	bare := &coreauth.Auth{}
	if _, got := e.codexCreds(bare); got != gateway {
		t.Fatalf("gateway fallback = %q, want %q", got, gateway)
	}

	// OAuth-style credentials (token from metadata) use the gateway too.
	oauth := &coreauth.Auth{Metadata: map[string]any{"access_token": "tok"}}
	if key, got := e.codexCreds(oauth); key != "tok" || got != gateway {
		t.Fatalf("oauth creds = (%q, %q), want token with gateway base", key, got)
	}

	// With no gateway configured the base stays empty so call sites apply the
	// official https://chatgpt.com/backend-api/codex default.
	defaults := NewCodexExecutor(&config.Config{})
	if _, got := defaults.codexCreds(&coreauth.Auth{}); got != "" {
		t.Fatalf("empty gateway must leave baseURL empty, got %q", got)
	}
}
