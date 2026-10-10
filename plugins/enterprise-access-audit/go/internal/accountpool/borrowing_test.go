package accountpool

import (
	"encoding/json"
	"fmt"
	"math"
	"sync"
	"testing"
	"time"
)

func borrowingService(t *testing.T, exempt bool) (*Service, *time.Time) {
	t.Helper()
	current := time.Unix(1_700_000_000, 0)
	svc := New(Options{DataDir: t.TempDir(), Enabled: true})
	svc.engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	if err := svc.PutLimits([]AccountLimit{{AuthID: "a", Limit: 5}, {AuthID: "b", Limit: 5}, {AuthID: "off", Limit: 5}}); err != nil {
		t.Fatal(err)
	}
	p := mustPolicy(t,
		[]Pool{{ID: "home", Name: "Home", Enabled: true}, {ID: "other", Name: "Other", Enabled: true}, {ID: "disabled", Name: "Disabled"}},
		[]Member{{PoolID: "home", AuthID: "a", Enabled: true}, {PoolID: "other", AuthID: "b", Enabled: true}, {PoolID: "disabled", AuthID: "off", Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "home", CrossPoolExempt: exempt}},
	)
	publishBorrowingPolicy(t, svc, p)
	return svc, &current
}

func publishBorrowingPolicy(t *testing.T, svc *Service, p Policy) {
	t.Helper()
	p.Hash = ""
	raw, err := json.Marshal(Envelope{Policy: p})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(raw); err != nil {
		t.Fatal(err)
	}
}

func borrowingRequest(session string) SchedulerPickRequest {
	return SchedulerPickRequest{
		Provider:   "codex",
		Candidates: []schedulerAuthCandidate{{ID: "a"}, {ID: "b"}, {ID: "off"}, {ID: "outside"}},
		Options:    schedulerOptions{Headers: map[string][]string{PickRequestIDHeader: {session}}, Metadata: map[string]any{metadataKeyHash: "abcd1234", "session_id": session}},
	}
}

func holdAccount(t *testing.T, e *Engine, authID string, count int) []string {
	t.Helper()
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("hold-%s-%d", authID, i)
		if code, _, _, ok := e.Admit(ids[i], authID, ""); !ok {
			t.Fatalf("hold %s: %s", ids[i], code)
		}
	}
	return ids
}

func TestCrossPoolExemptionDefaultsAndPersists(t *testing.T) {
	svc, _ := borrowingService(t, false)
	legacy := svc.Policy()
	encoded, _ := json.Marshal(legacy)
	var payload map[string]any
	_ = json.Unmarshal(encoded, &payload)
	if _, ok := payload["bindings"].([]any)[0].(map[string]any)["crossPoolExempt"]; ok {
		t.Fatal("false exemption must not change legacy policy encoding/hash")
	}
	next := Clone(legacy)
	next.Bindings[0].CrossPoolExempt = true
	if Hash(next) == Hash(legacy) {
		t.Fatal("exemption must be included in the policy digest at the same version")
	}
	next.Version++
	publishBorrowingPolicy(t, svc, next)
	reloaded := New(Options{DataDir: svc.persist.dataDir, Enabled: true})
	if err := reloaded.Reload(); err != nil {
		t.Fatal(err)
	}
	binding, bound := CallerBinding(reloaded.Policy(), "ABCD1234abcdef")
	if !bound || !binding.CrossPoolExempt || binding.PoolID != "home" {
		t.Fatalf("reloaded binding: %+v, bound=%v", binding, bound)
	}
}

func TestCrossPoolExemptionPrimaryFirstAndOrdinaryIsolation(t *testing.T) {
	for _, exempt := range []bool{false, true} {
		t.Run(fmt.Sprint(exempt), func(t *testing.T) {
			svc, now := borrowingService(t, exempt)
			*now = now.Add(time.Minute)
			if got := svc.Pick(borrowingRequest("first")); got.AuthID != "a" {
				t.Fatalf("available home must win: %+v", got)
			}
			svc.engine.releaseReservation("a", "first")
			holdAccount(t, svc.engine, "a", 5)
			got := svc.Pick(borrowingRequest("second"))
			want := "a"
			if exempt {
				want = "b"
			}
			if got.AuthID != want {
				t.Fatalf("selection = %+v, want %s", got, want)
			}
		})
	}
}

