package auth

import (
	"context"
	"errors"
	"slices"
	"testing"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// exclusiveClaimGateScheduler reports no serving record (broken/accidental
// state) while a live exclusive claim still names the provider, mirroring the
// pluginhost Host during an accidental scheduler loss.
type exclusiveClaimGateScheduler struct {
	fakePluginScheduler
	claim string
}

func (s *exclusiveClaimGateScheduler) HasScheduler() bool { return false }

func (s *exclusiveClaimGateScheduler) SchedulerExcludesFastPath(provider string) bool {
	return provider == s.claim
}

// TestManagerExclusiveClaimWithoutServingRecordFailsClosed locks the P0-2
// contract: an accidental scheduler loss under a live claim must route through
// PickAuth and surface its fail-closed error, never fall through to the
// native fast path selecting an account globally.
func TestManagerExclusiveClaimWithoutServingRecordFailsClosed(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	scheduler := &exclusiveClaimGateScheduler{claim: "codex"}
	scheduler.handled = true
	scheduler.err = &Error{Code: "policy_unavailable", Message: "exclusive scheduler policy is unavailable", Retryable: true, HTTPStatus: 503}
	manager.SetPluginScheduler(scheduler)

	got, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick == nil {
		t.Fatalf("pickNext() auth = %v, error = nil; want fail-closed error (no global fallback)", got)
	}
	if got != nil {
		t.Fatalf("pickNext() returned auth %q under a live claim with no serving record", got.ID)
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d, want 1 (claim forces the legacy PickAuth path)", scheduler.calls)
	}
}

// TestManagerReleasedClaimKeepsFastPath locks the deliberate-release half:
// when the claim is gone (pool disabled or declaration removed) and no record
// serves, the conductor must restore native default scheduling.
func TestManagerReleasedClaimKeepsFastPath(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-a", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(auth-a) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-b", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(auth-b) error = %v", errRegister)
	}

	scheduler := &exclusiveClaimGateScheduler{claim: ""}
	manager.SetPluginScheduler(scheduler)

	gotA, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() first error = %v", errPick)
	}
	gotB, _, errPick := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext() second error = %v", errPick)
	}
	if gotA == nil || gotB == nil || gotA.ID != "auth-a" || gotB.ID != "auth-b" {
		t.Fatalf("fast path picks = %v, %v; want auth-a, auth-b", gotA, gotB)
	}
	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d, want 0 (released claim uses native scheduling)", scheduler.calls)
	}
}

// TestManagerExclusiveClaimGateIsProviderScoped verifies a claim on one
// provider does not disable the fast path for unrelated providers.
func TestManagerExclusiveClaimGateIsProviderScoped(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["codex"] = schedulerTestExecutor{}
	manager.executors["gemini"] = schedulerTestExecutor{}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-codex", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register(auth-codex) error = %v", errRegister)
	}
	if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-gemini", Provider: "gemini"}); errRegister != nil {
		t.Fatalf("Register(auth-gemini) error = %v", errRegister)
	}

	scheduler := &exclusiveClaimGateScheduler{claim: "codex"}
	scheduler.handled = true
	scheduler.err = &Error{Code: "policy_unavailable", Message: "exclusive scheduler policy is unavailable", Retryable: true, HTTPStatus: 503}
	manager.SetPluginScheduler(scheduler)

	// Unclaimed provider keeps the native fast path and never reaches PickAuth.
	got, _, errPick := manager.pickNext(context.Background(), "gemini", "", cliproxyexecutor.Options{}, nil)
	if errPick != nil {
		t.Fatalf("pickNext(gemini) error = %v", errPick)
	}
	if got == nil || got.ID != "auth-gemini" {
		t.Fatalf("pickNext(gemini) = %v, want auth-gemini", got)
	}
	if scheduler.calls != 0 {
		t.Fatalf("scheduler.calls = %d for unclaimed provider, want 0", scheduler.calls)
	}

	// Claimed provider still fail-closes through PickAuth.
	if _, _, errClaimed := manager.pickNext(context.Background(), "codex", "", cliproxyexecutor.Options{}, nil); errClaimed == nil {
		t.Fatalf("pickNext(codex) error = nil, want fail-closed policy_unavailable")
	}
	if scheduler.calls != 1 {
		t.Fatalf("scheduler.calls = %d after claimed pick, want 1", scheduler.calls)
	}
}

