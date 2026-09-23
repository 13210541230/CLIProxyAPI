package accountpool

import (
	"testing"
	"time"
)

func TestAdmissionWithoutPoolSelectionDoesNotLeakBusyState(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	engine := NewEngine(time.Second, time.Millisecond, func(request schedulerPickRequest) string {
		return normalizeSessionKey(metadataString(request.Options.Metadata, "session_id"))
	})
	engine.SetSessionIdleTTL(time.Minute)
	engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	engine.Configure(map[string]Limit{"auth-a": {Max: 1}})

	selectedRequest := schedulerPickRequest{
		Provider:   "codex",
		Candidates: []schedulerAuthCandidate{{ID: "auth-a"}},
		Options:    schedulerOptions{Metadata: map[string]any{"session_id": "picked-session"}},
	}
	if authID := engine.Pick(selectedRequest); authID != "auth-a" {
		t.Fatalf("Pick() = %q, want auth-a", authID)
	}
	if _, _, _, admitted := engine.Admit("hold", "auth-a", "picked-session"); !admitted {
		t.Fatal("initial admission was rejected")
	}
	if _, _, _, admitted := engine.Admit("busy-picked", "auth-a", "picked-session"); admitted {
		t.Fatal("busy admission unexpectedly succeeded")
	}

	// Simulate a request whose selected account came from another scheduler:
	// after-auth admission sees its session id, but this engine never picked it.
	if _, _, _, admitted := engine.Admit("busy-unpicked", "auth-a", "unpicked-session"); admitted {
		t.Fatal("unpicked busy admission unexpectedly succeeded")
	}

	engine.mu.Lock()
	_, orphaned := engine.busyRejections["unpicked-session"]
	busyCount := len(engine.busyRejections)
	engine.mu.Unlock()
	if orphaned || busyCount != 1 {
		t.Fatalf("busy state after unpicked admission: orphan=%v entries=%d, want no orphan and one picked session", orphaned, busyCount)
	}

	current = current.Add(time.Minute + time.Second)
	engine.Snapshot("auth-a") // pruning is driven by normal engine operations

	engine.mu.Lock()
	sessionCount := len(engine.sessions)
	busyCount = len(engine.busyRejections)
	engine.mu.Unlock()
	if sessionCount != 0 || busyCount != 0 {
		t.Fatalf("state after idle expiry: sessions=%d busy=%d, want both zero", sessionCount, busyCount)
	}
}
