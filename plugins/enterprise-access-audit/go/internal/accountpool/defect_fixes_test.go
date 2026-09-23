package accountpool

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

// TestSessionKeyIsolatesCallersAndLayers locks the P0-1 fix: two callers (or
// two routing layers) reusing the same conversation id must never share one
// sessions-map entry.
func TestSessionKeyIsolatesCallersAndLayers(t *testing.T) {
	headers := map[string][]string{"x-session-id": {"shared-conv"}}

	nsA := nsJoin("pool", "eng", "v3", "aaaaaaaa")
	nsB := nsJoin("pool", "ops", "v3", "bbbbbbbb")
	reqA := withSessionNamespace(schedulerPickRequest{Options: schedulerOptions{Headers: headers, Metadata: map[string]any{metadataKeyHash: "aaaaaaaa"}}}, nsA)
	reqB := withSessionNamespace(schedulerPickRequest{Options: schedulerOptions{Headers: headers, Metadata: map[string]any{metadataKeyHash: "bbbbbbbb"}}}, nsB)

	keyA := sessionKey(reqA)
	keyB := sessionKey(reqB)
	if keyA == "" || keyA == keyB {
		t.Fatalf("session keys must be namespaced per caller/pool: %q vs %q", keyA, keyB)
	}
	if want := nsA + "|shared-conv"; keyA != want {
		t.Fatalf("key A = %q, want %q", keyA, want)
	}

	// The api-key layer with the same conversation id must differ from both.
	reqK := withSessionNamespace(schedulerPickRequest{Options: schedulerOptions{Headers: headers, Metadata: map[string]any{metadataKeyHash: "aaaaaaaa"}}}, nsJoin("apikey", "aaaaaaaa"))
	if keyK := sessionKey(reqK); keyK == keyA {
		t.Fatalf("api-key layer session key must not collide with OAuth layer: %q", keyK)
	}

	// Admission-side derivation must reproduce the Pick-side key byte for byte.
	admitKey := sessionKeyFrom(headers, map[string]any{metadataKeyHash: "aaaaaaaa"}, nsA)
	if admitKey != keyA {
		t.Fatalf("admit key %q != pick key %q", admitKey, keyA)
	}
}

// TestAdmissionNamespaceMirrorsPick verifies the service-computed namespace is
// identical on both sides for the bound, unbound, and api-key paths.
func TestAdmissionNamespaceMirrorsPick(t *testing.T) {
	p := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}},
		[]Member{{PoolID: "eng", AuthID: "auth-a", Priority: 0, Enabled: true}},
		[]Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}},
	)
	svc := newLayeredService(t, true, &p)
	meta := map[string]any{metadataKeyHash: "abcd1234"}

	// Bound OAuth: namespace must embed pool id and policy version.
	ns := svc.admissionNamespace("auth-a", meta)
	if want := nsJoin("pool", "eng", fmt.Sprintf("v%d", p.Version), "abcd1234"); ns != want {
		t.Fatalf("bound admission ns = %q, want %q", ns, want)
	}
	// Pick side computes the same value when building the scoped request.
	if pickNS := oauthNamespace(true, true, p, "eng", "abcd1234"); pickNS != ns {
		t.Fatalf("pick ns %q != admit ns %q", pickNS, ns)
	}

	// Unbound caller.
	unbound := svc.admissionNamespace("auth-a", map[string]any{metadataKeyHash: "99999999"})
	if want := nsJoin("oauth-glob", "99999999"); unbound != want {
		t.Fatalf("unbound admission ns = %q, want %q", unbound, want)
	}

	// Api-key layer selected auth id.
	apiKeyNS := svc.admissionNamespace("codex:apikey:2327d997c704", meta)
	if want := nsJoin("apikey", "abcd1234"); apiKeyNS != want {
		t.Fatalf("api-key admission ns = %q, want %q", apiKeyNS, want)
	}
}

