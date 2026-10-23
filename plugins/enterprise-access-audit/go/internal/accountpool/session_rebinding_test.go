package accountpool

import (
	"strings"
	"testing"
	"time"
)

func rebindRequest(callerHash, session string) schedulerPickRequest {
	return schedulerPickRequest{
		Provider: ProviderCodex,
		Options: schedulerOptions{Metadata: map[string]any{
			metadataKeyHash: callerHash,
			"session_id":    session,
		}},
		Candidates: []schedulerAuthCandidate{oauthCandidate("auth-a", 0), oauthCandidate("auth-b", 0)},
	}
}

func applyRebindPolicy(t *testing.T, svc *Service, next Policy) {
	t.Helper()
	next.Version = svc.Policy().Version + 1
	publishBorrowingPolicy(t, svc, next)
}

func TestServicePickRebindsSessionWhenCallerMovesPools(t *testing.T) {
	callerHash := strings.Repeat("a", 64)
	initial := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
	)
	svc := newLayeredService(t, true, &initial)
	request := rebindRequest(callerHash, "caller-moved-pools")

	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a from pool-a", got)
	}

	next := mustPolicy(t,
		initial.Pools,
		initial.Members,
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b"}},
	)
	applyRebindPolicy(t, svc, next)

	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-b" {
		t.Fatalf("pick after caller pool rebind = %+v, want auth-b from pool-b", got)
	}
	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-b" {
		t.Fatalf("subsequent pick = %+v, want session to stay on auth-b", got)
	}
}

func TestServicePoolPublicationWaitsForInFlightPick(t *testing.T) {
	callerHash := strings.Repeat("5", 64)
	initial := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
	)
	svc := newLayeredService(t, true, &initial)
	entered, release := make(chan struct{}), make(chan struct{})
	lookups := 0
	svc.engine.cfg.Session = func(request schedulerPickRequest) string {
		lookups++
		// The first lookup is the idle-clock touch before policy selection;
		// the second runs inside the policy-based engine pick.
		if lookups == 2 {
			close(entered)
			<-release
		}
		return sessionKey(request)
	}
	request := rebindRequest(callerHash, "publication-order")
	oldResult := make(chan SchedulerPickResponse, 1)
	go func() { oldResult <- svc.Pick(withPickID(request, "old-pick")) }()
	<-entered
	// Publication needs the service write lock. It must not overtake a pick
	// after that pick has read its policy but before it writes the affinity.
	publicationCouldOvertake := svc.mu.TryLock()
	if publicationCouldOvertake {
		svc.mu.Unlock()
	}
	close(release)
	if got := <-oldResult; got.AuthID != "auth-a" {
		t.Fatalf("old pick = %+v, want auth-a", got)
	}
	if publicationCouldOvertake {
		t.Fatal("policy publication can overtake an in-flight policy-based pick")
	}
	next := mustPolicy(t, initial.Pools, initial.Members,
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b"}})
	applyRebindPolicy(t, svc, next)
	for _, id := range []string{"new-pick", "new-pick-retry"} {
		if got := svc.Pick(withPickID(request, id)); got.AuthID != "auth-b" {
			t.Fatalf("%s = %+v, want stable auth-b affinity", id, got)
		}
	}
}

func TestServiceCallerPoolChangeKeepsAccountStillInNewPool(t *testing.T) {
	callerHash := strings.Repeat("6", 64)
	initial := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
	)
	svc := newLayeredService(t, true, &initial)
	request := rebindRequest(callerHash, "shared-auth-pool-change")
	if got := svc.Pick(withPickID(request, "old-pool")); got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a", got)
	}
	next := mustPolicy(t, initial.Pools, initial.Members,
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b"}})
	applyRebindPolicy(t, svc, next)
	if got := svc.Pick(withPickID(request, "new-pool")); got.AuthID != "auth-a" {
		t.Fatalf("shared auth after pool change = %+v, want original auth-a still in pool-b", got)
	}
}

func TestServicePickRebindsSessionWhenAccountMovesPools(t *testing.T) {
	callerHash := strings.Repeat("b", 64)
	initial := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
	)
	svc := newLayeredService(t, true, &initial)
	request := rebindRequest(callerHash, "account-moved-pools")

	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a from pool-a", got)
	}

	next := mustPolicy(t,
		initial.Pools,
		[]Member{{PoolID: "pool-a", AuthID: "auth-b", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-a", Enabled: true}},
		initial.Bindings,
	)
	applyRebindPolicy(t, svc, next)

	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-b" {
		t.Fatalf("pick after auth pool move = %+v, want auth-b from pool-a", got)
	}
}

