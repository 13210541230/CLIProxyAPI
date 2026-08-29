package extract

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/textclean"
)

// Result is the allowlisted, text-only representation of one request body.
type Result struct {
	Text                  string
	TextAvailable         bool
	TextUnavailableReason string
}

// Extract selects a strict protocol/path extractor and never falls back to raw JSON.
func Extract(sourceFormat, requestPath string, body []byte) (Result, error) {
	var payload any
	if err := json.Unmarshal(body, &payload); err != nil {
		return Result{TextUnavailableReason: "malformed_json"}, fmt.Errorf("decode %s request body: %w", sourceFormat, err)
	}
	root, ok := payload.(map[string]any)
	if !ok {
		return Result{TextUnavailableReason: "unsupported_payload"}, nil
	}
	switch requestPath {
	case "/v1/chat/completions":
		return chat(root), nil
	case "/v1/completions":
		return completions(root), nil
	case "/v1/responses", "/backend-api/codex/responses":
		return responses(root), nil
	case "/v1/messages":
		return claude(root), nil
	default:
		if strings.HasPrefix(requestPath, "/v1beta/models/") {
			return gemini(root), nil
		}
	}
	return Result{TextUnavailableReason: "unsupported_path"}, nil
}

func chat(root map[string]any) Result {
	return latestRoleUser(array(root["messages"]), func(message map[string]any) []string {
		return contentText(message["content"], "text")
	})
}

func completions(root map[string]any) Result {
	value, exists := root["prompt"]
	if !exists {
		return Result{TextUnavailableReason: "prompt_missing"}
	}
	if prompt, ok := value.(string); ok {
		return textResult([]string{prompt})
	}
	items, ok := value.([]any)
	if !ok {
		return Result{TextUnavailableReason: "unsupported_prompt"}
	}
	if len(items) == 0 {
		return Result{TextUnavailableReason: "no_user_text"}
	}
	allNumbers := true
	for _, item := range items {
		switch value := item.(type) {
		case float64:
			if value != float64(int64(value)) {
				allNumbers = false
			}
		case json.Number:
			if _, err := value.Int64(); err != nil {
				allNumbers = false
			}
		default:
			allNumbers = false
		}
	}
	if allNumbers {
		return Result{TextUnavailableReason: "numeric_prompt"}
	}
	for index := len(items) - 1; index >= 0; index-- {
		if prompt, ok := items[index].(string); ok {
			return textResult([]string{prompt})
		}
	}
	return Result{TextUnavailableReason: "no_user_text"}
}

func responses(root map[string]any) Result {
	value, exists := root["input"]
	if !exists {
		return Result{TextUnavailableReason: "input_missing"}
	}
	if input, ok := value.(string); ok {
		return textResult([]string{input})
	}
	return latestResponsesUser(array(value))
}

func claude(root map[string]any) Result {
	return latestRoleUser(array(root["messages"]), func(message map[string]any) []string {
		return contentText(message["content"], "text")
	})
}

func gemini(root map[string]any) Result {
	contents := array(root["contents"])
	frameworkTail := false
	for index := len(contents) - 1; index >= 0; index-- {
		content, ok := contents[index].(map[string]any)
		if !ok || strings.ToLower(stringValue(content["role"])) != "user" {
			if frameworkTail {
				return Result{TextUnavailableReason: "no_new_user_turn"}
			}
			return Result{TextUnavailableReason: "no_new_user_turn"}
		}
		var parts []string
		for _, part := range array(content["parts"]) {
			partObject, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if typeName, hasType := partObject["type"]; hasType && stringValue(typeName) != "text" {
				continue
			}
			if text, isString := partObject["text"].(string); isString {
				parts = append(parts, text)
			}
		}
		candidate, framework := cleanedTextResult(parts)
		if framework {
			frameworkTail = true
			continue
		}
		if frameworkTail {
			return Result{TextUnavailableReason: "no_new_user_turn"}
		}
		return candidate
	}
	return Result{TextUnavailableReason: "no_new_user_turn"}
}

func latestRoleUser(messages []any, parts func(map[string]any) []string) Result {
	frameworkTail := false
	for index := len(messages) - 1; index >= 0; index-- {
		message, ok := messages[index].(map[string]any)
		if !ok || strings.ToLower(stringValue(message["role"])) != "user" {
			return Result{TextUnavailableReason: "no_new_user_turn"}
		}
		candidate, framework := cleanedTextResult(parts(message))
		if framework {
			frameworkTail = true
			continue
		}
		if frameworkTail {
			return Result{TextUnavailableReason: "no_new_user_turn"}
		}
		return candidate
	}
	return Result{TextUnavailableReason: "no_new_user_turn"}
}

func latestResponsesUser(items []any) Result {
	frameworkTail := false
	for index := len(items) - 1; index >= 0; index-- {
		item, ok := items[index].(map[string]any)
		if !ok || strings.ToLower(stringValue(item["role"])) != "user" {
			return Result{TextUnavailableReason: "no_new_user_turn"}
		}
		var parts []string
		if stringValue(item["type"]) == "input_text" {
			if text, isString := item["text"].(string); isString {
				parts = []string{text}
			}
		} else {
			parts = contentText(item["content"], "input_text")
		}
		candidate, framework := cleanedTextResult(parts)
		if framework {
			frameworkTail = true
			continue
		}
		if frameworkTail {
			return Result{TextUnavailableReason: "no_new_user_turn"}
		}
		return candidate
	}
	return Result{TextUnavailableReason: "no_new_user_turn"}
}

func cleanedTextResult(parts []string) (Result, bool) {
	if len(parts) == 0 {
		return Result{TextUnavailableReason: "no_user_text"}, false
	}
	raw := strings.Join(parts, "\n")
	text, ok := textclean.Clean(raw)
	if !ok {
		if textclean.IsFrameworkMessage(raw) {
			return Result{TextUnavailableReason: "no_new_user_turn"}, true
		}
		return Result{TextUnavailableReason: "no_user_text"}, false
	}
	return Result{Text: text, TextAvailable: true}, false
}

func contentText(value any, expectedType string) []string {
	if text, ok := value.(string); ok {
		return []string{text}
	}
	var result []string
	for _, item := range array(value) {
		block, ok := item.(map[string]any)
		if !ok || stringValue(block["type"]) != expectedType {
			continue
		}
		if text, ok := block["text"].(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func textResult(parts []string) Result {
	result, _ := cleanedTextResult(parts)
	return result
}

func array(value any) []any {
	items, _ := value.([]any)
	return items
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
