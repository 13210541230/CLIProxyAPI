// Package modeltrace provides bounded, credential-pinned model attribution jobs.
package modeltrace

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxText = 64000

// Credential contains identifiers only, never credential material.
type Credential struct{ Index, ID, Identity string }
type Challenge = traceChallenge
type Output struct {
	Text                                       string
	InputTokens, OutputTokens, ReasoningTokens int64
}
type ExecuteFunc func(context.Context, Credential, string, Challenge) (Output, error)
type Model struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
}

func Models() []Model {
	models := make([]Model, 0, len(modelTraceBank.Models))
	for _, m := range modelTraceBank.Models {
		models = append(models, Model{m.ID, m.DisplayName})
	}
	return models
}
func BankRevision() string { return traceBankRevision }
func Supports(model string) bool {
	for _, m := range modelTraceBank.Models {
		if m.ID == model {
			return true
		}
	}
	return false
}

type Progress struct {
	ID        string    `json:"id"`
	Model     string    `json:"model"`
	Phase     string    `json:"phase"`
	Done      int       `json:"done"`
	Total     int       `json:"total"`
	StartedAt time.Time `json:"started_at"`
}
type Sample struct {
	Challenge
	Text     string `json:"text,omitempty"`
	Error    string `json:"error,omitempty"`
	Parsed   int    `json:"parsed_numbers"`
	Accepted bool   `json:"accepted"`
}

// Public attribution intentionally excludes the algorithm's internal feature
// scores, diagnostics and family ranking from the frozen management contract.
type Attribution struct {
	Prediction        string      `json:"prediction"`
	Probability       float64     `json:"probability"`
	Used              int         `json:"used_outputs"`
	FamilyPrediction  string      `json:"family_prediction_name,omitempty"`
	FamilyProbability float64     `json:"family_probability,omitempty"`
	Results           []Candidate `json:"results,omitempty"`
}
type Candidate struct {
	Model       string  `json:"model"`
	DisplayName string  `json:"display_name"`
	Probability float64 `json:"probability"`
}
type Record struct {
	ID              string       `json:"id"`
	Time            time.Time    `json:"time"`
	Model           string       `json:"model"`
	Status          string       `json:"status"`
	Verdict         string       `json:"verdict"`
	BankRevision    string       `json:"bank_revision"`
	DurationMS      int64        `json:"duration_ms"`
	InputTokens     int64        `json:"input_tokens"`
	OutputTokens    int64        `json:"output_tokens"`
	ReasoningTokens int64        `json:"reasoning_tokens"`
	Attribution     *Attribution `json:"attribution,omitempty"`
	Samples         []Sample     `json:"samples,omitempty"`
	Error           string       `json:"error,omitempty"`
	StorageError    string       `json:"storage_error,omitempty"`
}
type State struct {
	Running *Progress `json:"running,omitempty"`
	Latest  *Record   `json:"latest,omitempty"`
}
type savedEntry struct {
	Identity string   `json:"identity"`
	Records  []Record `json:"records"`
}
type activeRun struct {
	credential Credential
	progress   Progress
	cancel     context.CancelFunc
}
type Service struct {
	mu           sync.Mutex
	wg           sync.WaitGroup
	path         string
	execute      ExecuteFunc
	entries      map[string]savedEntry
	running      map[string]*activeRun
	storageError string
	stopped      bool
}
type admissionError struct {
	code    int
	message string
}

func (e *admissionError) Error() string { return e.message }
func ErrorCode(err error) int {
	if e, ok := err.(*admissionError); ok {
		return e.code
	}
	return 503
}
func unavailable(message string) error { return &admissionError{503, message} }

// New loads durable markers; restart never resumes potentially charged work.
func New(path string, execute ExecuteFunc) *Service {
	s := &Service{path: path, execute: execute, entries: map[string]savedEntry{}, running: map[string]*activeRun{}}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s
	}
	if err != nil {
		s.storageError = "cannot read ModelTrace state"
		return s
	}
	var disk struct {
		Version int                   `json:"version"`
		Entries map[string]savedEntry `json:"entries"`
	}
	if json.Unmarshal(raw, &disk) != nil || disk.Version != 1 || disk.Entries == nil {
		s.storageError = "invalid ModelTrace state"
		return s
	}
	s.entries = disk.Entries
	changed := false
	for index, e := range s.entries {
		if len(e.Records) > 20 {
			e.Records = e.Records[len(e.Records)-20:]
			changed = true
		}
		for i := range e.Records {
			if e.Records[i].Status == "running" {
				e.Records[i].Status = "interrupted"
				e.Records[i].Error = "server restarted before the run finished"
				changed = true
			}
			// Reclassify saved evidence using the current rule, without executing
			// any model requests or changing the original fingerprint and samples.
			current := verdict(e.Records[i].Model, e.Records[i].Attribution, e.Records[i].Status)
			if e.Records[i].Verdict != current {
				e.Records[i].Verdict = current
				changed = true
			}
		}
		s.entries[index] = e
	}
	if changed {
		if err = s.saveLocked(); err != nil {
			s.storageError = "cannot persist updated ModelTrace state"
		}
	}
	return s
}
func (s *Service) StorageError() string { s.mu.Lock(); defer s.mu.Unlock(); return s.storageError }