func TestServicePoolRebindDoesNotMigrateInFlightRequest(t *testing.T) {
	callerHash := strings.Repeat("c", 64)
	initial := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
	)
	svc := newLayeredService(t, true, &initial)
	request := rebindRequest(callerHash, "in-flight-rebind")
	oldPick := withPickID(request, "old-request")
	if got := svc.Pick(oldPick); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a", got)
	}
	next := mustPolicy(t,
		initial.Pools,
		initial.Members,
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b"}},
	)
	applyRebindPolicy(t, svc, next)

	newPick := withPickID(request, "new-request")
	if got := svc.Pick(newPick); got.Decision != "selected" || got.AuthID != "auth-b" {
		t.Fatalf("new request pick = %+v, want auth-b", got)
	}
	newMetadata := map[string]any{
		metadataKeyHash:        callerHash,
		metadataSelectedAuthID: "auth-b",
		"session_id":           "in-flight-rebind",
	}
	if rejected := svc.AdmitIntercept("new-request", newPick.Options.Headers, newMetadata); rejected != nil {
		t.Fatalf("admit new request = %+v", rejected)
	}
	// The old pick is already in flight across the policy change, even though
	// after-auth admission arrives later. It keeps its original account.
	oldMetadata := map[string]any{
		metadataKeyHash:        callerHash,
		metadataSelectedAuthID: "auth-a",
		"session_id":           "in-flight-rebind",
	}
	if rejected := svc.AdmitIntercept("old-request", oldPick.Options.Headers, oldMetadata); rejected != nil {
		t.Fatalf("admit old in-flight request = %+v", rejected)
	}
	if got := svc.StateSnapshot("auth-a").Active; got != 1 {
		t.Fatalf("auth-a active after rebind = %d, want in-flight request to remain active", got)
	}
	if got := svc.StateSnapshot("auth-b").Active; got != 1 {
		t.Fatalf("auth-b active after rebind = %d, want new request admitted", got)
	}

	svc.Complete("old-request")
	if got := svc.StateSnapshot("auth-a").Active; got != 0 {
		t.Fatalf("auth-a active after completing old request = %d, want 0", got)
	}
	if got := svc.StateSnapshot("auth-b").Active; got != 1 {
		t.Fatalf("auth-b active after completing old request = %d, want new request unaffected", got)
	}
	if got := svc.Pick(withPickID(request, "third-request")); got.Decision != "selected" || got.AuthID != "auth-b" {
		t.Fatalf("pick after old completion = %+v, want session to remain on auth-b", got)
	}
	svc.Complete("new-request")
}