func TestManagerExclusiveClaimGateSelectionPaths(t *testing.T) {
	for _, fastPath := range []bool{true, false} {
		pathName := "fast-path"
		if !fastPath {
			pathName = "custom-selector"
		}
		for _, claim := range []string{"codex", ""} {
			claimName := "claimed"
			if claim == "" {
				claimName = "released"
			}
			for _, tc := range []struct {
				name      string
				providers []string
				mixed     bool
			}{
				{name: "single-codex", providers: []string{"codex"}},
				{name: "single-unclaimed", providers: []string{"gemini"}},
				{name: "mixed-claimed-first", providers: []string{"codex", "gemini"}, mixed: true},
				{name: "mixed-claimed-last", providers: []string{"gemini", "codex"}, mixed: true},
				{name: "mixed-unclaimed", providers: []string{"gemini"}, mixed: true},
			} {
				t.Run(pathName+"/"+claimName+"/"+tc.name, func(t *testing.T) {
					var selector Selector = &RoundRobinSelector{}
					customSelector := &trackingSelector{}
					if !fastPath {
						selector = customSelector
					}
					manager := NewManager(nil, selector, nil)
					for _, provider := range []string{"codex", "gemini"} {
						manager.executors[provider] = schedulerTestExecutor{}
						if _, errRegister := manager.Register(context.Background(), &Auth{ID: "auth-" + provider, Provider: provider}); errRegister != nil {
							t.Fatalf("Register(%s) error = %v", provider, errRegister)
						}
					}
					if manager.useSchedulerFastPath() != fastPath {
						t.Fatalf("useSchedulerFastPath() = %v, want %v", manager.useSchedulerFastPath(), fastPath)
					}
					scheduler := &exclusiveClaimGateScheduler{claim: claim}
					scheduler.handled = true
					scheduler.err = &Error{Code: "policy_unavailable", Message: "exclusive scheduler policy is unavailable", Retryable: true, HTTPStatus: 503}
					manager.SetPluginScheduler(scheduler)
					opts := cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.SelectionIDMetadataKey: "stale-selection"}}
					var got *Auth
					var errPick error
					if tc.mixed {
						got, _, _, errPick = manager.pickNextMixed(context.Background(), tc.providers, "", opts, nil)
					} else {
						got, _, errPick = manager.pickNext(context.Background(), tc.providers[0], "", opts, nil)
					}
					claimed := false
					for _, provider := range tc.providers {
						claimed = claimed || provider == claim
					}
					if claimed {
						if got != nil || !errors.Is(errPick, scheduler.err) {
							t.Fatalf("pick() = %v, %v; want nil, policy_unavailable", got, errPick)
						}
						if scheduler.calls != 1 || customSelector.calls != 0 {
							t.Fatalf("plugin calls = %d, selector calls = %d; want 1, 0", scheduler.calls, customSelector.calls)
						}
					} else {
						if errPick != nil || got == nil || !slices.Contains(tc.providers, got.Provider) {
							t.Fatalf("pick() = %v, %v; want native selection from %v", got, errPick, tc.providers)
						}
						if scheduler.calls != 0 {
							t.Fatalf("plugin calls = %d without a relevant claim, want 0", scheduler.calls)
						}
					}
					if _, exists := opts.Metadata[cliproxyexecutor.SelectionIDMetadataKey]; exists {
						t.Fatal("failed or native pick retained a plugin selection ID")
					}
				})
			}
		}
	}
}

// compile-time assertions: the gate fake must satisfy both optional contracts.
var (
	_ pluginSchedulerState            = (*exclusiveClaimGateScheduler)(nil)
	_ PluginSchedulerExcludesFastPath = (*exclusiveClaimGateScheduler)(nil)
	_ PluginScheduler                 = (*exclusiveClaimGateScheduler)(nil)
)

var _ = pluginapi.SchedulerPickResponse{}
