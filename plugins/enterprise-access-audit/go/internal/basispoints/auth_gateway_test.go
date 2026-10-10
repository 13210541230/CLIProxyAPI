package basispoints

import "testing"

func TestNativeCodexAuthPreservesExplicitGatewayAttribute(t *testing.T) {
	raw := []byte(`{"type":"codex","access_token":"synthetic","account_id":"account","base_url":"http://127.0.0.1:18400"}`)
	c, err := parseCredential(raw)
	if err != nil {
		t.Fatal(err)
	}
	auth, err := nativeCodexAuthData(raw, "source.json", c)
	if err != nil {
		t.Fatal(err)
	}
	attributes, ok := auth["Attributes"].(map[string]string)
	if !ok || attributes["base_url"] != "http://127.0.0.1:18400" {
		t.Fatalf("plugin native parse must preserve the source gateway: %#v", auth)
	}
	if auth["ID"] != "source.json" || auth["Provider"] != "codex" {
		t.Fatalf("gateway propagation must not change account identity: %#v", auth)
	}
}