func TestServiceOldAdmissionDoesNotChangeReboundBusyStreak(t *testing.T) {
	for _, busy := range []bool{false, true} {
		name := "old-admission-succeeds"
		if busy {
			name = "old-admission-times-out"
		}
		t.Run(name, func(t *testing.T) {
			callerHash := strings.Repeat("8", 64)
			current := time.Unix(1_700_000_000, 0)
			initial := mustPolicy(t,
				[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
				[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
				[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
			)
			svc := newLayeredService(t, true, &initial)
			svc.engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
			request := rebindRequest(callerHash, "late-old-admission")
			if got := svc.Pick(request); got.AuthID != "auth-a" {
				t.Fatalf("old pick = %+v, want auth-a", got)
			}
			next := mustPolicy(t, initial.Pools, initial.Members,
				[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b"}})
			applyRebindPolicy(t, svc, next)
			if got := svc.Pick(request); got.AuthID != "auth-b" {
				t.Fatalf("new pick = %+v, want auth-b", got)
			}
			key := sessionKey(withSessionNamespace(request, oauthNamespace(true, true, "pool-b", callerHash)))
			svc.engine.busyRejections[key] = 2
			if busy {
				if err := svc.PutLimits([]AccountLimit{{AuthID: "auth-a", Limit: 1}}); err != nil {
					t.Fatal(err)
				}
				if _, _, _, ok := svc.engine.Admit("holder", "auth-a", "other-session"); !ok {
					t.Fatal("failed to hold auth-a")
				}
			}
			_, _, _, admitted := svc.engine.Admit("old-request", "auth-a", key)
			if admitted == busy {
				t.Fatalf("old admission success = %v, busy = %v", admitted, busy)
			}
			if got := svc.engine.busyRejections[key]; got != 2 {
				t.Fatalf("old auth-a admission changed auth-b busy streak to %d, want 2", got)
			}
			svc.Complete("old-request")
			svc.Complete("holder")
		})
	}
}

func TestServicePoolRebindDoesNotBorrowFromOldPool(t *testing.T) {
	callerHash := strings.Repeat("e", 64)
	current := time.Unix(1_700_000_000, 0)
	policy := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a", CrossPoolExempt: true}},
	)
	svc := newLayeredService(t, true, &policy)
	svc.engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	if err := svc.PutLimits([]AccountLimit{{AuthID: "auth-a", Limit: 5}, {AuthID: "auth-b", Limit: 5}}); err != nil {
		t.Fatal(err)
	}
	request := rebindRequest(callerHash, "caller-move-no-borrow")
	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a", got)
	}
	current = current.Add(time.Minute)
	holds := holdAccount(t, svc.engine, "auth-b", 5)

	next := mustPolicy(t,
		policy.Pools,
		policy.Members,
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b", CrossPoolExempt: true}},
	)
	applyRebindPolicy(t, svc, next)
	afterRebind := svc.Pick(withPickID(request, "after-rebind"))
	if afterRebind.Decision != "selected" || afterRebind.AuthID != "auth-b" {
		t.Fatalf("pick after rebind = %+v, want current primary auth-b (queued if saturated), never old-pool auth-a", afterRebind)
	}
	svc.Complete("after-rebind")
	for _, id := range holds {
		svc.Complete(id)
	}
}

func TestServicePoolRebindRetriesCannotBorrowOldPool(t *testing.T) {
	callerHash := strings.Repeat("7", 64)
	current := time.Unix(1_700_000_000, 0)
	initial := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a", CrossPoolExempt: true}},
	)
	svc := newLayeredService(t, true, &initial)
	svc.engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	if err := svc.PutLimits([]AccountLimit{{AuthID: "auth-a", Limit: 5}, {AuthID: "auth-b", Limit: 5}}); err != nil {
		t.Fatal(err)
	}
	request := rebindRequest(callerHash, "rebind-retries")
	if got := svc.Pick(withPickID(request, "initial")); got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a", got)
	}
	current = current.Add(time.Minute)
	next := mustPolicy(t, initial.Pools, initial.Members,
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-b", CrossPoolExempt: true}})
	applyRebindPolicy(t, svc, next)

	request.Candidates = []schedulerAuthCandidate{oauthCandidate("auth-a", 0)}
	for _, id := range []string{"first-rebind", "retry-rebind"} {
		if got := svc.Pick(withPickID(request, id)); got.Decision != "reject" || got.ErrorCode != "account_pool_unavailable" {
			t.Fatalf("%s = %+v, want no-primary rejection, never old-pool borrowing", id, got)
		}
	}
	request.Candidates = rebindRequest(callerHash, "").Candidates
	if got := svc.Pick(withPickID(request, "recovered-primary")); got.AuthID != "auth-b" {
		t.Fatalf("pick after primary recovery = %+v, want auth-b", got)
	}
}

func TestBorrowedAffinitySurvivesUnrelatedMembershipEdits(t *testing.T) {
	for _, pools := range [][]string{{"lender", "extra"}, {"lender"}} {
		selection := poolSelection{
			poolID: "home", primary: map[string]struct{}{"auth-a": {}},
			memberPools: map[string][]string{"auth-b": pools},
		}
		binding := sessionBinding{AuthID: "auth-b", PoolID: "home", Borrowed: true,
			MemberPoolIDs: []string{"lender", "original-extra"}}
		if selection.needsRebind(binding) {
			t.Fatalf("borrowed auth still belongs to original lender: current memberships = %v", pools)
		}
	}
}

func TestServiceBorrowedSessionRebindsWhenBorrowedAccountMovesPools(t *testing.T) {
	callerHash := strings.Repeat("f", 64)
	current := time.Unix(1_700_000_000, 0)
	initial := mustPolicy(t,
		[]Pool{{ID: "home", Name: "Home", Enabled: true}, {ID: "other", Name: "Other", Enabled: true}, {ID: "third", Name: "Third", Enabled: true}},
		[]Member{{PoolID: "home", AuthID: "auth-a", Enabled: true}, {PoolID: "other", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "home", CrossPoolExempt: true}},
	)
	svc := newLayeredService(t, true, &initial)
	svc.engine.SetClock(func() time.Time { return current }, func(d time.Duration) { current = current.Add(d) })
	if err := svc.PutLimits([]AccountLimit{{AuthID: "auth-a", Limit: 5}, {AuthID: "auth-b", Limit: 5}}); err != nil {
		t.Fatal(err)
	}
	warm := withPickID(rebindRequest(callerHash, "warm-session"), "warm-request")
	if got := svc.Pick(warm); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("warm pick = %+v, want auth-a", got)
	}
	current = current.Add(time.Minute)
	holds := holdAccount(t, svc.engine, "auth-a", 5)
	borrowed := withPickID(rebindRequest(callerHash, "borrowed-account-move"), "borrowed-request")
	if got := svc.Pick(borrowed); got.Decision != "selected" || got.AuthID != "auth-b" {
		t.Fatalf("initial borrowed pick = %+v, want auth-b", got)
	}

	next := mustPolicy(t,
		[]Pool{{ID: "home", Name: "Home", Enabled: true}, {ID: "other", Name: "Other", Enabled: true}, {ID: "third", Name: "Third", Enabled: true}},
		[]Member{{PoolID: "home", AuthID: "auth-a", Enabled: true}, {PoolID: "third", AuthID: "auth-b", Enabled: true}},
		initial.Bindings,
	)
	applyRebindPolicy(t, svc, next)
	for _, id := range holds {
		svc.Complete(id)
	}
	if got := svc.Pick(withPickID(borrowed, "after-account-move")); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("pick after borrowed account moved pools = %+v, want current primary auth-a", got)
	}
}

