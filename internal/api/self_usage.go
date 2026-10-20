package api

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quota"
	log "github.com/sirupsen/logrus"
)

// selfUsageResponse is the JSON response for GET /v1/usage/self.
type selfUsageResponse struct {
	Status string          `json:"status"`
	Quota  selfQuotaStatus `json:"quota"`
	Usage  *selfUsageData  `json:"usage"`
}

type selfQuotaStatus struct {
	Paused       bool   `json:"paused"`
	PausedReason string `json:"paused_reason"`
	ResumesAt    string `json:"resumes_at"`
}

type selfUsageData struct {
	Today     selfUsagePeriod           `json:"today"`
	ThisWeek  selfUsagePeriod           `json:"this_week"`
	ByModel   map[string]selfModelUsage `json:"by_model"`
	Available bool                      `json:"available"`
}

type selfUsagePeriod struct {
	Requests            int   `json:"requests"`
	Success             int   `json:"success"`
	Failed              int   `json:"failed"`
	InputTokens         int64 `json:"input_tokens"`
	OutputTokens        int64 `json:"output_tokens"`
	CacheReadTokens     int64 `json:"cache_read_tokens"`
	CacheCreationTokens int64 `json:"cache_creation_tokens"`
	TotalTokens         int64 `json:"total_tokens"`
	CostCents           int64 `json:"cost_cents"`
	LimitCents          int64 `json:"limit_cents"`
}

type selfModelUsage struct {
	Requests     int   `json:"requests"`
	InputTokens  int64 `json:"input_tokens"`
	OutputTokens int64 `json:"output_tokens"`
	CostCents    int64 `json:"cost_cents"`
}

// selfUsageHandler handles GET /v1/usage/self.
type selfUsageHandler struct {
	quotaManager  *quota.Manager
	managementKey string // plaintext MANAGEMENT_PASSWORD for usage-service auth
}

// newSelfUsageHandler creates a new self-usage handler.
// managementKey is the plaintext MANAGEMENT_PASSWORD env value.
// The usage service URL is read from the USAGE_SERVICE_URL env var.
func newSelfUsageHandler(qm *quota.Manager) *selfUsageHandler {
	key := ""
	if pw := strings.TrimSpace(os.Getenv("MANAGEMENT_PASSWORD")); pw != "" {
		key = pw
	}
	return &selfUsageHandler{
		quotaManager:  qm,
		managementKey: key,
	}
}

// keyHash computes SHA-256[:8] hex of an API key.
func keyHash(apiKey string) string {
	h := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(h[:4])
}

// handleSelfUsage processes GET /v1/usage/self requests.
func (h *selfUsageHandler) handleSelfUsage(c *gin.Context) {
	rawKey, exists := c.Get("userApiKey")
	if !exists {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}
	apiKey, ok := rawKey.(string)
	if !ok || apiKey == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "not authenticated"})
		return
	}

	kh := keyHash(apiKey)

	resp := selfUsageResponse{
		Status: "ok",
		Quota:  selfQuotaStatus{},
		Usage:  &selfUsageData{Available: false, ByModel: map[string]selfModelUsage{}},
	}

	// Check pause status
	if h.quotaManager != nil {
		paused, entry, err := h.quotaManager.IsPaused(kh)
		if err != nil {
			log.Errorf("self usage: IsPaused error: %v", err)
		} else if paused && entry != nil {
			resp.Quota.Paused = true
			resp.Quota.PausedReason = entry.Reason
			if !entry.ExpiresAt.IsZero() {
				resp.Quota.ResumesAt = entry.ExpiresAt.Format(time.RFC3339)
			}
		}
	}

	// Fetch usage data from external usage service via HTTP.
	// Falls back gracefully (usage.available = false) when the service URL is not configured or unreachable.
	if usageData, err := h.fetchUsage(kh); err != nil {
		log.Debugf("self usage: usage service call skipped: %v", err)
	} else {
		resp.Usage = usageData
	}

	c.JSON(http.StatusOK, resp)
}

// fetchUsage calls the usage service to get aggregated usage for a key hash.
// The usage service URL is read from USAGE_SERVICE_URL env var.
func (h *selfUsageHandler) fetchUsage(keyHash string) (*selfUsageData, error) {
	usageServiceURL := strings.TrimSpace(os.Getenv("USAGE_SERVICE_URL"))
	if usageServiceURL == "" || h.managementKey == "" {
		return nil, fmt.Errorf("usage service not configured (set USAGE_SERVICE_URL and MANAGEMENT_PASSWORD)")
	}

	client := &http.Client{Timeout: 10 * time.Second}
	url := strings.TrimRight(usageServiceURL, "/") + "/api/usage/key/" + keyHash

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+h.managementKey)
	req.Header.Set("X-Management-Key", h.managementKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("usage service returned status %d", resp.StatusCode)
	}

	var result selfUsageData
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	result.Available = true
	if result.ByModel == nil {
		result.ByModel = map[string]selfModelUsage{}
	}
	return &result, nil
}
