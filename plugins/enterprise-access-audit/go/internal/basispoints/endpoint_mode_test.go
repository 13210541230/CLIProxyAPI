package basispoints

import (
	"encoding/json"
	"testing"
)

func TestParseAuthRequestUsesConfiguredBasisPointsEndpointWithoutVirtualAuth(t *testing.T) {
	result, err := parseAuthRequest(authParseRequest{
		Provider: "codex",
		FileName: "pilot.json",
		RawJSON:  []byte(`{"access_token":"token","account_id":"acct-pilot","endpoint":"basispoints"}`),
	})
	if err != nil {
		t.Fatalf("parseAuthRequest() error = %v", err)
	}
	if handled, _ := result["Handled"].(bool); !handled {
		t.Fatal("parseAuthRequest() did not handle the Codex credential")
	}
	auth, ok := result["Auth"].(map[string]any)
	if !ok {
		t.Fatalf("result Auth = %#v, want one auth record", result["Auth"])
	}
	if got := auth["Provider"]; got != Provider {
		t.Fatalf("auth provider = %v, want %q", got, Provider)
	}
	if got := auth["ID"]; got != "pilot.json" {
		t.Fatalf("auth ID = %v, want source auth ID", got)
	}
	if _, exists := result["Auths"]; exists {
		t.Fatal("basispoints endpoint must not expand one source auth into virtual auths")
	}
}

func TestParseAuthRequestKeepsNormalCodexEndpointByDefault(t *testing.T) {
	result, err := parseAuthRequest(authParseRequest{
		Provider: "codex",
		FileName: "normal.json",
		RawJSON:  []byte(`{"access_token":"token","account_id":"acct-normal"}`),
	})
	if err != nil {
		t.Fatalf("parseAuthRequest() error = %v", err)
	}
	auth, ok := result["Auth"].(map[string]any)
	if !ok {
		t.Fatalf("normal auth = %#v, want exactly one native auth", result["Auth"])
	}
	if got := auth["Provider"]; got != AuthProviderID {
		t.Fatalf("normal auth provider = %v, want %q", got, AuthProviderID)
	}
	if got := auth["ID"]; got != "normal.json" {
		t.Fatalf("normal auth ID = %v, want source auth ID", got)
	}
}

func TestModelRegistrationUsesLogicalModelName(t *testing.T) {
	cfg := defaultConfig()
	registration := modelRegistration(cfg)
	models, ok := registration["Models"].([]map[string]any)
	if !ok || len(models) == 0 {
		t.Fatalf("model registration = %#v", registration)
	}
	if got := models[0]["ID"]; got != "gpt-6-astra" {
		t.Fatalf("registered model ID = %v, want gpt-6-astra", got)
	}
}

func TestEndpointModeSurvivesSourceAuthJSONRoundTrip(t *testing.T) {
	var fields map[string]any
	if err := json.Unmarshal([]byte(`{"type":"codex","endpoint":"openai"}`), &fields); err != nil {
		t.Fatalf("decode source auth: %v", err)
	}
	if got := endpointMode(fields); got != EndpointOpenAI {
		t.Fatalf("endpointMode() = %q, want %q", got, EndpointOpenAI)
	}
}
