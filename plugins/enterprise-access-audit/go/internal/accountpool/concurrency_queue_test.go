package accountpool

import (
	"testing"
	"time"
)

// TestAdmitQueueDrainsByCompletionNotTimeout proves a queue deeper than the
// concurrency limit keeps admitting as executing requests complete, while the
// fake clock never reaches the wait deadline. Counting queued waiters toward
// the limit deadlocks the queue behind an idle account until timeout fires.
func TestAdmitQueueDrainsByCompletionNotTimeout(t *testing.T) {
	fakeNow := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	engine := NewEngine(10*time.Second, 30*time.Second, nil)
	engine.SetClock(
		func() time.Time { return fakeNow },
		func(time.Duration) {}, // poll sleeps are no-ops; admission is logical, not timed
	)
	engine.Configure(map[string]Limit{"acct": {Max: 3}})

	// Fill the account: active = 3.
	for _, id := range []string{"r1", "r2", "r3"} {
		if code, status, _, ok := engine.Admit(id, "acct"); !ok || code != "" || status != 0 {
			t.Fatalf("%s admit = (%q,%d,ok), want immediate", id, code, status)
		}
	}

	// Queue four more waiters: queue depth (4) exceeds the limit (3).
	type result struct {
		id   string
		code string
		ok   bool
	}
	results := make(chan result, 4)
	for _, id := range []string{"r4", "r5", "r6", "r7"} {
		go func(requestID string) {
			code, _, _, ok := engine.Admit(requestID, "acct")
			results <- result{id: requestID, code: code, ok: ok}
		}(id)
	}

	waitWaiting := func(want int) {
		deadline := time.Now().Add(2 * time.Second)
		for engine.Snapshot("acct").Waiting < want {
			if time.Now().After(deadline) {
				t.Fatalf("waiters never queued: %+v", engine.Snapshot("acct"))
			}
			time.Sleep(time.Millisecond)
		}
	}
	waitWaiting(4)
	if got := engine.Snapshot("acct"); got.Active != 3 || got.Waiting != 4 {
		t.Fatalf("pre-drain state = %+v, want active=3 waiting=4", got)
	}

	// Complete all executing requests: exactly (limit) waiters must be admitted
	// without the fake clock ever approaching the 30s deadline.
	engine.Complete("r1")
	engine.Complete("r2")
	engine.Complete("r3")

	drained := make([]result, 0, 4)
	collect := func(count int, failAfter time.Duration) {
		guard := time.After(failAfter)
		for len(drained) < count {
			select {
			case res := <-results:
				if !res.ok {
					t.Fatalf("queued waiter rejected instead of admitted: %s %s", res.id, res.code)
				}
				drained = append(drained, res)
			case <-guard:
				t.Fatalf("queue deadlocked: only %d/%d admitted with an idle account", len(drained), count)
			}
		}
	}
	collect(3, 2*time.Second)
	if got := engine.Snapshot("acct"); got.Active != 3 || got.Waiting != 1 {
		t.Fatalf("mid-drain state = %+v, want active=3 waiting=1", got)
	}

	// One slot frees: the last waiter admits. Never a timeout, never a reject.
	engine.Complete(drained[0].id)
	collect(4, 2*time.Second)
	if got := engine.Snapshot("acct"); got.Active != 3 || got.Waiting != 0 {
		t.Fatalf("post-drain state = %+v, want active=3 waiting=0", got)
	}
}