func TestServicePickReportsBoundAuthMissingFromEligibleCandidates(t *testing.T) {
	callerHash := strings.Repeat("9", 64)
	policy := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a"}},
	)
	svc := newLayeredService(t, true, &policy)
	request := rebindRequest(callerHash, "primary-auth-temporarily-filtered")
	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("initial pick = %+v, want auth-a", got)
	}

	// auth-a remains a configured pool member but is absent from this request's
	// CPA-eligible candidates (for example, because its model is cooling).
	for _, candidates := range [][]schedulerAuthCandidate{
		{oauthCandidate("auth-b", 0)},
		{oauthCandidate("outside-policy", 0)},
		nil,
	} {
		request.Candidates = candidates
		got := svc.Pick(request)
		if got.Decision != "reject" || got.ErrorCode != "account_pool_unavailable" {
			t.Fatalf("pick without the bound candidate = %+v, want fail-closed rejection", got)
		}
		if !strings.Contains(got.Reason, "not an eligible candidate") {
			t.Fatalf("candidates = %+v, reason = %q, want honest candidate-eligibility diagnosis", candidates, got.Reason)
		}
	}

	request.Candidates = []schedulerAuthCandidate{oauthCandidate("auth-a", 0), oauthCandidate("auth-b", 0)}
	if got := svc.Pick(request); got.Decision != "selected" || got.AuthID != "auth-a" {
		t.Fatalf("pick after bound auth becomes eligible again = %+v, want original auth-a affinity", got)
	}
}

func TestServicePickNewSessionDoesNotBorrowWhenPrimaryHasNoCandidates(t *testing.T) {
	callerHash := strings.Repeat("d", 64)
	policy := mustPolicy(t,
		[]Pool{{ID: "pool-a", Name: "Pool A", Enabled: true}, {ID: "pool-b", Name: "Pool B", Enabled: true}},
		[]Member{{PoolID: "pool-a", AuthID: "auth-a", Enabled: true}, {PoolID: "pool-b", AuthID: "auth-b", Enabled: true}},
		[]Binding{{APIKeyHash: callerHash, PoolID: "pool-a", CrossPoolExempt: true}},
	)
	svc := newLayeredService(t, true, &policy)
	request := rebindRequest(callerHash, "new-user-no-primary-candidate")
	for _, candidates := range [][]schedulerAuthCandidate{
		{oauthCandidate("auth-b", 0)},
		{oauthCandidate("outside-policy", 0)},
		nil,
	} {
		request.Candidates = candidates
		got := svc.Pick(request)
		if got.Decision != "reject" || got.ErrorCode != "account_pool_unavailable" {
			t.Fatalf("pick with no pool-a candidate = %+v, want rejection instead of foreign-pool access", got)
		}
		if !strings.Contains(got.Reason, "assigned account pool") || strings.Contains(got.Reason, "bound account") {
			t.Fatalf("candidates = %+v, new-session rejection reason = %q, want an assigned-pool candidate explanation", candidates, got.Reason)
		}
	}
}