func TestCrossPoolBorrowNeedsHistoryAndSustainedSpareCapacity(t *testing.T) {
	svc, now := borrowingService(t, true)
	holdAccount(t, svc.engine, "a", 5)
	if got := svc.Pick(borrowingRequest("cold")); got.AuthID != "a" {
		t.Fatalf("cold history must not borrow: %+v", got)
	}
	foreignHolds := holdAccount(t, svc.engine, "b", 5)
	*now = now.Add(time.Minute)
	for _, id := range foreignHolds {
		svc.Complete(id)
	}
	if got := svc.Pick(borrowingRequest("just-released")); got.AuthID != "a" {
		t.Fatalf("momentary release must not borrow: %+v", got)
	}
	*now = now.Add(15 * time.Second)
	if got := svc.Pick(borrowingRequest("still-hot-average")); got.AuthID != "a" {
		t.Fatalf("recent high mean must not borrow: %+v", got)
	}
	*now = now.Add(35 * time.Second)
	if got := svc.Pick(borrowingRequest("sustained-idle")); got.AuthID != "b" {
		t.Fatalf("sustained spare capacity should borrow: %+v", got)
	}
}

func TestCrossPoolBorrowReservesAtomicallyAndLeavesOneSlot(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	const count = 20
	results := make(chan string, count)
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results <- svc.Pick(borrowingRequest(fmt.Sprintf("burst-%d", i))).AuthID
		}(i)
	}
	wg.Wait()
	close(results)
	borrowed := 0
	for id := range results {
		if id == "b" {
			borrowed++
		} else if id != "a" {
			t.Fatalf("unexpected target %q", id)
		}
	}
	if borrowed != 4 || svc.StateSnapshot("b").Reserved != 4 {
		t.Fatalf("borrowed=%d state=%+v, want four reservations and one spare slot", borrowed, svc.StateSnapshot("b"))
	}
}

func TestBorrowedSessionSurvivesRevocationFailureAndPoolReassignment(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holds := holdAccount(t, svc.engine, "a", 5)
	req := borrowingRequest("sticky")
	if got := svc.Pick(req); got.AuthID != "b" {
		t.Fatalf("initial borrow: %+v", got)
	}
	p := svc.Policy()
	p.Version++
	p.Bindings[0].CrossPoolExempt = false
	publishBorrowingPolicy(t, svc, p)
	for _, id := range holds {
		svc.Complete(id)
	}
	if got := svc.Pick(req); got.AuthID != "b" {
		t.Fatalf("revocation must not migrate borrowed session: %+v", got)
	}
	if got := svc.Pick(borrowingRequest("new-normal")); got.AuthID != "a" {
		t.Fatalf("revocation must restrict new sessions: %+v", got)
	}
	// Simulate cooling/execution failure by removing b from host candidates.
	req.Candidates = []schedulerAuthCandidate{{ID: "a"}}
	*now = now.Add(119 * time.Minute)
	if got := svc.Pick(req); got.Decision != "reject" {
		t.Fatalf("cooled bound account must fail closed: %+v", got)
	}
	*now = now.Add(119 * time.Minute)
	if got := svc.Pick(req); got.Decision != "reject" {
		t.Fatalf("unsuccessful requests must refresh TTL: %+v", got)
	}
	p = svc.Policy()
	p.Version++
	p.Bindings[0].PoolID = "other"
	publishBorrowingPolicy(t, svc, p)
	req.Candidates = borrowingRequest("").Candidates
	if got := svc.Pick(req); got.AuthID != "b" {
		t.Fatalf("primary-pool policy change must preserve session: %+v", got)
	}
	p = svc.Policy()
	p.Version++
	p.Bindings[0].PoolID = "home"
	publishBorrowingPolicy(t, svc, p)
	*now = now.Add(2*time.Hour + time.Second)
	if got := svc.Pick(req); got.AuthID != "a" {
		t.Fatalf("idle expiry must restore primary-only new allocation: %+v", got)
	}
}

