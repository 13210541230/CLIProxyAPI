package clienterror

import "testing"

func TestExtractUpstreamErrorDetails(t *testing.T) {
	got := ExtractUpstreamErrorDetails(`upstream: {"error":{"type":"server_error","code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."}}`)
	if got.Code != "server_is_overloaded" || got.Type != "server_error" {
		t.Fatalf("details = %#v", got)
	}
	if got.Summary != "server_is_overloaded: Our servers are currently overloaded. Please try again later." {
		t.Fatalf("summary = %q", got.Summary)
	}
}

func TestExtractUpstreamErrorDetailsRedactsSecrets(t *testing.T) {
	got := ExtractUpstreamErrorDetails(`{"error":{"code":"bad_request","message":"authorization: Bearer abc123 sk-secret-value"}}`)
	if got.Summary == "" || got.Summary == "authorization: Bearer abc123 sk-secret-value" {
		t.Fatalf("summary was not sanitized: %q", got.Summary)
	}
}
