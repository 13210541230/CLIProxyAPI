package cyberpolicy

import "testing"

func TestDetect(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "direct error", text: `{"error":{"code":"cyber_policy"}}`, want: true},
		{name: "nested response error", text: `{"response":{"error":{"code":"cyber_policy"}}}`, want: true},
		{name: "prefixed json", text: `upstream response: {"error":{"code":"cyber_policy","message":"blocked"}}`, want: true},
		{name: "case insensitive code", text: `{"error":{"code":"CYBER_POLICY"}}`, want: true},
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
