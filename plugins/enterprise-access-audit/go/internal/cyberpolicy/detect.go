package cyberpolicy

import (
	"encoding/json"
	"strings"
)

const UpstreamCyberPolicyCode = "cyber_policy"

type errorEnvelope struct {
	Error    *errorObject `json:"error"`
	Response *struct {
		Error *errorObject `json:"error"`
	} `json:"response"`
}

type errorObject struct {
	Code string `json:"code"`
}

// Detect reports only an explicit upstream cyber_policy error code. It accepts
// a raw JSON response or an error string containing a JSON response envelope.
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
	decoder := json.NewDecoder(strings.NewReader(errorText[start:]))
	if err := decoder.Decode(&envelope); err != nil {
		return false
	}
	if envelope.Error != nil && strings.EqualFold(strings.TrimSpace(envelope.Error.Code), UpstreamCyberPolicyCode) {
		return true
	}
	return envelope.Response != nil && envelope.Response.Error != nil && strings.EqualFold(strings.TrimSpace(envelope.Response.Error.Code), UpstreamCyberPolicyCode)
}
