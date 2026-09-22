package accountpool

import (
	"fmt"
	"testing"
	"time"
)

func stickySessionRequest(metadata map[string]any) schedulerPickRequest {
	return schedulerPickRequest{
		Provider:   "codex",
		Candidates: []schedulerAuthCandidate{{ID: "a"}, {ID: "b"}},
		Options:    schedulerOptions{Metadata: metadata},
	}
}

// newStickyEngine builds an engine with an advancing fake clock so queue
// waits deterministically expire into account_busy rejections.
func newStickyEngine(t *testing.T, sessionFn func(schedulerPickRequest) string) *Engine {
	t.Helper()
	current := time.Unix(1_700_000_000, 0)
	engine := NewEngine(time.Second, 100*time.Millisecond, sessionFn)
	engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	engine.Configure(map[string]Limit{"a": {Max: 1}, "b": {Max: 1}})
	return engine
}

func mustBusy(t *testing.T, engine *Engine, requestID string) {
	t.Helper()
	code, status, retryable, ok := engine.Admit(requestID, "a")
	if ok || code != "account_busy" || status != 503 || !retryable {
		t.Fatalf("Admit(%s) = %q %d retry=%v ok=%v, want account_busy/503", requestID, code, status, retryable, ok)
	}
}

// TestStickySessionKeepsAccountForTransientBusy locks the risk-control
// contract: queue timeouts below the persistent budget never move the
// session, even though every client retry passes through Pick again.
func TestStickySessionKeepsAccountForTransientBusy(t *testing.T) {
	sessionFn := func(req schedulerPickRequest) string {
		return normalizeSessionKey(metadataString(req.Options.Metadata, "session_id"))
	}
	engine := newStickyEngine(t, sessionFn)
	meta := map[string]any{"session_id": "sess-transient"}

	first := engine.Pick(stickySessionRequest(meta))
	if first != "a" {
		t.Fatalf("first Pick = %q, want a", first)
	}
	if code, _, _, ok := engine.Admit("hold", "a"); !ok || code != "" {
		t.Fatalf("hold admit = %q ok=%v", code, ok)
	}

	// Two consecutive rejections (budget = 3): each retry re-picks first, so a
	// reset inside Pick would break the failover test and stay invisible here.
	for i := 1; i <= 2; i++ {
		if got := engine.Pick(stickySessionRequest(meta)); got != "a" {
			t.Fatalf("retry pick %d = %q, want sticky a", i, got)
		}
		mustBusy(t, engine, fmt.Sprintf("busy-%d", i))
		if got := engine.Pick(stickySessionRequest(meta)); got != "a" {
			t.Fatalf("pick after %d rejections = %q, want a", i, got)
		}
	}
}

// TestStickySessionFailsOverAfterPersistentBusy proves the liveness escape:
// a full budget of consecutive queue timeouts with no success in between
// releases the binding so the stranded user reaches another in-pool account.
func TestStickySessionFailsOverAfterPersistentBusy(t *testing.T) {
	sessionFn := func(req schedulerPickRequest) string {
		return normalizeSessionKey(metadataString(req.Options.Metadata, "session_id"))
	}
	engine := newStickyEngine(t, sessionFn)
	meta := map[string]any{"session_id": "sess-persistent"}

	if got := engine.Pick(stickySessionRequest(meta)); got != "a" {
		t.Fatalf("first Pick = %q, want a", got)
	}
	if code, _, _, ok := engine.Admit("hold", "a"); !ok || code != "" {
		t.Fatalf("hold admit = %q ok=%v", code, ok)
	}

	for i := 1; i <= 3; i++ {
		mustBusy(t, engine, fmt.Sprintf("pb-%d", i))
	}

	got := engine.Pick(stickySessionRequest(meta))
	if got == "a" || got == "" {
		t.Fatalf("pick after persistent unavailability = %q, want failover to b", got)
	}
}

