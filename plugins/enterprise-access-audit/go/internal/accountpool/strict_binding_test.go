package accountpool

import (
	"testing"
	"time"
)

// strictPoolRequest builds a bound-pool pick with the given candidates.
func strictPoolRequest(metadata map[string]any, ids ...string) schedulerPickRequest {
	cands := make([]schedulerAuthCandidate, 0, len(ids))
	for _, id := range ids {
		cands = append(cands, schedulerAuthCandidate{ID: id})
	}
	return schedulerPickRequest{
		Provider:   "codex",
		Candidates: cands,
		Options:    schedulerOptions{Metadata: metadata},
	}
}

func strictSessionFn(req schedulerPickRequest) string {
	return normalizeSessionKey(metadataString(req.Options.Metadata, "session_id"))
}

// TestPickPoolNeverRebindsWhileAccountGone locks the single-account realism
// contract: the bound account leaving the candidate set (cooled, removed,
// disabled) yields a coded decline for the api-key layer to absorb — the
// session must never hop to a sibling OAuth account.
func TestPickPoolNeverRebindsWhileAccountGone(t *testing.T) {
	engine := newStickyEngine(t, strictSessionFn)
	meta := map[string]any{"session_id": "sess-strict"}

	if got := engine.Pick(strictPoolRequest(meta, "a", "b")); got != "a" {
		t.Fatalf("initial pick = %q, want a", got)
	}

	// Bound account gone; a sibling remains available.
	gotID, decline := engine.PickPool(strictPoolRequest(meta, "b"))
	if gotID != "" {
		t.Fatalf("PickPool with bound account gone = %q, want decline (no hop to b)", gotID)
	}
	if decline != "account_pool_unavailable" {
		t.Fatalf("decline = %q, want account_pool_unavailable", decline)
	}
	// The lenient path still rebinds: unbound sessions keep global routing.
	if got := engine.Pick(strictPoolRequest(meta, "b")); got != "b" {
		t.Fatalf("lenient pick after exit = %q, want b", got)
	}
}

// TestPickPoolDefersToAPIKeyOnPersistentBusy: a full busy budget on the bound
// account must decline with account_busy (deferring to the api-key provider)
// instead of selecting a sibling, and the binding must survive.
func TestPickPoolDefersToAPIKeyOnPersistentBusy(t *testing.T) {
	engine := newStickyEngine(t, strictSessionFn)
	meta := map[string]any{"session_id": "sess-strict-busy"}
	sessKey := strictSessionFn(strictPoolRequest(meta))

	if got := engine.Pick(strictPoolRequest(meta, "a", "b")); got != "a" {
		t.Fatalf("initial pick = %q, want a", got)
	}
	if code, _, _, ok := engine.Admit("hold", "a", sessKey); !ok || code != "" {
		t.Fatalf("hold admit = %q ok=%v", code, ok)
	}
	for i := 1; i <= 3; i++ {
		mustBusy(t, engine, fmtID("sb", i), sessKey)
	}

	gotID, decline := engine.PickPool(strictPoolRequest(meta, "a", "b"))
	if gotID != "" {
		t.Fatalf("PickPool after busy budget = %q, want decline (never b)", gotID)
	}
	if decline != "account_busy" {
		t.Fatalf("decline = %q, want account_busy", decline)
	}

	// Binding survives: once the account drains, the same session returns to a.
	engine.Complete("hold")
	if gotID, decline := engine.PickPool(strictPoolRequest(meta, "a", "b")); gotID != "a" || decline != "" {
		t.Fatalf("PickPool after drain = (%q, %q), want (a, \"\")", gotID, decline)
	}
}

// TestPickPoolBindingExpiresAfterIdleTTL locks the 2-hour escape: an active
// session never rebinds, but once it stays silent past the idle TTL the next
// request may select a fresh account.
func TestPickPoolBindingExpiresAfterIdleTTL(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	engine := NewEngine(time.Second, time.Second, strictSessionFn)
	engine.SetClock(func() time.Time { return current }, nil)
	engine.Configure(map[string]Limit{})

	meta := map[string]any{"session_id": "sess-idle"}
	if got := engine.Pick(strictPoolRequest(meta, "a", "b")); got != "a" {
		t.Fatalf("initial pick = %q, want a", got)
	}

	// Just under the TTL: the decline itself refreshes the idle clock (it is
	// still a request from the session), so the binding stays.
	current = current.Add(2*time.Hour - time.Minute)
	if gotID, decline := engine.PickPool(strictPoolRequest(meta, "b")); gotID != "" || decline != "account_pool_unavailable" {
		t.Fatalf("PickPool just under TTL = (%q, %q), want decline", gotID, decline)
	}

	// Past the TTL measured from the LAST request (the decline above): the
	// binding expires and fresh selection is permitted.
	current = current.Add(2*time.Hour + time.Minute)
	gotID, decline := engine.PickPool(strictPoolRequest(meta, "b"))
	if gotID != "b" || decline != "" {
		t.Fatalf("PickPool past TTL = (%q, %q), want (b, \"\") — fresh selection", gotID, decline)
	}
}

// TestTouchKeepsActiveSessionAlive: requests currently served by the deferred
// api-key layer must still refresh the binding clock (Touch), so an active
// session never expires mid-conversation.
func TestTouchKeepsActiveSessionAlive(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	engine := NewEngine(time.Second, time.Second, strictSessionFn)
	engine.SetClock(func() time.Time { return current }, nil)
	engine.Configure(map[string]Limit{})

	meta := map[string]any{"session_id": "sess-touch"}
	if got := engine.Pick(strictPoolRequest(meta, "a", "b")); got != "a" {
		t.Fatalf("initial pick = %q, want a", got)
	}

	// 90 minutes pass, api-key layer serves the session (Touch only).
	for i := 0; i < 6; i++ {
		current = current.Add(30 * time.Minute)
		engine.Touch(strictPoolRequest(meta))
	}

	// Still bound at the 3-hour mark because Touch kept the clock fresh.
	gotID, decline := engine.PickPool(strictPoolRequest(meta, "b"))
	if gotID != "" || decline != "account_pool_unavailable" {
		t.Fatalf("PickPool after active Touches = (%q, %q), want decline (still bound to a)", gotID, decline)
	}
}

func fmtID(prefix string, i int) string {
	return prefix + string(rune('0'+i))
}
