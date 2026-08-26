package cyberpolicy

import (
	"encoding/json"
	"strings"
	"unicode"
)

const (
	UpstreamCyberPolicyCode = "cyber_policy"
	MaxMessageBytes         = 4096
)

type errorEnvelope struct {
	Error    *errorObject `json:"error"`
	Response *struct {
		Error *errorObject `json:"error"`
	} `json:"response"`
}

type errorObject struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Extract reports only an explicit upstream cyber_policy error code and returns
// its bounded, control-character-free message. It accepts a raw JSON response
// or an error string containing a JSON response envelope.
func Extract(errorText string) (string, bool) {
	errorText = strings.TrimSpace(errorText)
	if errorText == "" {
		return "", false
	}
	start := strings.IndexByte(errorText, '{')
	if start < 0 {
		return "", false
	}
	var envelope errorEnvelope
	decoder := json.NewDecoder(strings.NewReader(errorText[start:]))
	if err := decoder.Decode(&envelope); err != nil {
		return "", false
	}
	if envelope.Error != nil && envelope.Error.Code == UpstreamCyberPolicyCode {
		return sanitizeMessage(envelope.Error.Message), true
	}
	if envelope.Response != nil && envelope.Response.Error != nil && envelope.Response.Error.Code == UpstreamCyberPolicyCode {
		return sanitizeMessage(envelope.Response.Error.Message), true
	}
	return "", false
}

// Detect reports whether the upstream error explicitly contains cyber_policy.
func Detect(errorText string) bool {
	_, detected := Extract(errorText)
	return detected
}

func sanitizeMessage(message string) string {
	var builder strings.Builder
	for _, char := range strings.TrimSpace(message) {
		if unicode.IsControl(char) {
			char = ' '
		}
		encoded := string(char)
		if builder.Len()+len(encoded) > MaxMessageBytes {
			break
		}
		builder.WriteString(encoded)
	}
	return strings.TrimSpace(builder.String())
}
