package modeltrace

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func testCredential(index string) Credential {
	return Credential{Index: index, ID: index, Identity: "account:" + index}
}
func testPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "modeltrace-state.json")
}
func TestModelTraceAdmissionCancellationAndRestart(t *testing.T) {
	path := testPath(t)
	entered := make(chan struct{}, 6)
	var calls atomic.Int32
	s := New(path, func(ctx context.Context, c Credential, model string, ch Challenge) (Output, error) {
		calls.Add(1)
		entered <- struct{}{}
		<-ctx.Done()
		return Output{InputTokens: 12}, ctx.Err()
	})
	p, err := s.Start(testCredential("a"), "gpt-5.5")
	if err != nil {
		t.Fatal(err)
	}
	if p.Total != 3 || p.ID == "" {
		t.Fatal(p)
	}
	if _, err = s.Start(testCredential("a"), "gpt-5.5"); ErrorCode(err) != 409 {
		t.Fatal(err)
	}
	if _, err = s.Start(testCredential("b"), "gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	for range 6 {
		<-entered
	}
	if _, err = s.Start(testCredential("c"), "gpt-5.5"); ErrorCode(err) != 429 {
		t.Fatal(err)
	}
	// A second reader simulates restart while the first process has an on-disk marker.
	restarted := New(path, nil)
	r, ok := restarted.Record(testCredential("a"), p.ID)
	if !ok || r.Status != "interrupted" || r.Verdict != "inconclusive" {
		t.Fatal(r)
	}
	if calls.Load() != 6 {
		t.Fatal("restart issued execution")
	}
	if !s.Cancel(testCredential("a")) {
		t.Fatal("not cancelled")
	}
	if err = s.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	r, ok = s.Record(testCredential("a"), "")
	if !ok || r.Status != "cancelled" || r.InputTokens != 36 {
		t.Fatal(r)
	}
	if _, err = s.Start(testCredential("c"), "gpt-5.5"); ErrorCode(err) != 503 {
		t.Fatal(err)
	}
}
func TestModelTraceHistorySummaryIdentityAndUsage(t *testing.T) {
	path := testPath(t)
	s := New(path, func(ctx context.Context, c Credential, model string, ch Challenge) (Output, error) {
		return Output{Text: strings.Repeat("42,", 320), InputTokens: 12, OutputTokens: 3, ReasoningTokens: 1}, nil
	})
	c := testCredential("a")
	for range 25 {
		if _, err := s.Start(c, "gpt-5.5"); err != nil {
			t.Fatal(err)
		}
		s.Wait()
	}
	if len(s.entries[c.Index].Records) != 20 {
		t.Fatal("unbounded history")
	}
	r, ok := s.Record(c, "")
	if !ok || r.Status != "completed" || r.InputTokens != 36 || r.OutputTokens != 9 || r.ReasoningTokens != 3 || len(r.Samples) != 3 {
		t.Fatal(r)
	}
	detail, _ := json.Marshal(r)
	for _, field := range []string{"diagnostics", "family_probabilities", "profile_similarity", "score"} {
		if strings.Contains(string(detail), `"`+field+`"`) {
			t.Fatalf("internal algorithm field leaked into frozen contract: %s", field)
		}
	}
	state := s.State(c)
	raw, _ := json.Marshal(state)
	if strings.Contains(string(raw), "samples") || strings.Contains(string(raw), "results") {
		t.Fatal(string(raw))
	}
	c.Identity = "replacement"
	if _, ok = s.Record(c, ""); ok || s.State(c).Latest != nil {
		t.Fatal("replacement inherited result")
	}
	reload := New(path, nil)
	r, ok = reload.Record(testCredential("a"), "")
	if !ok || len(r.Samples) != 3 {
		t.Fatal(r)
	}
}
func TestModelTraceStorageFailuresPreventExecution(t *testing.T) {
	path := testPath(t)
	if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	fn := func(context.Context, Credential, string, Challenge) (Output, error) {
		calls.Add(1)
		return Output{}, nil
	}
	s := New(path, fn)
	if s.StorageError() == "" {
		t.Fatal("hidden load error")
	}
	if _, err := s.Start(testCredential("a"), "gpt-5.5"); ErrorCode(err) != 503 {
		t.Fatal(err)
	}
	broken := New(filepath.Join(t.TempDir(), "missing", "state.json"), fn)
	if _, err := broken.Start(testCredential("a"), "gpt-5.5"); ErrorCode(err) != 503 {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("executed without durable marker")
	}
}
func TestModelTracePartialFailureAndVerdictThreshold(t *testing.T) {
	for _, tc := range []struct {
		name   string
		valid  int
		fail   bool
		status string
	}{{"partial", 2, false, "partial"}, {"invalid", 0, false, "partial"}, {"failed", 0, true, "failed"}} {
		t.Run(tc.name, func(t *testing.T) {
			var n atomic.Int32
			s := New(testPath(t), func(context.Context, Credential, string, Challenge) (Output, error) {
				if int(n.Add(1)) <= tc.valid {
					return Output{Text: strings.Repeat("42,", 320)}, nil
				}
				if tc.fail {
					return Output{}, errors.New("upstream failure")
				}
				return Output{Text: "refused"}, nil
			})
			if _, err := s.Start(testCredential("a"), "gpt-5.5"); err != nil {
				t.Fatal(err)
			}
			s.Wait()
			r, _ := s.Record(testCredential("a"), "")
			if r.Status != tc.status || r.Verdict != "inconclusive" {
				t.Fatal(r)
			}
		})
	}
	for _, tc := range []struct {
		used             int
		prob             float64
		prediction, want string
	}{
		{3, .8, "a", "consistent"},
		{3, .8, "b", "consistent"},
		{3, .99, "gpt-6-luna", "consistent"},
		{3, .8, "gpt-5.6-luna", "suspected"},
		{3, .7999, "gpt-5.6-luna", "inconclusive"},
		{2, .99, "gpt-5.6-luna", "inconclusive"},
	} {
		if got := verdict("a", &Attribution{Used: tc.used, Probability: tc.prob, Prediction: tc.prediction}, "completed"); got != tc.want {
			t.Fatal(got, tc)
		}
	}
}
func TestModelTraceStoredVerdictsUseCurrentLunaRuleWithoutExecution(t *testing.T) {
	c := testCredential("a")
	path := testPath(t)
	records := []Record{
		{ID: "other", Model: "gpt-5.4", Status: "completed", Verdict: "suspected", InputTokens: 42302, Attribution: &Attribution{Prediction: "gpt-6-luna", Probability: .99, Used: 3}},
		{ID: "luna", Model: "gpt-5.6-luna", Status: "completed", Verdict: "consistent", Attribution: &Attribution{Prediction: "gpt-5.6-luna", Probability: .99, Used: 3}},
		{ID: "low", Model: "gpt-5.4", Status: "completed", Verdict: "suspected", Attribution: &Attribution{Prediction: "gpt-5.6-luna", Probability: .79, Used: 3}},
		{ID: "partial", Model: "gpt-5.4", Status: "partial", Verdict: "suspected", Attribution: &Attribution{Prediction: "gpt-5.6-luna", Probability: .99, Used: 2}},
		{ID: "cancelled", Model: "gpt-5.4", Status: "cancelled", Verdict: "suspected", Attribution: &Attribution{Prediction: "gpt-5.6-luna", Probability: .99, Used: 3}},
	}
	raw, err := json.Marshal(map[string]any{"version": 1, "entries": map[string]savedEntry{c.Index: {Identity: c.Identity, Records: records}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	s := New(path, func(context.Context, Credential, string, Challenge) (Output, error) {
		calls.Add(1)
		return Output{}, errors.New("must not execute")
	})
	for i, want := range []string{"consistent", "suspected", "inconclusive", "inconclusive", "inconclusive"} {
		r, ok := s.Record(c, records[i].ID)
		if !ok || r.Verdict != want || r.Attribution.Prediction != records[i].Attribution.Prediction || r.InputTokens != records[i].InputTokens {
			t.Fatal("stored verdict was not reclassified from original evidence", r, want)
		}
	}
	if s.State(c).Latest.Verdict != "inconclusive" || s.StorageError() != "" || calls.Load() != 0 {
		t.Fatal("reclassification changed runtime or issued paid work")
	}
	reloaded := New(path, nil)
	if r, _ := reloaded.Record(c, "other"); r.Verdict != "consistent" {
		t.Fatal("policy migration did not persist", r)
	}
}

func TestModelTraceFinalSaveFailureIsVisible(t *testing.T) {
	path := testPath(t)
	entered, release := make(chan struct{}, 3), make(chan struct{})
	s := New(path, func(context.Context, Credential, string, Challenge) (Output, error) {
		entered <- struct{}{}
		<-release
		return Output{Text: strings.Repeat("42,", 320)}, nil
	})
	if _, err := s.Start(testCredential("a"), "gpt-5.5"); err != nil {
		t.Fatal(err)
	}
	for range 3 {
		<-entered
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	close(release)
	s.Wait()
	r, _ := s.Record(testCredential("a"), "")
	if r.StorageError == "" || s.StorageError() == "" {
		t.Fatal("fake durable success", r)
	}
}