func TestBorrowedSessionHasNoProtectedAdmissionSlot(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	req := borrowingRequest("borrowed")
	if got := svc.Pick(req); got.AuthID != "b" {
		t.Fatalf("initial selection: %+v", got)
	}
	holdAccount(t, svc.engine, "b", 4)
	meta := map[string]any{metadataKeyHash: "abcd1234", metadataSelectedAuthID: "b", "session_id": "borrowed"}
	if rejected := svc.AdmitIntercept("borrowed", nil, meta); rejected != nil {
		t.Fatalf("existing borrowed session may occupy fifth slot: %+v", rejected)
	}
	if svc.StateSnapshot("b").Active != 5 {
		t.Fatal("no continuous protected slot should be imposed")
	}
}

func TestReservationConsumedOnceWithoutDoubleCounting(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	e := NewEngine(10*time.Second, time.Second, strictSessionFn)
	e.SetClock(func() time.Time { return current }, nil)
	e.Configure(map[string]Limit{"a": {Max: 1}})
	e.Pick(withPickID(strictPoolRequest(map[string]any{"session_id": "one"}, "a"), "one"))
	current = current.Add(time.Millisecond)
	if _, _, _, ok := e.Admit("one", "a", "one"); !ok {
		t.Fatal("admission rejected")
	}
	if state := e.Snapshot("a"); state.Active != 1 || state.Reserved != 0 {
		t.Fatalf("admitted request must not remain reserved: %+v", state)
	}
	e.Pick(withPickID(strictPoolRequest(map[string]any{"session_id": "queued"}, "a"), "queued"))
	e.SetClock(func() time.Time { return current }, func(d time.Duration) {
		current = current.Add(d)
		e.Pick(withPickID(strictPoolRequest(map[string]any{"session_id": "next"}, "a"), "next"))
		e.Complete("one")
	})
	if _, _, _, ok := e.Admit("queued", "a", "queued"); !ok {
		t.Fatal("queued admission rejected")
	}
	if state := e.Snapshot("a"); state.Active != 1 || state.Reserved != 1 || state.Waiting != 0 {
		t.Fatalf("queue wake must not consume another request's reservation: %+v", state)
	}
	current = current.Add(11 * time.Second)
	if state := e.Snapshot("a"); state.Reserved != 0 {
		t.Fatalf("unconsumed reservation must expire: %+v", state)
	}
}

func TestOccupancyMeanIntegratesExecutionTime(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	e := NewEngine(time.Second, time.Second, nil)
	e.SetClock(func() time.Time { return current }, nil)
	e.Configure(map[string]Limit{"a": {Max: 5}})
	ids := holdAccount(t, e, "a", 2)
	current = current.Add(30 * time.Second)
	for _, id := range ids {
		e.Complete(id)
	}
	current = current.Add(30 * time.Second)
	state := e.Snapshot("a")
	if !state.BorrowReady || math.Abs(state.AverageActive-1) > 1e-9 || !state.Borrowable {
		t.Fatalf("two executions for half of a minute average to one: %+v", state)
	}
	current = current.Add(24 * time.Hour)
	state = e.Snapshot("a")
	if state.AverageActive != 0 || !state.Borrowable {
		t.Fatalf("old occupancy must age out with bounded work: %+v", state)
	}
}

func TestInFlightSessionCannotExpireOrMove(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	e := NewEngine(time.Second, time.Second, strictSessionFn)
	e.SetClock(func() time.Time { return current }, nil)
	meta := map[string]any{"session_id": "long-running"}
	e.PickPool(strictPoolRequest(meta, "a", "b"))
	if _, _, _, ok := e.Admit("long", "a", "long-running"); !ok {
		t.Fatal("admission rejected")
	}
	current = current.Add(2*time.Hour + time.Second)
	if id, code := e.PickPool(strictPoolRequest(meta, "b")); id != "" || code != "account_pool_unavailable" {
		t.Fatalf("running session must remain bound: %q %q", id, code)
	}
	e.Complete("long")
	current = current.Add(2*time.Hour + time.Second)
	if id, code := e.PickPool(strictPoolRequest(meta, "b")); id != "b" || code != "" {
		t.Fatalf("completed and idle session may rebind: %q %q", id, code)
	}
}
