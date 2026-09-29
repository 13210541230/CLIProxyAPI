package basispoints

import "testing"

func TestParseAuthRequestKeepsCodexAuthAndIgnoresLegacyEndpointField(t *testing.T) {
	raw := []byte(`{"access_token":"token","account_id":"acct-pilot","endpoint":"basispoints"}`)
	before := append([]byte(nil), raw...)
	result, err := parseAuthRequest(authParseRequest{
		Provider: "codex",
		FileName: "pilot.json",
		RawJSON:  raw,
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
	if got := auth["Provider"]; got != AuthProviderID {
		t.Fatalf("auth provider = %v, want native %q", got, AuthProviderID)
	}
	if got := auth["ID"]; got != "pilot.json" {
		t.Fatalf("auth ID = %v, want source auth ID", got)
	}
	metadata := auth["Metadata"].(map[string]any)
	if _, exists := metadata["endpoint"]; exists {
		t.Fatalf("legacy endpoint marker leaked into runtime metadata: %#v", metadata["endpoint"])
	}
	if string(raw) != string(before) {
		t.Fatalf("auth parser modified the source bytes: %s", raw)
	}
	if _, exists := result["Auths"]; exists {
		t.Fatal("one source auth must not expand into virtual auths")
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

func TestModelRegistrationUsesBPSProviderAndLogicalModelNames(t *testing.T) {
	cfg := defaultConfig()
	registration := modelRegistration(cfg)
	if got := registration["Provider"]; got != Provider {
		t.Fatalf("registered provider = %v, want %q", got, Provider)
	}
	models, ok := registration["Models"].([]map[string]any)
	if !ok || len(models) == 0 {
		t.Fatalf("model registration = %#v", registration)
	}
	registered := make(map[string]bool, len(models))
	for _, model := range models {
		registered[model["ID"].(string)] = true
		if got := model["OwnedBy"]; got != Provider {
			t.Errorf("model %v owned by %v, want %q", model["ID"], got, Provider)
		}
	}
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		if !registered[model] {
			t.Errorf("logical model %q not registered", model)
		}
	}
}

func TestModelForAuthAugmentsNativeCodexModels(t *testing.T) {
	service := NewService()
	result, err := service.Handle("model.for_auth", []byte(`{"AuthID":"pilot.json","AuthProvider":"codex","AuthKind":"oauth"}`))
	if err != nil {
		t.Fatalf("Handle(model.for_auth) error = %v", err)
	}
	registration, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("auth model registration = %#v", result)
	}
	if registration["Provider"] != AuthProviderID || registration["Augment"] != true {
		t.Fatalf("auth model registration = %#v, want additive Codex models", registration)
	}
	models, ok := registration["Models"].([]map[string]any)
	if !ok || len(models) != 2 {
		t.Fatalf("auth models = %#v, want both BPS-capable models", registration["Models"])
	}
}

func TestModelForAuthDoesNotAugmentNonOAuthCodexCredentials(t *testing.T) {
	service := NewService()
	result, err := service.Handle("model.for_auth", []byte(`{"AuthID":"key-auth","AuthProvider":"codex","AuthKind":"apikey"}`))
	if err != nil {
		t.Fatalf("Handle(model.for_auth) error = %v", err)
	}
	registration, ok := result.(map[string]any)
	if !ok || registration["Provider"] != Provider || registration["Augment"] == true {
		t.Fatalf("auth model registration = %#v, want no Codex augmentation", result)
	}
}

func TestModelRouterTargetsCodexForSupportedLogicalModel(t *testing.T) {
	service := NewService()
	result, err := service.Handle("model.route", []byte(`{"RequestedModel":"gpt-5.6-sol"}`))
	if err != nil {
		t.Fatalf("Handle(model.route) error = %v", err)
	}
	route, ok := result.(map[string]any)
	if !ok {
		t.Fatalf("model route = %#v", result)
	}
	if route["Handled"] != true || route["TargetKind"] != "provider" || route["Target"] != AuthProviderID || route["TargetModel"] != "gpt-5.6-sol" {
		t.Fatalf("model route = %#v, want Codex provider with unchanged logical model", route)
	}
}

func TestModelRouterLeavesUnsupportedModelsUnchanged(t *testing.T) {
	service := NewService()
	result, err := service.Handle("model.route", []byte(`{"RequestedModel":"gpt-4.1"}`))
	if err != nil {
		t.Fatalf("Handle(model.route) error = %v", err)
	}
	route, ok := result.(map[string]any)
	if !ok || route["Handled"] != false {
		t.Fatalf("model route = %#v, want unhandled", result)
	}
}

func TestDefaultConfigSupportsBothBasisPointsModels(t *testing.T) {
	cfg := defaultConfig()
	for _, model := range []string{"gpt-5.6-sol", "gpt-6-astra"} {
		if !basispointsModelSupported(model, cfg) {
			t.Errorf("Basis Points model %q is not enabled by default", model)
		}
	}
	if upstream, ok := cfg.upstreamModelForAlias("gpt-5.6-sol"); !ok || upstream != "gpt-5.6-sol" {
		t.Fatalf("gpt-5.6-sol upstream mapping = %q, %v", upstream, ok)
	}
}
