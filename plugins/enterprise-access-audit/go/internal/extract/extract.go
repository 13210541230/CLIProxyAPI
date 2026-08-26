package extract

import (
	"encoding/json"
	"fmt"
	"strings"
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
	var parts []string
	for _, item := range array(root["messages"]) {
		message, ok := item.(map[string]any)
		if !ok || strings.ToLower(stringValue(message["role"])) != "user" {
			continue
		}
		parts = append(parts, contentText(message["content"], "text")...)
	}
	return textResult(parts)
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
		return textResult(nil)
	}
	parts := make([]string, 0, len(items))
	allNumbers := true
	for _, item := range items {
		switch value := item.(type) {
		case string:
			allNumbers = false
			parts = append(parts, value)
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
	return textResult(parts)
}

func responses(root map[string]any) Result {
	value, exists := root["input"]
	if !exists {
		return Result{TextUnavailableReason: "input_missing"}
	}
	if input, ok := value.(string); ok {
		return textResult([]string{input})
	}
	var parts []string
	for _, item := range array(value) {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		role := strings.ToLower(stringValue(message["role"]))
		if role != "user" {
			continue
		}
		if stringValue(message["type"]) == "input_text" {
			if text, isString := message["text"].(string); isString {
				parts = append(parts, text)
			}
			continue
		}
		parts = append(parts, contentText(message["content"], "input_text")...)
	}
	return textResult(parts)
}

func claude(root map[string]any) Result {
	var parts []string
	for _, item := range array(root["messages"]) {
		message, ok := item.(map[string]any)
		if !ok || strings.ToLower(stringValue(message["role"])) != "user" {
			continue
		}
		parts = append(parts, contentText(message["content"], "text")...)
	}
	return textResult(parts)
}

func gemini(root map[string]any) Result {
	var parts []string
	for _, item := range array(root["contents"]) {
		content, ok := item.(map[string]any)
		if !ok || strings.ToLower(stringValue(content["role"])) != "user" {
			continue
		}
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
	}
	return textResult(parts)
}

func contentText(value any, expectedType string) []string {
	if text, ok := value.(string); ok {
		return []string{text}
	}
	var result []string
	for _, item := range array(value) {
		block, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if stringValue(block["type"]) != expectedType {
			continue
		}
		if text, ok := block["text"].(string); ok {
			result = append(result, text)
		}
	}
	return result
}

func textResult(parts []string) Result {
	if len(parts) == 0 {
		return Result{TextUnavailableReason: "no_user_text"}
	}
	return Result{Text: strings.Join(parts, "\n"), TextAvailable: true}
}

func array(value any) []any {
	items, _ := value.([]any)
	return items
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