// TestPutLimitsTrimsAuthIDEverywhere locks the P1-6 fix: a spaced auth id must
// be persisted trimmed so it survives restart as a configured account.
func TestPutLimitsTrimsAuthIDEverywhere(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir(), Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	if err := svc.PutLimits([]AccountLimit{{AuthID: "  auth-spaced  ", Limit: 3, WindowSeconds: 15}}); err != nil {
		t.Fatalf("PutLimits() error = %v", err)
	}
	// In-memory view is keyed trimmed; the stored field must match it.
	found := false
	for _, item := range svc.LimitsView() {
		if item.AuthID == "auth-spaced" {
			found = true
		}
		if item.AuthID != trimSpaceASCII(item.AuthID) {
			t.Fatalf("LimitsView authId %q still contains spaces", item.AuthID)
		}
	}
	if !found {
		t.Fatalf("trimmed auth id missing from LimitsView: %#v", svc.LimitsView())
	}
	// A fresh reload from disk must still see the account as configured.
	fresh := New(Options{DataDir: svc.persist.dataDir, Enabled: true})
	if err := fresh.Reload(); err != nil {
		t.Fatalf("reload fresh() error = %v", err)
	}
	limits := fresh.LimitsView()
	alive := false
	for _, item := range limits {
		if item.AuthID == "auth-spaced" && item.Limit == 3 {
			alive = true
		}
	}
	if !alive {
		t.Fatalf("limit lost after restart: %#v", limits)
	}
}

func trimSpaceASCII(value string) string {
	start, end := 0, len(value)
	for start < end && value[start] == ' ' {
		start++
	}
	for end > start && value[end-1] == ' ' {
		end--
	}
	return value[start:end]
}

// TestApplyConcurrentVersionsNeverRegress locks the P1-7 fix: concurrent
// publishers cannot let an older version overwrite a newer one, and memory and
// disk must end in the same state.
func TestApplyConcurrentVersionsNeverRegress(t *testing.T) {
	dir := t.TempDir()
	svc := New(Options{DataDir: dir, Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	base := mustPolicy(t,
		[]Pool{{ID: "eng", Name: "Engineering", Enabled: true}},
		nil, nil,
	)

	const writers = 8
	var wg sync.WaitGroup
	for i := 1; i <= writers; i++ {
		wg.Add(1)
		go func(version int64) {
			defer wg.Done()
			next := base
			next.Version = version
			next.Hash = ""
			raw, errMarshal := json.Marshal(Envelope{Policy: next})
			if errMarshal != nil {
				return
			}
			_, _ = svc.Apply(raw)
		}(int64(i))
	}
	wg.Wait()

	status := svc.Status()
	if int64(status.Version) != int64(writers) {
		t.Fatalf("final version = %d, want %d (highest writer wins)", status.Version, writers)
	}
	// Disk must agree with memory: reload a fresh service and compare.
	fresh := New(Options{DataDir: dir, Enabled: true})
	if err := fresh.Reload(); err != nil {
		t.Fatalf("fresh Reload() error = %v", err)
	}
	if fresh.Status().Version != status.Version {
		t.Fatalf("disk version %d != memory version %d", fresh.Status().Version, status.Version)
	}
}

// TestReconfigurePreservesLiveEngineState locks the P1-5 fix: a hot
// reconfigure must keep active counters and session bindings.
func TestReconfigurePreservesLiveEngineState(t *testing.T) {
	dir := t.TempDir()
	svc := New(Options{DataDir: dir, Reserve: time.Second, MaxWait: time.Second, Enabled: true})
	if err := svc.Reload(); err != nil {
		t.Fatalf("Reload() error = %v", err)
	}
	// Occupy the account through admission (no limit => admitted, active++).
	admit := svc.AdmitIntercept("req-live", nil, map[string]any{metadataSelectedAuthID: "auth-a"})
	if admit != nil {
		t.Fatalf("admit should pass on unlimited account: %+v", admit)
	}
	// Create a sticky session via pick.
	req := layeredRequest("abcd1234", oauthCandidate("auth-a", 0))
	if resp := svc.Pick(req); resp.Decision != "selected" {
		t.Fatalf("pick = %+v", resp)
	}
	before := svc.StateSnapshot("auth-a")
	if before.Active != 1 {
		t.Fatalf("pre-reconfigure active = %d, want 1", before.Active)
	}

	if err := svc.Reconfigure(Options{DataDir: dir, Reserve: 2 * time.Second, MaxWait: 2 * time.Second, MaxBusy: 5, Enabled: true}); err != nil {
		t.Fatalf("Reconfigure() error = %v", err)
	}
	after := svc.StateSnapshot("auth-a")
	if after.Active != 1 {
		t.Fatalf("post-reconfigure active = %d, want 1 (counters must survive)", after.Active)
	}
	// Session binding must survive: the same session picks the same account.
	if resp := svc.Pick(req); resp.AuthID != "auth-a" {
		t.Fatalf("session binding lost after reconfigure: %+v", resp)
	}
	// Completion still releases the pre-reconfigure admission.
	svc.Complete("req-live")
	if done := svc.StateSnapshot("auth-a"); done.Active != 0 {
		t.Fatalf("active after Complete = %d, want 0", done.Active)
	}
}
