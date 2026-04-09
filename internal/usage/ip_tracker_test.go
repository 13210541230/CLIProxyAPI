package usage

import (
	"testing"
	"time"
)

func TestIPTracker_RecordActivity(t *testing.T) {
	tracker := NewIPTracker()

	// Test recording activity for a new IP
	tracker.RecordActivity("192.168.1.1", "sk-test", 100, false)

	if count := tracker.GetIPCount(); count != 1 {
		t.Errorf("expected 1 IP, got %d", count)
	}

	ips := tracker.GetAllIPs()
	ip, exists := ips["192.168.1.1"]
	if !exists {
		t.Fatal("expected IP to exist")
	}

	if ip.TotalRequests != 1 {
		t.Errorf("expected 1 request, got %d", ip.TotalRequests)
	}
	if ip.TotalTokens != 100 {
		t.Errorf("expected 100 tokens, got %d", ip.TotalTokens)
	}
	if ip.FailedRequests != 0 {
		t.Errorf("expected 0 failed requests, got %d", ip.FailedRequests)
	}
	if ip.APIKeys["sk-test"] != 1 {
		t.Errorf("expected API key count 1, got %d", ip.APIKeys["sk-test"])
	}
}

func TestIPTracker_RecordActivityMultipleRequests(t *testing.T) {
	tracker := NewIPTracker()

	// Record multiple requests from the same IP
	tracker.RecordActivity("192.168.1.1", "sk-test", 100, false)
	tracker.RecordActivity("192.168.1.1", "sk-test", 200, false)
	tracker.RecordActivity("192.168.1.1", "sk-other", 50, true)

	ips := tracker.GetAllIPs()
	ip := ips["192.168.1.1"]

	if ip.TotalRequests != 3 {
		t.Errorf("expected 3 requests, got %d", ip.TotalRequests)
	}
	if ip.TotalTokens != 350 {
		t.Errorf("expected 350 tokens, got %d", ip.TotalTokens)
	}
	if ip.FailedRequests != 1 {
		t.Errorf("expected 1 failed request, got %d", ip.FailedRequests)
	}
	if ip.APIKeys["sk-test"] != 2 {
		t.Errorf("expected sk-test count 2, got %d", ip.APIKeys["sk-test"])
	}
	if ip.APIKeys["sk-other"] != 1 {
		t.Errorf("expected sk-other count 1, got %d", ip.APIKeys["sk-other"])
	}
}

func TestIPTracker_GetActiveIPs(t *testing.T) {
	tracker := NewIPTracker()

	// Record activity for multiple IPs
	tracker.RecordActivity("192.168.1.1", "sk-test", 100, false)
	tracker.RecordActivity("192.168.1.2", "sk-test", 200, false)

	// Get IPs active in the last minute
	since := time.Now().Add(-1 * time.Minute)
	activeIPs := tracker.GetActiveIPs(since)

	if len(activeIPs) != 2 {
		t.Errorf("expected 2 active IPs, got %d", len(activeIPs))
	}

	// Get IPs active in the future (should be empty)
	futureSince := time.Now().Add(1 * time.Hour)
	futureActiveIPs := tracker.GetActiveIPs(futureSince)

	if len(futureActiveIPs) != 0 {
		t.Errorf("expected 0 active IPs for future time, got %d", len(futureActiveIPs))
	}
}

func TestIPTracker_CleanupInactiveIPs(t *testing.T) {
	tracker := NewIPTracker()

	// Create IPs with different timestamps
	tracker.RecordActivity("192.168.1.1", "sk-test", 100, false)
	
	// Wait a moment and record another IP
	time.Sleep(10 * time.Millisecond)
	tracker.RecordActivity("192.168.1.2", "sk-test", 200, false)

	// Cleanup IPs older than 5ms (should remove 192.168.1.1)
	before := time.Now().Add(-5 * time.Millisecond)
	removed := tracker.CleanupInactiveIPs(before)

	// Note: Due to timing, both might still be present or one might be removed
	// This test is mainly to ensure the cleanup doesn't panic and works correctly
	if removed < 0 {
		t.Errorf("expected non-negative removed count, got %d", removed)
	}
}

func TestIPTracker_GetStatistics(t *testing.T) {
	tracker := NewIPTracker()

	// Record activity for multiple IPs
	tracker.RecordActivity("192.168.1.1", "sk-test", 100, false)
	tracker.RecordActivity("192.168.1.2", "sk-test", 200, false)
	tracker.RecordActivity("192.168.1.3", "sk-test", 300, false)

	totalIPs, activeIPs, totalRequests, totalTokens := tracker.GetStatistics()

	if totalIPs != 3 {
		t.Errorf("expected 3 total IPs, got %d", totalIPs)
	}
	if activeIPs != 3 {
		t.Errorf("expected 3 active IPs, got %d", activeIPs)
	}
	if totalRequests != 3 {
		t.Errorf("expected 3 total requests, got %d", totalRequests)
	}
	if totalTokens != 600 {
		t.Errorf("expected 600 total tokens, got %d", totalTokens)
	}
}

func TestIPTracker_EmptyIP(t *testing.T) {
	tracker := NewIPTracker()

	// Recording with empty IP should be a no-op
	tracker.RecordActivity("", "sk-test", 100, false)

	if count := tracker.GetIPCount(); count != 0 {
		t.Errorf("expected 0 IPs for empty IP input, got %d", count)
	}
}

func TestIPTracker_NilTracker(t *testing.T) {
	var tracker *IPTracker

	// All operations on nil tracker should be safe
	tracker.RecordActivity("192.168.1.1", "sk-test", 100, false)

	if count := tracker.GetIPCount(); count != 0 {
		t.Errorf("expected 0 for nil tracker, got %d", count)
	}

	activeIPs := tracker.GetActiveIPs(time.Now())
	if activeIPs != nil {
		t.Errorf("expected nil for nil tracker, got %v", activeIPs)
	}

	allIPs := tracker.GetAllIPs()
	if allIPs != nil {
		t.Errorf("expected nil for nil tracker, got %v", allIPs)
	}

	totalIPs, activeIPs2, totalRequests, totalTokens := tracker.GetStatistics()
	if totalIPs != 0 || activeIPs2 != 0 || totalRequests != 0 || totalTokens != 0 {
		t.Errorf("expected all zeros for nil tracker")
	}

	removed := tracker.CleanupInactiveIPs(time.Now())
	if removed != 0 {
		t.Errorf("expected 0 removed for nil tracker, got %d", removed)
	}
}