// Start commits the initial marker before launching any request. Two admitted
// runs each own three challenge goroutines: no queue or extra retry layer.
func (s *Service) Start(c Credential, model string) (Progress, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped || s.execute == nil {
		return Progress{}, unavailable("ModelTrace execution unavailable")
	}
	if s.storageError != "" {
		return Progress{}, unavailable(s.storageError)
	}
	if _, ok := s.running[c.Index]; ok {
		return Progress{}, &admissionError{409, "credential already running"}
	}
	if len(s.running) >= 2 {
		return Progress{}, &admissionError{429, "ModelTrace run capacity reached"}
	}
	if c.Index == "" || c.ID == "" || c.Identity == "" || !Supports(model) {
		return Progress{}, &admissionError{400, "invalid credential or target model"}
	}
	now := time.Now().UTC()
	p := Progress{ID: uuidV7().String(), Model: model, Phase: "collecting", Total: 3, StartedAt: now}
	marker := Record{ID: p.ID, Time: now, Model: model, Status: "running", Verdict: "inconclusive", BankRevision: traceBankRevision}
	previous, exists := s.entries[c.Index]
	entry := previous
	if entry.Identity != c.Identity {
		entry = savedEntry{Identity: c.Identity}
	}
	// Copy before trimming so a failed save can restore the previous history.
	entry.Records = append(append([]Record(nil), entry.Records...), marker)
	if len(entry.Records) > 20 {
		entry.Records = entry.Records[len(entry.Records)-20:]
	}
	s.entries[c.Index] = entry
	if err := s.saveLocked(); err != nil {
		if exists {
			s.entries[c.Index] = previous
		} else {
			delete(s.entries, c.Index)
		}
		s.storageError = "cannot persist ModelTrace running marker"
		return Progress{}, unavailable(s.storageError)
	}
	ctx, cancel := context.WithCancel(context.Background())
	active := &activeRun{credential: c, progress: p, cancel: cancel}
	s.running[c.Index] = active
	s.wg.Add(1)
	go s.run(ctx, active)
	return p, nil
}
func (s *Service) Cancel(c Credential) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.running[c.Index]
	if r == nil || r.credential.Identity != c.Identity {
		return false
	}
	r.progress.Phase = "cancelling"
	r.cancel()
	return true
}
func (s *Service) Wait() { s.wg.Wait() }
func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	s.stopped = true
	for _, r := range s.running {
		r.progress.Phase = "cancelling"
		r.cancel()
	}
	s.mu.Unlock()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s *Service) State(c Credential) State {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := State{}
	if r := s.running[c.Index]; r != nil && r.credential.Identity == c.Identity {
		p := r.progress
		state.Running = &p
	}
	if e, ok := s.entries[c.Index]; ok && e.Identity == c.Identity && len(e.Records) > 0 {
		// Batch status avoids copying/encoding large sample text only to discard it.
		r := e.Records[len(e.Records)-1]
		r.Samples = nil
		if r.Attribution != nil {
			attr := *r.Attribution
			attr.Results = nil
			r.Attribution = &attr
		}
		state.Latest = &r
	}
	return state
}
func (s *Service) Record(c Credential, id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.entries[c.Index]
	if !ok || e.Identity != c.Identity {
		return Record{}, false
	}
	for i := len(e.Records) - 1; i >= 0; i-- {
		if id == "" || e.Records[i].ID == id {
			return cloneRecord(e.Records[i]), true
		}
	}
	return Record{}, false
}
func cloneRecord(r Record) Record {
	raw, _ := json.Marshal(r)
	var copy Record
	_ = json.Unmarshal(raw, &copy)
	return copy
}

