package cyberpolicy

import (
	"strings"
	"testing"
)

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "direct error", text: `{"error":{"code":"cyber_policy"}}`, want: true},
		{name: "nested response error", text: `{"response":{"error":{"code":"cyber_policy"}}}`, want: true},
		{name: "prefixed json", text: `upstream response: {"error":{"code":"cyber_policy","message":"blocked"}}`, want: true},
		{name: "case sensitive code", text: `{"error":{"code":"CYBER_POLICY"}}`, want: false},
		{name: "code with whitespace", text: `{"error":{"code":" cyber_policy "}}`, want: false},
		{name: "nested response despite other top-level error", text: `{"error":{"code":"invalid_request"},"response":{"error":{"code":"cyber_policy"}}}`, want: true},
		{name: "other code", text: `{"error":{"code":"invalid_request"}}`, want: false},
		{name: "generic text", text: "cyber_policy was mentioned", want: false},
		{name: "nested mention only", text: `{"error":{"message":"{\"code\":\"cyber_policy\"}"}}`, want: false},
		{name: "empty", text: "", want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := Detect(test.text); got != test.want {
				t.Fatalf("Detect(%q) = %v, want %v", test.text, got, test.want)
			}
		})
	}
}

func TestExtractSanitizesAndBoundsMessage(t *testing.T) {
	message, detected := Extract(`{"error":{"code":"cyber_policy","message":"line1\nline2"}}`)
	if !detected || message != "line1 line2" {
		t.Fatalf("Extract() = (%q, %v)", message, detected)
	}
	longMessage := strings.Repeat("x", MaxMessageBytes+100)
	message, detected = Extract(`{"error":{"code":"cyber_policy","message":"` + longMessage + `"}}`)
	if !detected || len([]byte(message)) != MaxMessageBytes {
		t.Fatalf("bounded Extract() = (%d, %v)", len([]byte(message)), detected)
	}
}
