package model

import "testing"

func TestNormalizeModelIDs(t *testing.T) {
	got, err := NormalizeModelIDs([]string{" GPT-4 ", "gpt-3", "gpt-4", "GPT-3"})
	if err != nil {
		t.Fatalf("NormalizeModelIDs() error = %v", err)
	}
	want := []string{"gpt-3", "gpt-4"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("NormalizeModelIDs() = %#v, want %#v", got, want)
	}
}

func TestNormalizeModelIDRejectsInternalWhitespaceAndControl(t *testing.T) {
	for _, input := range []string{"gpt 4", "gpt\n4", "\x00model"} {
		if _, err := NormalizeModelID(input); err == nil {
			t.Errorf("NormalizeModelID(%q) accepted invalid input", input)
		}
	}
}

func TestPolicyDefaultsAndExactMatching(t *testing.T) {
	policy := DefaultPolicy("abcdef12")
	if !policy.AuditEnabled {
		t.Fatal("default policy audit must be enabled")
	}
	allowed, err := policy.Allows("model")
	if err != nil || !allowed {
		t.Fatalf("empty deny list should allow models, allowed=%v err=%v", allowed, err)
	}
	policy.DeniedModels = []string{"gpt-4"}
	allowed, err = policy.Allows("GPT-4")
	if err != nil || allowed {
		t.Fatalf("normalized exact deny did not match, allowed=%v err=%v", allowed, err)
	}
	allowed, err = policy.Allows("gpt-4-mini")
	if err != nil || !allowed {
		t.Fatalf("prefix matching must not deny, allowed=%v err=%v", allowed, err)
	}
}

func TestNormalizeKeyHashRejectsRawKey(t *testing.T) {
	if _, err := NormalizeKeyHash("sk-raw-secret"); err == nil {
		t.Fatal("raw API key was accepted as a hash")
	}
	got, err := NormalizeKeyHash("ABCDEF12")
	if err != nil || got != "abcdef12" {
		t.Fatalf("NormalizeKeyHash() = %q, %v", got, err)
	}
}
