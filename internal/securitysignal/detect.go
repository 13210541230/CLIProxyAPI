package securitysignal

import (
	"encoding/json"
	"strings"
)

const CyberPolicy = "cyber_policy"

type errorEnvelope struct {
	Error    *errorObject `json:"error"`
	Response *struct {
		Error *errorObject `json:"error"`
	} `json:"response"`
}

type errorObject struct {
	Code string `json:"code"`
}

// Detect reports whether an error string contains an explicit upstream
// cyber_policy code in the top-level or nested response error envelope.
func Detect(errorText string) bool {
	errorText = strings.TrimSpace(errorText)
	if errorText == "" {
		return false
	}
	start := strings.IndexByte(errorText, '{')
	if start < 0 {
		return false
	}
	var envelope errorEnvelope
	if err := json.NewDecoder(strings.NewReader(errorText[start:])).Decode(&envelope); err != nil {
		return false
	}
	return envelope.Error != nil && envelope.Error.Code == CyberPolicy ||
		envelope.Response != nil && envelope.Response.Error != nil && envelope.Response.Error.Code == CyberPolicy
}
