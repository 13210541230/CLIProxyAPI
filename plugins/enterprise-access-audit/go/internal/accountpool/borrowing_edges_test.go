package accountpool

import (
	"testing"
	"time"
)

func TestOrdinaryCallerRetainsPrimaryPoolScoring(t *testing.T) {
	for _, exempt := range []bool{false, true} {
		t.Run(map[bool]string{false: "ordinary", true: "exempt"}[exempt], func(t *testing.T) {
			svc, now := borrowingService(t, exempt)
			p := svc.Policy()
			p.Version++
			p.Members = append(p.Members, Member{PoolID: "home", AuthID: "c", Enabled: true})
			publishBorrowingPolicy(t, svc, p)
			svc.engine.Configure(map[string]Limit{"a": {Max: 5}, "b": {Max: 5}, "c": {Max: 5}})
			*now = now.Add(time.Minute)
			holdAccount(t, svc.engine, "c", 2)
			svc.engine.mu.Lock()
			svc.engine.waiting["a"] = 1
			svc.engine.observeLocked("a", *now)
			svc.engine.mu.Unlock()
			req := borrowingRequest("new")
			req.Candidates = []schedulerAuthCandidate{{ID: "a"}, {ID: "b"}, {ID: "c"}}
			want := "a" // Existing pressure scoring prefers 1/5 over 2/5.
			if exempt {
				want = "c" // Exempt allocation prefers immediate primary capacity.
			}
			if got := svc.Pick(req); got.AuthID != want {
				t.Fatalf("exempt=%v pick=%+v, want %s", exempt, got, want)
			}
		})
	}
}

func TestExemptionCannotBorrowWithoutSessionIdentity(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	if got := svc.Pick(borrowingRequest("")); got.AuthID != "a" {
		t.Fatalf("no stable session means primary-only allocation: %+v", got)
	}
}

func TestExemptionCannotBorrowWithoutHostPickIdentity(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	req := borrowingRequest("stable-session")
	req.Options.Headers = nil
	if got := svc.Pick(req); got.AuthID != "a" {
		t.Fatalf("missing host pick identity must exclude new borrowing: %+v", got)
	}
}

func TestBorrowTargetsMustBeEnabledPoolMembersWithFiniteLimits(t *testing.T) {
	for _, mode := range []string{"disabled-pool", "disabled-member", "unlimited", "absent", "window-full"} {
		t.Run(mode, func(t *testing.T) {
			svc, now := borrowingService(t, true)
			p := svc.Policy()
			p.Version++
			switch mode {
			case "disabled-pool":
				for i := range p.Pools {
					if p.Pools[i].ID == "other" {
						p.Pools[i].Enabled = false
					}
				}
			case "disabled-member":
				for i := range p.Members {
					if p.Members[i].AuthID == "b" {
						p.Members[i].Enabled = false
					}
				}
			case "unlimited", "absent":
				limits := map[string]Limit{"a": {Max: 5}}
				if mode == "unlimited" {
					limits["b"] = Limit{}
				}
				svc.engine.Configure(limits)
			case "window-full":
				svc.engine.Configure(map[string]Limit{"a": {Max: 5}, "b": {Max: 5, Max15s: 1, Window: time.Minute}})
			}
			publishBorrowingPolicy(t, svc, p)
			*now = now.Add(time.Minute)
			if mode == "window-full" {
				ids := holdAccount(t, svc.engine, "b", 1)
				svc.Complete(ids[0])
			}
			holdAccount(t, svc.engine, "a", 5)
			if got := svc.Pick(borrowingRequest("new")); got.AuthID != "a" {
				t.Fatalf("ineligible target %s must not be used: %+v", mode, got)
			}
		})
	}
}

func TestBorrowRecoveryRequiresFifteenSecondsWithoutPressure(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	holdAccount(t, svc.engine, "a", 5)
	foreignHolds := holdAccount(t, svc.engine, "b", 5)
	*now = now.Add(time.Second)
	for _, id := range foreignHolds {
		svc.Complete(id)
	}
	if got := svc.Pick(borrowingRequest("recent-full")); got.AuthID != "a" {
		t.Fatalf("low average alone cannot override recent saturation: %+v", got)
	}
	*now = now.Add(15 * time.Second)
	if got := svc.Pick(borrowingRequest("recovered")); got.AuthID != "b" {
		t.Fatalf("recovered low-load target should be usable: %+v", got)
	}
}

func TestLosingProviderLayerDoesNotRetainReservation(t *testing.T) {
	svc, now := borrowingService(t, true)
	*now = now.Add(time.Minute)
	req := borrowingRequest("layered")
	req.Candidates = append(req.Candidates, schedulerAuthCandidate{
		ID: "codex:apikey:test", Priority: 100,
		Attributes: map[string]string{"auth_kind": "apikey"},
	})
	if got := svc.Pick(req); got.AuthID != "codex:apikey:test" {
		t.Fatalf("provider priority should remain unchanged: %+v", got)
	}
	if state := svc.StateSnapshot("a"); state.Reserved != 0 {
		t.Fatalf("unused OAuth pick must release its reservation: %+v", state)
	}
	if state := svc.StateSnapshot("codex:apikey:test"); state.Reserved != 1 {
		t.Fatalf("winning pick must retain its reservation before admission: %+v", state)
	}
}
