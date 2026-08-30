package securitysignal

import "testing"

func TestDetectExplicitCyberPolicyCode(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "top level JSON", text: `{"error":{"code":"cyber_policy"}}`, want: true},
		{name: "nested response JSON", text: `{"response":{"error":{"code":"cyber_policy"}}}`, want: true},
		{name: "wrapped JSON", text: `upstream request failed: {"error":{"code":"cyber_policy"}}`, want: true},
		{name: "other code", text: `{"error":{"code":"rate_limit"}}`, want: false},
		{name: "case variant", text: `{"error":{"code":"CYBER_POLICY"}}`, want: false},
		{name: "padded code", text: `{"error":{"code":" cyber_policy "}}`, want: false},
		{name: "message mention", text: `{"error":{"code":"bad_request","message":"cyber_policy"}}`, want: false},
		{name: "generic status", text: "upstream returned HTTP 400", want: false},
		{name: "malformed JSON", text: `prefix {"error":`, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Detect(tt.text); got != tt.want {
				t.Fatalf("Detect(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
