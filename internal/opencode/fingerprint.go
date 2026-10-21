package opencode

import (
	"encoding/json"
	"net/http"
	"strings"

	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// UserAgentValue mirrors the OpenCode CLI stable channel User-Agent. It must stay
// in sync with upstream; a stale value is the most common cause of FreeTierError.
const UserAgentValue = "opencode/latest/2.0.29/cli"

// Protocol identifies the upstream wire protocol of a model.
type Protocol string

const (
	// ProtocolChat is the OpenAI chat completions protocol.
	ProtocolChat Protocol = "chat"
	// ProtocolResponses is the OpenAI responses protocol.
	ProtocolResponses Protocol = "responses"
	// ProtocolAnthropic is the Anthropic messages protocol.
	ProtocolAnthropic Protocol = "anthropic"
)

// Path returns the upstream path suffix for the protocol.
func (p Protocol) Path() string {
	switch p {
	case ProtocolResponses:
		return "/v1/responses"
	case ProtocolAnthropic:
		return "/v1/messages"
	default:
		return "/v1/chat/completions"
	}
}

// Format returns the CLIProxyAPI translator format matching the protocol.
func (p Protocol) Format() sdktranslator.Format {
	switch p {
	case ProtocolResponses:
		return sdktranslator.FormatOpenAIResponse
	case ProtocolAnthropic:
		return sdktranslator.FormatClaude
	default:
		return sdktranslator.FormatOpenAI
	}
}

// ProtocolForFormat maps a translator format back to a protocol.
func ProtocolForFormat(format sdktranslator.Format) (Protocol, bool) {
	switch format {
	case sdktranslator.FormatOpenAI:
		return ProtocolChat, true
	case sdktranslator.FormatOpenAIResponse:
		return ProtocolResponses, true
	case sdktranslator.FormatClaude:
		return ProtocolAnthropic, true
	default:
		return "", false
	}
}

// FormatForProtocol resolves the translator format from a protocol name.
func FormatForProtocol(name string) (sdktranslator.Format, bool) {
	switch Protocol(strings.ToLower(strings.TrimSpace(name))) {
	case ProtocolChat:
		return sdktranslator.FormatOpenAI, true
	case ProtocolResponses:
		return sdktranslator.FormatOpenAIResponse, true
	case ProtocolAnthropic:
		return sdktranslator.FormatClaude, true
	default:
		return "", false
	}
}

// Identity carries the OpenCode request identity headers derived per request.
type Identity struct {
	Session       string
	Project       string
	ParentSession string
}

// ApplyHeaders writes the OpenCode CLI request identity onto req.
func ApplyHeaders(req *http.Request, body []byte, id Identity) {
	if req == nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if streamRequested(body) {
		req.Header.Set("Accept", "text/event-stream")
	} else {
		req.Header.Set("Accept", "application/json")
	}
	req.Header.Set("User-Agent", UserAgentValue)
	req.Header.Set("x-opencode-client", "cli")

	session := id.Session
	if session == "" {
		session = SessionIDForSignal("cli-default")
	}
	req.Header.Set("x-opencode-session", session)
	req.Header.Set("x-opencode-session-id", session)
	req.Header.Set("x-session-affinity", session)
	req.Header.Set("X-Session-Id", session)
	req.Header.Del("x-opencode-request")
	req.Header.Del("x-opencode-parent-session-id")
	req.Header.Set("x-opencode-project", ProjectID(id.Project))

	if strings.TrimSpace(id.ParentSession) != "" {
		req.Header.Set("x-parent-session-id", strings.TrimSpace(id.ParentSession))
	} else {
		req.Header.Del("x-parent-session-id")
	}
}

// SetCredential writes the protocol specific upstream credential header.
func SetCredential(req *http.Request, protocol Protocol, key string) {
	if req == nil {
		return
	}
	req.Header.Del("Authorization")
	req.Header.Del("x-api-key")
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	if protocol == ProtocolAnthropic {
		req.Header.Set("x-api-key", key)
		return
	}
	req.Header.Set("Authorization", "Bearer "+key)
}

func streamRequested(body []byte) bool {
	if len(body) == 0 {
		return true
	}
	var payload struct {
		Stream *bool `json:"stream"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return true
	}
	if payload.Stream == nil {
		return false
	}
	return *payload.Stream
}
