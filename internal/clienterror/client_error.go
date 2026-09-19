// Package clienterror classifies upstream failures caused by the client request.
package clienterror

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/tidwall/gjson"
)

// StatusClientClosedRequest is the nginx-style status used when the client
// aborts the request before the proxy finishes (context.Canceled).
const StatusClientClosedRequest = 499

// UpstreamErrorDetails contains safe, bounded error metadata suitable for
// request telemetry. It deliberately excludes the raw upstream body.
type UpstreamErrorDetails struct {
	Code    string
	Type    string
	Summary string
}

var (
	telemetrySecretPattern = regexp.MustCompile(`(?i)\b(?:authorization|bearer|basic|api[_ -]?key|access[_ -]?token|refresh[_ -]?token|client[_ -]?secret|password|secret)\b\s*[:=]\s*["']?[^,\s"']+`)
	telemetryTokenPattern  = regexp.MustCompile(`\b(?:sk-|ghp_)[A-Za-z0-9._~+/=-]{6,}\b`)
)

var requestFaultCodes = map[string]struct{}{
	"cyber_policy":                {},
	"context_length_exceeded":     {},
	"message_too_big":             {},
	"string_above_max_length":     {},
	"invalid_prompt":              {},
	"invalid_value":               {},
	"unsupported_value":           {},
	"invalid_request_error":       {},
	"previous_response_not_found": {},
}

var requestFaultTypes = map[string]struct{}{
	"invalid_request":       {},
	"invalid_request_error": {},
	"bad_request_error":     {},
	"invalid_prompt":        {},
}

// ExtractUpstreamErrorDetails extracts only structured code/type and a
// sanitized, bounded summary from an upstream error string.
func ExtractUpstreamErrorDetails(raw string) UpstreamErrorDetails {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return UpstreamErrorDetails{}
	}
	jsonPart := raw
	if idx := strings.Index(raw, ": {"); idx >= 0 && idx < 80 {
		jsonPart = strings.TrimSpace(raw[idx+2:])
	}

	var code, errType, message string
	if json.Valid([]byte(jsonPart)) {
		parsed := gjson.Parse(jsonPart)
		for _, path := range []string{"error.code", "response.error.code", "body.error.code", "code"} {
			if code = strings.TrimSpace(parsed.Get(path).String()); code != "" {
				break
			}
		}
		for _, path := range []string{"error.type", "response.error.type", "body.error.type", "type"} {
			if errType = strings.TrimSpace(parsed.Get(path).String()); errType != "" {
				break
			}
		}
		for _, path := range []string{"error.message", "response.error.message", "body.error.message", "message"} {
			if message = strings.TrimSpace(parsed.Get(path).String()); message != "" {
				break
			}
		}
	}
	if message == "" && code == "" && errType == "" {
		message = raw
	}
	if message == "" {
		message = errType
	}
	if message == "" {
		message = code
	}
	message = sanitizeTelemetrySummary(message)
	if code != "" && message != "" && !strings.Contains(strings.ToLower(message), strings.ToLower(code)) {
		message = code + ": " + message
	}
	return UpstreamErrorDetails{Code: code, Type: errType, Summary: message}
}

func sanitizeTelemetrySummary(value string) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	value = telemetrySecretPattern.ReplaceAllString(value, "[REDACTED]")
	value = telemetryTokenPattern.ReplaceAllString(value, "[REDACTED]")
	if len(value) > 512 {
		value = value[:512]
	}
	return strings.TrimSpace(value)
}

// HTTPStatusFromError extracts an HTTP status from err.
// Explicit StatusCode() values win. Otherwise context.Canceled maps to 499
// and context.DeadlineExceeded maps to 504. Returns 0 when unknown.
func HTTPStatusFromError(err error) int {
	if err == nil {
		return 0
	}
	type statusCoder interface {
		StatusCode() int
	}
	var sc statusCoder
	if errors.As(err, &sc) && sc != nil {
		if code := sc.StatusCode(); code > 0 {
			return code
		}
	}
	if errors.Is(err, context.Canceled) {
		return StatusClientClosedRequest
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusGatewayTimeout
	}
	return 0
}

