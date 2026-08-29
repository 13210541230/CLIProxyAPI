package textclean

import (
	"html"
	"strings"
)

var frameworkPrefixes = []string{
	"<background-task-notification",
	"<compartment_examples_from_other_projects",
	"<ctx-search-hint",
	"<memory-updates",
	"<new-compartments",
	"<new-memories",
	"<project-memory",
	"<session-history",
	"<session-references",
	"<summary>",
	"<system-reminder",
	"<user-profile",
}

// Clean removes agent-harness decorations and rejects messages that are not human prompts.
// It deliberately does not parse arbitrary HTML or Markdown; those remain user text for the UI.
func Clean(value string) (string, bool) {
	text := normalizeWhitespace(value)
	text = decodeFrameworkMarkup(text)
	if strings.HasPrefix(text, "§") {
		if cleaned, ok := stripSectionMarker(text); ok {
			text = cleaned
		}
	}
	text = stripLeadingElapsedComment(text)
	text = stripFrameworkSuffix(text)
	text = strings.TrimSpace(text)
	if text == "" || isFrameworkMessage(text) {
		return "", false
	}
	return text, true
}

func normalizeWhitespace(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\r\n", "\n"))
}

func decodeFrameworkMarkup(value string) string {
	decoded := html.UnescapeString(value)
	if decoded == value {
		return value
	}
	for _, prefix := range frameworkPrefixes {
		if strings.Contains(decoded, prefix) {
			return decoded
		}
	}
	return value
}

func stripSectionMarker(value string) (string, bool) {
	const marker = "§"
	if !strings.HasPrefix(value, marker) {
		return value, false
	}
	closingOffset := strings.Index(value[len(marker):], marker)
	if closingOffset <= 0 {
		return value, false
	}
	for _, character := range value[len(marker) : len(marker)+closingOffset] {
		if character < '0' || character > '9' {
			return value, false
		}
	}
	start := len(marker) + closingOffset + len(marker)
	return strings.TrimSpace(value[start:]), true
}

func stripLeadingElapsedComment(value string) string {
	text := strings.TrimSpace(value)
	for strings.HasPrefix(text, "<!--") {
		end := strings.Index(text, "-->")
		if end < 0 {
			return text
		}
		text = strings.TrimSpace(text[end+len("-->"):])
	}
	return text
}

func stripFrameworkSuffix(value string) string {
	cut := len(value)
	for _, prefix := range frameworkPrefixes {
		if index := strings.Index(value, "\n"+prefix); index >= 0 && index < cut {
			cut = index
		}
	}
	return strings.TrimSpace(value[:cut])
}

func isFrameworkMessage(value string) bool {
	text := strings.TrimSpace(value)
	for _, prefix := range frameworkPrefixes {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	for _, prefix := range []string{
		"The conversation history before this point was compacted",
		"The following is a summary of the conversation",
		"Ran `",
	} {
		if strings.HasPrefix(text, prefix) {
			return true
		}
	}
	return false
}