// TestBusyCounterResetsOnSuccessfulAdmission ensures only an unbroken streak
// counts: one success wipes prior rejections, so intermittently busy accounts
// never accumulate into a hop.
func TestBusyCounterResetsOnSuccessfulAdmission(t *testing.T) {
	sessionFn := func(req schedulerPickRequest) string {
		return normalizeSessionKey(metadataString(req.Options.Metadata, "session_id"))
	}
	engine := newStickyEngine(t, sessionFn)
	meta := map[string]any{"session_id": "sess-reset"}

	if got := engine.Pick(stickySessionRequest(meta)); got != "a" {
		t.Fatalf("first Pick = %q, want a", got)
	}
	if code, _, _, ok := engine.Admit("hold", "a"); !ok || code != "" {
		t.Fatalf("hold admit = %q ok=%v", code, ok)
	}

	// Two rejections, then a success: the streak must restart at zero. The
	// success stays active so the slot remains occupied for later rejections.
	mustBusy(t, engine, "reset-busy-1")
	mustBusy(t, engine, "reset-busy-2")
	engine.Complete("hold")
	if code, _, _, ok := engine.Admit("success", "a"); !ok || code != "" {
		t.Fatalf("success admit = %q ok=%v", code, ok)
	}

	// Two more rejections stay below the budget because the success cleared
	// the earlier two; without the reset the session would fail over here.
	mustBusy(t, engine, "reset-busy-3")
	mustBusy(t, engine, "reset-busy-4")
	if got := engine.Pick(stickySessionRequest(meta)); got != "a" {
		t.Fatalf("pick after reset+2 rejections = %q, want a", got)
	}

	mustBusy(t, engine, "reset-busy-5")
	if got := engine.Pick(stickySessionRequest(meta)); got == "a" || got == "" {
		t.Fatalf("pick after unbroken streak of 3 = %q, want failover", got)
	}
	engine.Complete("success")
}

// TestStickySessionRebindsWhenAccountLeavesCandidates covers the immediate
// transfer: the sticky account is no longer eligible (cooled, removed, or
// disabled), so the session rebinds instead of selecting a dead account.
func TestStickySessionRebindsWhenAccountLeavesCandidates(t *testing.T) {
	sessionFn := func(req schedulerPickRequest) string {
		return normalizeSessionKey(metadataString(req.Options.Metadata, "session_id"))
	}
	engine := NewEngine(time.Second, time.Second, sessionFn)
	engine.SetClock(func() time.Time { return time.Now() }, nil)

	meta := map[string]any{"session_id": "sess-move"}
	onlyA := schedulerPickRequest{
		Provider:   "codex",
		Candidates: []schedulerAuthCandidate{{ID: "a"}},
		Options:    schedulerOptions{Metadata: meta},
	}
	if got := engine.Pick(onlyA); got != "a" {
		t.Fatalf("initial Pick = %q, want a", got)
	}

	onlyB := schedulerPickRequest{
		Provider:   "codex",
		Candidates: []schedulerAuthCandidate{{ID: "b"}},
		Options:    schedulerOptions{Metadata: meta},
	}
	if got := engine.Pick(onlyB); got != "b" {
		t.Fatalf("Pick after candidate exit = %q, want b (sticky account left the pool)", got)
	}
}

// TestPolicyAllowsAccountInMultiplePools locks the pool-imbalance escape
// hatch: one account may be a member of several pools (per-pool membership),
// while user bindings stay single-pool, letting a hot pool share capacity.
func TestPolicyAllowsAccountInMultiplePools(t *testing.T) {
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering"}, {ID: "sales", Name: "Sales"}},
		[]Member{
			{PoolID: "eng", AuthID: "shared-auth", Enabled: true},
			{PoolID: "sales", AuthID: "shared-auth", Enabled: true},
		},
		[]Binding{{APIKeyHash: "aaaa1111", PoolID: "eng"}},
	)
	if len(EnabledMembers(p, "eng")) != 1 || len(EnabledMembers(p, "sales")) != 1 {
		t.Fatalf("shared account must be enabled in both pools: eng=%d sales=%d",
			len(EnabledMembers(p, "eng")), len(EnabledMembers(p, "sales")))
	}

	// Duplicate within ONE pool is still rejected.
	dup := p
	dup.Members = []Member{
		{PoolID: "eng", AuthID: "dup-auth", Enabled: true},
		{PoolID: "eng", AuthID: "dup-auth", Enabled: true},
	}
	if _, err := Normalize(dup); err == nil {
		t.Fatal("duplicate member inside one pool must be rejected")
	}
}
