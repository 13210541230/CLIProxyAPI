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

// sessionNamespaceKey carries the routing-layer namespace injected by the
// service into the scoped pick request (pool id, policy version, caller hash,
// or the api-key layer marker). It isolates session bindings so different
// callers (or layers) that reuse the same conversation id never overwrite
// each other's account binding.
const sessionNamespaceKey = "account_pool_session_ns"

// baseSessionKey validates and returns a bounded printable session identity
// from headers and metadata, ignoring any namespace.
func baseSessionKey(headers map[string][]string, metadata map[string]any) string {
	for _, name := range sessionHeaderKeys {
		for header, values := range headers {
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
		if key := normalizeSessionKey(metadataString(metadata, name)); key != "" {
			return key
		}
	}
	return ""
}

// sessionKey returns the namespaced session identity for scheduler requests.
// The namespace isolates callers and routing layers; without it two callers
// reusing one conversation id would share (and overwrite) one binding.
func sessionKey(request schedulerPickRequest) string {
	raw := baseSessionKey(request.Options.Headers, request.Options.Metadata)
	if raw == "" {
		return ""
	}
	ns := normalizeNamespace(metadataString(request.Options.Metadata, sessionNamespaceKey))
	if ns == "" {
		return raw
	}
	return ns + "|" + raw
}

// sessionKeyFrom derives the same namespaced identity at admission time from
// the same inputs Pick sees (headers first, then metadata), using the
// service-computed namespace.
func sessionKeyFrom(headers map[string][]string, metadata map[string]any, namespace string) string {
	raw := baseSessionKey(headers, metadata)
	if raw == "" {
		return ""
	}
	ns := normalizeNamespace(namespace)
	if ns == "" {
		return raw
	}
	return ns + "|" + raw
}

// callerNamespace builds the caller-hash namespace fragment (empty when the
// caller hash is unavailable).
func callerNamespace(metadata map[string]any) string {
	return normalizeNamespace(metadataString(metadata, metadataKeyHash))
}

func normalizeNamespace(value string) string {
	key := strings.TrimSpace(value)
	if key == "" || len(key) > 256 {
		return ""
	}
	for index := 0; index < len(key); index++ {
		if key[index] < 0x20 || key[index] > 0x7e {
			return ""
		}
	}
	return key
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
