package accountpool

import (
	"fmt"
	"testing"
	"time"
)

func withPickID(request schedulerPickRequest, id string) schedulerPickRequest {
	request.Options.Headers = map[string][]string{PickRequestIDHeader: {id}}
	return request
}

func TestExpiredPickCannotConsumeNewRequestsReservations(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	if got := svc.Pick(borrowingRequest("old")); got.AuthID != "b" {
		t.Fatalf("old pick: %+v", got)
	}
	*now = now.Add(11 * time.Second)
	for i := 0; i < 3; i++ {
		if got := svc.Pick(borrowingRequest(fmt.Sprintf("new-%d", i))); got.AuthID != "b" {
			t.Fatalf("new pick: %+v", got)
		}
	}
	meta := map[string]any{metadataKeyHash: "abcd1234", metadataSelectedAuthID: "b", "session_id": "old"}
	if rejected := svc.AdmitIntercept("old", nil, meta); rejected != nil {
		t.Fatalf("delayed old admission: %+v", rejected)
	}
	if state := svc.StateSnapshot("b"); state.Active != 1 || state.Reserved != 3 {
		t.Fatalf("old expired pick must not steal a newer reservation: %+v", state)
	}
	if got := svc.Pick(borrowingRequest("fourth-new")); got.AuthID != "a" {
		t.Fatalf("active + outstanding picks already occupy four slots: %+v", got)
	}
}

func TestReservationConsumptionIsRequestSpecific(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	e := NewEngine(10*time.Second, time.Second, nil)
	e.SetClock(func() time.Time { return current }, nil)
	e.Pick(withPickID(strictPoolRequest(nil, "a"), "first"))
	e.Pick(withPickID(strictPoolRequest(nil, "a"), "second"))
	if _, _, _, ok := e.Admit("second", "a", ""); !ok {
		t.Fatal("second admission rejected")
	}
	e.mu.Lock()
	remaining := append([]pickReservation(nil), e.reserved["a"]...)
	e.mu.Unlock()
	if len(remaining) != 1 || remaining[0].RequestID != "first" {
		t.Fatalf("out-of-order admission consumed wrong pick: %+v", remaining)
	}
	if _, _, _, ok := e.Admit("unknown", "a", ""); !ok {
		t.Fatal("unselected admission rejected")
	}
	if state := e.Snapshot("a"); state.Reserved != 1 || state.Active != 2 {
		t.Fatalf("unselected admission must not consume a peer's pick: %+v", state)
	}
}

func TestDuplicateAdmissionConsumesOnlyFreshOwnRepick(t *testing.T) {
	current := time.Unix(1_700_000_000, 0)
	e := NewEngine(10*time.Second, time.Second, nil)
	e.SetClock(func() time.Time { return current }, nil)
	e.Configure(map[string]Limit{"a": {Max: 5}})
	req := withPickID(strictPoolRequest(map[string]any{"session_id": "retry"}, "a"), "same-request")
	key := sessionKeyFrom(req.Options.Headers, req.Options.Metadata, "")
	e.Pick(req)
	if _, _, _, ok := e.Admit("same-request", "a", key); !ok {
		t.Fatal("initial admission rejected")
	}
	e.Pick(req)
	if _, _, _, ok := e.Admit("same-request", "a", key); !ok {
		t.Fatal("same-account retry admission rejected")
	}
	if state := e.Snapshot("a"); state.Active != 1 || state.Reserved != 0 {
		t.Fatalf("duplicate admission must release the fresh own repick without recounting execution: %+v", state)
	}
	e.Pick(withPickID(strictPoolRequest(map[string]any{"session_id": "peer"}, "a"), "peer-request"))
	e.Admit("same-request", "a", key)
	if state := e.Snapshot("a"); state.Active != 1 || state.Reserved != 1 {
		t.Fatalf("duplicate without an own pick must leave the peer's reservation intact: %+v", state)
	}
}

func TestBorrowingObservesDisabledTrafficWithoutApplyingLimits(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	dir := svc.persist.dataDir
	svc.EnabledWithDataDir(false, dir)
	for i := 0; i < 6; i++ {
		id := fmt.Sprintf("off-%d", i)
		meta := map[string]any{metadataSelectedAuthID: "b", metadataKeyHash: "abcd1234", "session_id": id}
		if rejected := svc.AdmitIntercept(id, nil, meta); rejected != nil {
			t.Fatalf("disabled limits must not reject request %s: %+v", id, rejected)
		}
	}
	if state := svc.StateSnapshot("b"); state.Active != 6 {
		t.Fatalf("disabled admission must still observe real execution occupancy: %+v", state)
	}
	*now = now.Add(time.Minute)
	svc.EnabledWithDataDir(true, dir)
	if got := svc.Pick(borrowingRequest("on-new")); got.AuthID != "a" {
		t.Fatalf("reenabling must not misclassify still-running off-period requests as idle: %+v", got)
	}
	for i := 0; i < 6; i++ {
		svc.Complete(fmt.Sprintf("off-%d", i))
	}
	*now = now.Add(time.Minute)
	if got := svc.Pick(borrowingRequest("after-idle")); got.AuthID != "b" {
		t.Fatalf("completed off-period requests and sustained idle permit borrowing: %+v", got)
	}
}

func TestFallbackInFlightProtectsBorrowedOAuthBinding(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holds := holdAccount(t, svc.engine, "a", 5)
	original := borrowingRequest("conversation")
	if got := svc.Pick(original); got.AuthID != "b" {
		t.Fatalf("initial borrow: %+v", got)
	}
	p := svc.Policy()
	p.Version++
	p.Bindings[0].CrossPoolExempt = false
	publishBorrowingPolicy(t, svc, p)
	for _, id := range holds {
		svc.Complete(id)
	}
	fallback := withPickID(original, "fallback-request")
	fallback.Candidates = []schedulerAuthCandidate{{ID: "codex:apikey:fallback", Attributes: map[string]string{"auth_kind": "apikey"}}}
	if got := svc.Pick(fallback); got.AuthID != "codex:apikey:fallback" {
		t.Fatalf("API fallback selection: %+v", got)
	}
	meta := map[string]any{metadataKeyHash: "abcd1234", metadataSelectedAuthID: "codex:apikey:fallback", "session_id": "conversation"}
	if rejected := svc.AdmitIntercept("fallback-request", fallback.Options.Headers, meta); rejected != nil {
		t.Fatalf("fallback admission: %+v", rejected)
	}
	*now = now.Add(2*time.Hour + time.Second)
	original.Candidates = []schedulerAuthCandidate{{ID: "a"}}
	if got := svc.Pick(original); got.Decision != "reject" || got.ErrorCode != "account_pool_unavailable" {
		t.Fatalf("still-running fallback must protect cooled borrowed binding: %+v", got)
	}
	original.Candidates = borrowingRequest("").Candidates
	if got := svc.Pick(original); got.AuthID != "b" {
		t.Fatalf("original account must recover without migration: %+v", got)
	}
	svc.Complete("fallback-request")
	*now = now.Add(2*time.Hour + time.Second)
	if got := svc.Pick(original); got.AuthID != "a" {
		t.Fatalf("completed fallback and idle session may return to primary: %+v", got)
	}
}
