// Package usage provides usage tracking and logging functionality for the CLI Proxy API server.
// This file provides IP address tracking for monitoring active client connections.
package usage

import (
	"sync"
	"time"
)

// IPTracker maintains records of client IP activity.
// It tracks which IP addresses have made requests recently, along with usage statistics per IP.
type IPTracker struct {
	mu sync.RWMutex

	// ips maps IP addresses to their activity records.
	ips map[string]*IPActivity
}

// IPActivity holds activity metrics for a single IP address.
type IPActivity struct {
	// LastSeen is the timestamp of the most recent request from this IP.
	LastSeen time.Time `json:"last_seen"`

	// TotalRequests is the total number of requests from this IP.
	TotalRequests int64 `json:"total_requests"`

	// TotalTokens is the total token consumption from this IP.
	TotalTokens int64 `json:"total_tokens"`

	// APIKeys tracks which API keys this IP has used and their request counts.
	APIKeys map[string]int64 `json:"api_keys"`

	// FailedRequests is the count of failed requests from this IP.
	FailedRequests int64 `json:"failed_requests"`
}

// IPSnapshot is an immutable view of IP activity for export.
type IPSnapshot struct {
	IP             string            `json:"ip"`
	LastSeen       time.Time         `json:"last_seen"`
	TotalRequests  int64             `json:"total_requests"`
	TotalTokens    int64             `json:"total_tokens"`
	APIKeys        map[string]int64  `json:"api_keys"`
	FailedRequests int64             `json:"failed_requests"`
}

// NewIPTracker constructs a new IP tracker.
func NewIPTracker() *IPTracker {
	return &IPTracker{
		ips: make(map[string]*IPActivity),
	}
}

// RecordActivity records a request from an IP address.
// It updates the last seen time, request count, and token usage.
//
// Parameters:
//   - ip: The client IP address
//   - apiKey: The API key used (may be empty if no key was provided)
//   - tokens: The number of tokens consumed
//   - failed: Whether the request failed
func (t *IPTracker) RecordActivity(ip, apiKey string, tokens int64, failed bool) {
	if t == nil || ip == "" {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	activity, exists := t.ips[ip]
	if !exists {
		activity = &IPActivity{
			APIKeys: make(map[string]int64),
		}
		t.ips[ip] = activity
	}

	activity.LastSeen = time.Now()
	activity.TotalRequests++
	activity.TotalTokens += tokens

	if failed {
		activity.FailedRequests++
	}

	if apiKey != "" {
		activity.APIKeys[apiKey]++
	}
}

// GetActiveIPs returns all IPs that have been active since the given time.
//
// Parameters:
//   - since: Only IPs with activity after this time are included
//
// Returns:
//   - map[string]IPSnapshot: A map of IP addresses to their activity snapshots
func (t *IPTracker) GetActiveIPs(since time.Time) map[string]IPSnapshot {
	if t == nil {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]IPSnapshot)
	for ip, activity := range t.ips {
		if activity.LastSeen.After(since) || activity.LastSeen.Equal(since) {
			// Copy APIKeys to avoid race conditions
			apiKeysCopy := make(map[string]int64, len(activity.APIKeys))
			for k, v := range activity.APIKeys {
				apiKeysCopy[k] = v
			}

			result[ip] = IPSnapshot{
				IP:             ip,
				LastSeen:       activity.LastSeen,
				TotalRequests:  activity.TotalRequests,
				TotalTokens:    activity.TotalTokens,
				APIKeys:        apiKeysCopy,
				FailedRequests: activity.FailedRequests,
			}
		}
	}

	return result
}

// GetAllIPs returns all tracked IPs regardless of last activity time.
//
// Returns:
//   - map[string]IPSnapshot: A map of all IP addresses to their activity snapshots
func (t *IPTracker) GetAllIPs() map[string]IPSnapshot {
	if t == nil {
		return nil
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	result := make(map[string]IPSnapshot, len(t.ips))
	for ip, activity := range t.ips {
		apiKeysCopy := make(map[string]int64, len(activity.APIKeys))
		for k, v := range activity.APIKeys {
			apiKeysCopy[k] = v
		}

		result[ip] = IPSnapshot{
			IP:             ip,
			LastSeen:       activity.LastSeen,
			TotalRequests:  activity.TotalRequests,
			TotalTokens:    activity.TotalTokens,
			APIKeys:        apiKeysCopy,
			FailedRequests: activity.FailedRequests,
		}
	}

	return result
}

// GetIPCount returns the total number of unique IPs tracked.
func (t *IPTracker) GetIPCount() int {
	if t == nil {
		return 0
	}
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.ips)
}

// CleanupInactiveIPs removes IPs that have not been active since the given time.
// This helps prevent memory growth from stale IP records.
//
// Parameters:
//   - before: IPs with last activity before this time are removed
//
// Returns:
//   - int: The number of IPs removed
func (t *IPTracker) CleanupInactiveIPs(before time.Time) int {
	if t == nil {
		return 0
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	removed := 0
	for ip, activity := range t.ips {
		if activity.LastSeen.Before(before) {
			delete(t.ips, ip)
			removed++
		}
	}

	return removed
}

// GetStatistics returns aggregate statistics about tracked IPs.
//
// Returns:
//   - totalIPs: Total unique IPs tracked
//   - activeIPs: IPs active in the last 5 minutes
//   - totalRequests: Sum of all requests across all IPs
//   - totalTokens: Sum of all tokens across all IPs
func (t *IPTracker) GetStatistics() (totalIPs, activeIPs, totalRequests, totalTokens int64) {
	if t == nil {
		return 0, 0, 0, 0
	}

	fiveMinutesAgo := time.Now().Add(-5 * time.Minute)

	t.mu.RLock()
	defer t.mu.RUnlock()

	totalIPs = int64(len(t.ips))

	for _, activity := range t.ips {
		if activity.LastSeen.After(fiveMinutesAgo) {
			activeIPs++
		}
		totalRequests += activity.TotalRequests
		totalTokens += activity.TotalTokens
	}

	return
}