// HTTPStatusFromErrorOr is like HTTPStatusFromError but returns fallback when
// the error does not carry a known status.
func HTTPStatusFromErrorOr(err error, fallback int) int {
	if code := HTTPStatusFromError(err); code > 0 {
		return code
	}
	return fallback
}

// IsRequestFault reports whether an upstream failure is caused by the request
// and therefore must not rotate or penalize credentials.
func IsRequestFault(status int, err error) bool {
	if status <= 0 && err != nil {
		type statusCoder interface {
			StatusCode() int
		}
		var statusErr statusCoder
		if errors.As(err, &statusErr) && statusErr != nil {
			status = statusErr.StatusCode()
		}
	}
	// Payment and rate-limit statuses are authoritative even when an upstream
	// pairs them with a generic invalid_request_error body. The credential must
	// remain eligible for cooldown and rotation.
	if status == http.StatusPaymentRequired || status == http.StatusTooManyRequests {
		return false
	}
	// DeepSeek reports an invalid API key as 401 with the authentication_error
	// type alongside the same generic code. Preserve that credential failure
	// classification without weakening generic request-fault handling.
	if status == http.StatusUnauthorized && hasAuthenticationErrorBody(err) {
		return false
	}
	// Model not found indicates a credential-model capability mismatch rather than
	// a caller request error. Preserve rotation and cooldown for the model.
	if hasModelNotFoundErrorBody(err) {
		return false
	}
	if hasRequestFaultBody(err) {
		return true
	}
	if err != nil && IsItemNotPersisted(err.Error()) {
		return true
	}
	switch status {
	case http.StatusBadRequest,
		http.StatusConflict,
		http.StatusRequestEntityTooLarge,
		http.StatusUnprocessableEntity:
		return true
	default:
		return false
	}
}

// IsItemNotPersisted matches the upstream 404 raised when a request references a
// response item the upstream never stored because `store` was false. The upstream
// sends this as a plain-text message rather than a JSON body, so it cannot be
// recognized through the structured identifiers above.
//
// The request can only succeed once the client rebuilds it without the stale
// reference, so it is a request fault: rotating credentials cannot help, and the
// client must be told rather than left to retry the same broken input.
func IsItemNotPersisted(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "item with id") &&
		strings.Contains(lower, "not found") &&
		strings.Contains(lower, "items are not persisted when `store` is set to false")
}

func hasModelNotFoundErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if code == "model_not_found" || code == "model_not_found_error" {
			return true
		}
	}
	return false
}

func hasAuthenticationErrorBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		if errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String())); errType == "authentication_error" {
			return true
		}
	}
	return false
}

func hasRequestFaultBody(err error) bool {
	if err == nil {
		return false
	}
	body := strings.TrimSpace(err.Error())
	if body == "" || !json.Valid([]byte(body)) {
		return false
	}
	for _, path := range []string{"error.code", "code", "response.error.code", "body.error.code"} {
		code := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if _, ok := requestFaultCodes[code]; ok {
			return true
		}
	}
	for _, path := range []string{"error.type", "type", "response.error.type", "body.error.type"} {
		errType := strings.ToLower(strings.TrimSpace(gjson.Get(body, path).String()))
		if _, ok := requestFaultTypes[errType]; ok {
			return true
		}
	}
	return false
}

// IsClientCancellation reports whether an HTTP status code or error represents
// a client-initiated cancellation (HTTP 499 StatusClientClosedRequest or context.Canceled).
func IsClientCancellation(status int, err error) bool {
	if status == StatusClientClosedRequest {
		return true
	}
	if err != nil {
		if errors.Is(err, context.Canceled) {
			return true
		}
		type statusCoder interface {
			StatusCode() int
		}
		var sc statusCoder
		if errors.As(err, &sc) && sc != nil && sc.StatusCode() == StatusClientClosedRequest {
			return true
		}
		lower := strings.ToLower(err.Error())
		if strings.Contains(lower, "context canceled") || strings.Contains(lower, "client closed request") {
			return true
		}
	}
	return false
}
