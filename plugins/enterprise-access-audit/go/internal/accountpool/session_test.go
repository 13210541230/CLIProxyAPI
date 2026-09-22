package accountpool

import "testing"

func TestSessionKeyPrefersHeadersThenCanonicalMetadata(t *testing.T) {
	withHeader := schedulerPickRequest{Options: schedulerOptions{
		Headers:  map[string][]string{"x-conversation-id": {"conv-header"}},
		Metadata: map[string]any{"canonical_session_id": "conv-canonical"},
	}}
	if got := sessionKey(withHeader); got != "conv-header" {
		t.Fatalf("sessionKey(header) = %q, want %q", got, "conv-header")
	}

	canonicalOnly := schedulerPickRequest{Options: schedulerOptions{
		Metadata: map[string]any{
			"canonical_session_id": "conv-canonical",
			"conversation_id":      "conv-legacy",
		},
	}}
	if got := sessionKey(canonicalOnly); got != "conv-canonical" {
		t.Fatalf("sessionKey(canonical) = %q, want %q", got, "conv-canonical")
	}

	legacyOnly := schedulerPickRequest{Options: schedulerOptions{
		Metadata: map[string]any{"conversation_id": "conv-legacy"},
	}}
	if got := sessionKey(legacyOnly); got != "conv-legacy" {
		t.Fatalf("sessionKey(legacy) = %q, want %q", got, "conv-legacy")
	}

	// Non-string or missing identity yields no session key.
	notAString := schedulerPickRequest{Options: schedulerOptions{
		Metadata: map[string]any{"canonical_session_id": 42},
	}}
	if got := sessionKey(notAString); got != "" {
		t.Fatalf("sessionKey(non-string) = %q, want empty", got)
	}

	empty := schedulerPickRequest{}
	if got := sessionKey(empty); got != "" {
		t.Fatalf("sessionKey(empty) = %q, want empty", got)
	}
}
