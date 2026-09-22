package accountpool

import (
	"strings"
)

// sessionHeaderKeys and sessionMetadataKeys are read in order to derive a
// stable conversation session identity for stickiness within a pool.
// canonical_session_id is injected by the CPA host (ExtractSessionInfo over
// headers plus native payload identifiers) before scheduling, so native Codex
// clients stay sticky without custom headers.
var (
	sessionHeaderKeys   = []string{"x-session-id", "x-conversation-id", "x-thread-id", "x-prompt-cache-key"}
	sessionMetadataKeys = []string{"canonical_session_id", "session_id", "conversation_id", "thread_id", "prompt_cache_key"}
)

const sessionKeyMaxLen = 128

// sessionKey validates and returns a bounded printable session identity.
func sessionKey(request schedulerPickRequest) string {
	for _, name := range sessionHeaderKeys {
		for header, values := range request.Options.Headers {
			if !strings.EqualFold(header, name) {
				continue
			}
			for _, value := range values {
				if key := normalizeSessionKey(value); key != "" {
					return key
				}
			}
		}
	}
	for _, name := range sessionMetadataKeys {
		if key := normalizeSessionKey(metadataString(request.Options.Metadata, name)); key != "" {
			return key
		}
	}
	return ""
}

func normalizeSessionKey(value string) string {
	key := strings.TrimSpace(value)
	if key == "" || len(key) > sessionKeyMaxLen {
		return ""
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] > 0x7e {
			return ""
		}
	}
	return key
}
