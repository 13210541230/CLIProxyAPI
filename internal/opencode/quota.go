package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Window describes one rolling quota window reported by the usage endpoint.
type Window struct {
	Status   string
	Percent  float64
	ResetsAt time.Time
}

// QuotaSnapshot is the last observed quota state for one account.
type QuotaSnapshot struct {
	Rolling   Window
	Weekly    Window
	Monthly   Window
	FetchedAt time.Time
	Err       string
}

// MinPercent returns the tightest remaining-ratio proxy across windows.
func (s QuotaSnapshot) MinPercent() float64 {
	highest := s.Rolling.Percent
	if s.Weekly.Percent > highest {
		highest = s.Weekly.Percent
	}
	if s.Monthly.Percent > highest {
		highest = s.Monthly.Percent
	}
	return highest
}

// Exhausted reports whether any window reached its ceiling.
func (s QuotaSnapshot) Exhausted() bool {
	for _, window := range []Window{s.Rolling, s.Weekly, s.Monthly} {
		if window.Percent >= 100 {
			return true
		}
		if window.Status != "" && !strings.EqualFold(window.Status, "ok") {
			return true
		}
	}
	return false
}

type usageEnvelope struct {
	Usage map[string]struct {
		Status   string  `json:"status"`
		Percent  float64 `json:"percent"`
		ResetsAt string  `json:"resetsAt"`
	} `json:"usage"`
}

// QuotaTracker stores per-account quota snapshots.
type QuotaTracker struct {
	mu        sync.RWMutex
	snapshots map[string]QuotaSnapshot
}

// NewQuotaTracker creates an empty tracker.
func NewQuotaTracker() *QuotaTracker {
	return &QuotaTracker{snapshots: make(map[string]QuotaSnapshot)}
}

// Record stores a snapshot for an auth id.
func (q *QuotaTracker) Record(authID string, snapshot QuotaSnapshot) {
	if q == nil || authID == "" {
		return
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.snapshots[authID] = snapshot
}

// Snapshot returns the stored snapshot for an auth id.
func (q *QuotaTracker) Snapshot(authID string) (QuotaSnapshot, bool) {
	if q == nil {
		return QuotaSnapshot{}, false
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	snapshot, ok := q.snapshots[authID]
	return snapshot, ok
}

// All returns a copy of every stored snapshot.
func (q *QuotaTracker) All() map[string]QuotaSnapshot {
	if q == nil {
		return nil
	}
	q.mu.RLock()
	defer q.mu.RUnlock()
	out := make(map[string]QuotaSnapshot, len(q.snapshots))
	for key, value := range q.snapshots {
		out[key] = value
	}
	return out
}

// FetchQuota reads the Go-plan usage endpoint for one account.
func FetchQuota(ctx context.Context, client *http.Client, endpoint, key string) (QuotaSnapshot, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if strings.TrimSpace(key) == "" {
		return QuotaSnapshot{}, fmt.Errorf("opencode: missing api key")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgentValue)
	req.Header.Set("x-opencode-client", "cli")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	resp, err := client.Do(req)
	if err != nil {
		return QuotaSnapshot{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return QuotaSnapshot{}, err
	}
	if resp.StatusCode/100 != 2 {
		return QuotaSnapshot{}, fmt.Errorf("opencode usage endpoint returned HTTP %d", resp.StatusCode)
	}
	var envelope usageEnvelope
	if err := json.Unmarshal(body, &envelope); err != nil {
		return QuotaSnapshot{}, err
	}
	snapshot := QuotaSnapshot{FetchedAt: time.Now()}
	snapshot.Rolling = parseWindow(envelope.Usage["rolling"])
	snapshot.Weekly = parseWindow(envelope.Usage["weekly"])
	snapshot.Monthly = parseWindow(envelope.Usage["monthly"])
	return snapshot, nil
}

func parseWindow(entry struct {
	Status   string  `json:"status"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resetsAt"`
}) Window {
	window := Window{Status: entry.Status, Percent: entry.Percent}
	if entry.ResetsAt != "" {
		if parsed, err := time.Parse(time.RFC3339, entry.ResetsAt); err == nil {
			window.ResetsAt = parsed
		}
	}
	return window
}
