package management

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

// GetActiveIPs returns IP addresses that have been active recently.
// Query parameters:
//   - minutes: Number of minutes to look back (default: 5)
//   - all: If "true", return all IPs regardless of activity time
//
// Response includes:
//   - active_ips: Map of IP addresses to their activity details
//   - count: Number of IPs in the response
//   - since: The timestamp threshold used for filtering
func (h *Handler) GetActiveIPs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	// Check if IP tracker is available
	ipTracker := usage.GetIPTracker()
	if ipTracker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IP tracking unavailable"})
		return
	}

	// Check if user wants all IPs
	if c.Query("all") == "true" {
		allIPs := ipTracker.GetAllIPs()
		c.JSON(http.StatusOK, gin.H{
			"ips":   allIPs,
			"count": len(allIPs),
		})
		return
	}

	// Parse minutes parameter (default: 5 minutes)
	minutes := 5
	if minStr := c.Query("minutes"); minStr != "" {
		if m, err := strconv.Atoi(minStr); err == nil && m > 0 {
			minutes = m
		}
	}

	since := time.Now().Add(-time.Duration(minutes) * time.Minute)
	activeIPs := ipTracker.GetActiveIPs(since)

	c.JSON(http.StatusOK, gin.H{
		"active_ips": activeIPs,
		"count":     len(activeIPs),
		"since":     since,
		"minutes":   minutes,
	})
}

// GetIPStatistics returns aggregate statistics about tracked IP addresses.
//
// Response includes:
//   - total_ips: Total unique IPs tracked
//   - active_ips: IPs active in the last 5 minutes
//   - total_requests: Sum of all requests across all IPs
//   - total_tokens: Sum of all tokens across all IPs
func (h *Handler) GetIPStatistics(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	ipTracker := usage.GetIPTracker()
	if ipTracker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IP tracking unavailable"})
		return
	}

	totalIPs, activeIPs, totalRequests, totalTokens := ipTracker.GetStatistics()

	c.JSON(http.StatusOK, gin.H{
		"total_ips":     totalIPs,
		"active_ips":    activeIPs,
		"total_requests": totalRequests,
		"total_tokens":  totalTokens,
	})
}

// CleanupInactiveIPs removes IP records that have not been active for a specified duration.
// Query parameters:
//   - hours: Number of hours of inactivity before cleanup (default: 24)
//
// Response includes:
//   - removed: Number of IPs removed
//   - message: Success message
func (h *Handler) CleanupInactiveIPs(c *gin.Context) {
	if h == nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "handler unavailable"})
		return
	}

	ipTracker := usage.GetIPTracker()
	if ipTracker == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IP tracking unavailable"})
		return
	}

	// Parse hours parameter (default: 24 hours)
	hours := 24
	if hStr := c.Query("hours"); hStr != "" {
		if h, err := strconv.Atoi(hStr); err == nil && h > 0 {
			hours = h
		}
	}

	before := time.Now().Add(-time.Duration(hours) * time.Hour)
	removed := ipTracker.CleanupInactiveIPs(before)

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "Inactive IPs cleaned up successfully",
		"removed": removed,
		"hours":   hours,
	})
}