// The target/fingerprint comparison is independent of degradation. Only the
// exact gpt-5.6-luna fingerprint is considered suspicious by the product rule.
// "consistent" is retained as the wire value for "no degradation detected".
func verdict(_ string, attr *Attribution, status string) string {
	if status != "completed" || attr == nil || attr.Used != 3 || attr.Probability < .8 || attr.Prediction == "" {
		return "inconclusive"
	}
	if attr.Prediction == "gpt-5.6-luna" {
		return "suspected"
	}
	return "consistent"
}
func (s *Service) run(ctx context.Context, active *activeRun) {
	defer s.wg.Done()
	defer active.cancel()
	started := time.Now()
	r := Record{ID: active.progress.ID, Time: active.progress.StartedAt, Model: active.progress.Model, Status: "completed", Verdict: "inconclusive", BankRevision: traceBankRevision}
	challenges := traceChallenges()
	type result struct {
		i      int
		sample Sample
		output Output
		failed bool
	}
	results := make(chan result, 3)
	for i, ch := range challenges {
		go func() {
			sample := Sample{Challenge: ch}
			out, err := s.execute(ctx, active.credential, r.Model, ch)
			if err != nil {
				sample.Error = "model request failed"
				var diagnostic *executionDiagnostic
				if errors.As(err, &diagnostic) {
					sample.Error = diagnostic.Error()
				}
				if ctx.Err() != nil {
					sample.Error = "request cancelled"
				}
			} else if len(out.Text) > maxText {
				sample.Error = "response text exceeds limit"
			} else {
				sample.Text = out.Text
				sample.Parsed = len(traceParseNumbers(out.Text))
				sample.Accepted = sample.Parsed >= traceMinimumNumbers(ch.ExpectedCount)
				if !sample.Accepted {
					sample.Error = "insufficient numbers"
				}
			}
			results <- result{i, sample, out, err != nil}
		}()
	}
	r.Samples = make([]Sample, 3)
	failures := 0
	outputs := make([]traceOutput, 3)
	for range 3 {
		e := <-results
		r.Samples[e.i] = e.sample
		outputs[e.i] = traceOutput{e.sample.Text, e.sample.ExpectedCount}
		r.InputTokens += e.output.InputTokens
		r.OutputTokens += e.output.OutputTokens
		r.ReasoningTokens += e.output.ReasoningTokens
		if e.failed {
			failures++
		}
		s.mu.Lock()
		active.progress.Done++
		s.mu.Unlock()
	}
	attr, err := analyzeModelTrace(outputs)
	if err == nil {
		r.Attribution = &Attribution{Prediction: attr.Prediction, Probability: attr.Probability, Used: attr.Used, FamilyPrediction: attr.FamilyPrediction, FamilyProbability: attr.FamilyProbability}
		for _, candidate := range attr.Results {
			r.Attribution.Results = append(r.Attribution.Results, Candidate{candidate.Model, candidate.DisplayName, candidate.Probability})
		}
	}
	if err != nil || attr.Used < 3 {
		r.Status = "partial"
		r.Error = "insufficient valid challenges"
	}
	if failures == 3 {
		r.Status = "failed"
		r.Error = "model requests failed"
	}
	if ctx.Err() != nil {
		r.Status = "cancelled"
		r.Error = "run cancelled"
	}
	r.Verdict = verdict(r.Model, r.Attribution, r.Status)
	r.DurationMS = time.Since(started).Milliseconds()
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.entries[active.credential.Index]
	// The running marker is replaced, rather than adding another history entry.
	for i := range entry.Records {
		if entry.Records[i].ID == r.ID {
			entry.Records[i] = r
		}
	}
	s.entries[active.credential.Index] = entry
	if err = s.saveLocked(); err != nil {
		s.storageError = "cannot persist final ModelTrace result"
		for i := range entry.Records {
			if entry.Records[i].ID == r.ID {
				entry.Records[i].StorageError = s.storageError
			}
		}
		s.entries[active.credential.Index] = entry
	}
	delete(s.running, active.credential.Index)
}

// saveLocked uses a same-directory rename so interruption cannot leave a
// partially encoded state file. No authentication metadata enters this file.
func (s *Service) saveLocked() error {
	disk := struct {
		Version int                   `json:"version"`
		Entries map[string]savedEntry `json:"entries"`
	}{1, s.entries}
	raw, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.path), ".modeltrace-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err = f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, s.path)
}
func mustDecode[T any](raw []byte) T {
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(fmt.Sprintf("invalid embedded ModelTrace data: %v", err))
	}
	return v
}
