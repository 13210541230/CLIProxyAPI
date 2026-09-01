package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/quota"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestGetPausedKeysReturnsEmptyListWhenQuotaDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/quota/paused", nil)

	h.GetPausedKeys(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Enabled bool               `json:"enabled"`
		Entries []quota.PauseEntry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if body.Enabled {
		t.Fatal("enabled = true, want false")
	}
	if len(body.Entries) != 0 {
		t.Fatalf("entries len = %d, want 0", len(body.Entries))
	}
}

func TestPostResumeKey_ExpectedReasonPreservesReplacedManualPause(t *testing.T) {
	gin.SetMode(gin.TestMode)
	qm, err := quota.NewManager(quota.QuotaConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer qm.Stop()
	const keyHash = "abcdef12"
	if err := qm.PauseKey(keyHash, "spend_limit_exceeded", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("PauseKey automatic failed: %v", err)
	}
	if err := qm.PauseKey(keyHash, "manual pause", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("PauseKey manual replacement failed: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetQuotaManager(qm)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota/resume", strings.NewReader(`{"key_hash":"`+keyHash+`","expected_reason":"spend_limit_exceeded"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PostResumeKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("conditional resume status = %d, body = %s", rec.Code, rec.Body.String())
	}
	paused, entry, err := qm.IsPaused(keyHash)
	if err != nil || !paused || entry == nil || entry.Reason != "manual pause" {
		t.Fatalf("manual replacement should remain, paused=%v entry=%v err=%v", paused, entry, err)
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/quota/resume", strings.NewReader(`{"key_hash":"`+keyHash+`"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PostResumeKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("unconditional resume status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if paused, _, err := qm.IsPaused(keyHash); err != nil || paused {
		t.Fatalf("unconditional resume should retain existing behavior, paused=%v err=%v", paused, err)
	}
}

func TestGetQuotaConfigReturnsCurrentConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		Quota: quota.QuotaConfig{
			Enabled: true,
			DBPath:  "quota.db",
			Mode:    "tokens",
			Default: quota.SpendLimit{DailyCents: 100, WeeklyCents: 700, DailyTokens: 10000, WeeklyTokens: 70000},
			Overrides: []quota.SpendLimitEntry{{
				ApplyTo:      "api-key",
				ApplyValue:   "abc",
				DailyCents:   200,
				WeeklyCents:  900,
				DailyTokens:  20000,
				WeeklyTokens: 90000,
			}},
		},
	}, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/quota/config", nil)

	h.GetQuotaConfig(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var body struct {
		Enabled bool   `json:"enabled"`
		Mode    string `json:"mode"`
		DBPath  string `json:"db_path"`
		Default struct {
			DailyCents   int64 `json:"daily_cents"`
			WeeklyCents  int64 `json:"weekly_cents"`
			DailyTokens  int64 `json:"daily_tokens"`
			WeeklyTokens int64 `json:"weekly_tokens"`
		} `json:"default"`
		Overrides []struct {
			ApplyTo      string `json:"apply_to"`
			ApplyValue   string `json:"apply_value"`
			DailyCents   int64  `json:"daily_cents"`
			WeeklyCents  int64  `json:"weekly_cents"`
			DailyTokens  int64  `json:"daily_tokens"`
			WeeklyTokens int64  `json:"weekly_tokens"`
		} `json:"overrides"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !body.Enabled || body.Mode != "tokens" || body.DBPath != "quota.db" {
		t.Fatalf("unexpected top-level config: %+v", body)
	}
	if body.Default.DailyCents != 100 || body.Default.WeeklyCents != 700 || body.Default.DailyTokens != 10000 || body.Default.WeeklyTokens != 70000 {
		t.Fatalf("unexpected default config: %+v", body.Default)
	}
	if len(body.Overrides) != 1 {
		t.Fatalf("overrides len = %d, want 1", len(body.Overrides))
	}
	if body.Overrides[0].ApplyTo != "api-key" || body.Overrides[0].ApplyValue != "abc" || body.Overrides[0].DailyTokens != 20000 || body.Overrides[0].WeeklyTokens != 90000 {
		t.Fatalf("unexpected override: %+v", body.Overrides[0])
	}
}

func TestPutQuotaConfigPersistsModeAndTokenLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)

	configPath := t.TempDir() + "/config.yaml"
	if err := os.WriteFile(configPath, []byte("quota:\n  enabled: false\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{}
	h := NewHandler(cfg, configPath, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/quota/config", strings.NewReader(`{
		"enabled": true,
		"mode": "tokens",
		"default": {"daily_tokens": 1000, "weekly_tokens": 7000},
		"overrides": [{"apply_to":"api-key","apply_value":"abcdef12","daily_tokens":2000,"weekly_tokens":14000}]
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutQuotaConfig(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if cfg.Quota.Mode != "tokens" || cfg.Quota.Default.DailyTokens != 1000 || cfg.Quota.Default.WeeklyTokens != 7000 {
		t.Fatalf("quota config = %+v, want token mode and limits", cfg.Quota)
	}
	if len(cfg.Quota.Overrides) != 1 || cfg.Quota.Overrides[0].DailyTokens != 2000 || cfg.Quota.Overrides[0].WeeklyTokens != 14000 {
		t.Fatalf("quota overrides = %+v, want token limits", cfg.Quota.Overrides)
	}
	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	for _, fragment := range []string{"mode: tokens", "daily-tokens: 1000", "weekly-tokens: 7000"} {
		if !strings.Contains(string(persisted), fragment) {
			t.Fatalf("persisted config missing %q: %s", fragment, persisted)
		}
	}
}

func TestResetQuota_UsesAuthIndex(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	next := time.Now().Add(time.Hour)
	auth := &coreauth.Auth{
		ID:             "reset-auth-id",
		FileName:       "reset-auth-file.json",
		Provider:       "claude",
		Status:         coreauth.StatusError,
		StatusMessage:  "quota exhausted",
		Unavailable:    true,
		NextRetryAfter: next,
		Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
		ModelStates: map[string]*coreauth.ModelState{
			"claude-reset-model": {
				Status:         coreauth.StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: next, BackoffLevel: 2},
			},
		},
	}
	authIndex := auth.EnsureIndex()
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/reset-quota", strings.NewReader(`{"auth_index":"`+authIndex+`"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.ResetQuota(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("failed to decode response: %v", errUnmarshal)
	}
	if payload["auth_index"] != authIndex {
		t.Fatalf("auth_index = %#v, want %q", payload["auth_index"], authIndex)
	}

	updated, ok := manager.GetByID("reset-auth-id")
	if !ok || updated == nil {
		t.Fatalf("expected auth record to exist after reset")
	}
	if updated.Status != coreauth.StatusActive || updated.StatusMessage != "" || updated.Unavailable || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("updated auth state = status %q message %q unavailable %v next %v", updated.Status, updated.StatusMessage, updated.Unavailable, updated.NextRetryAfter)
	}
	if updated.Quota.Exceeded || updated.Quota.Reason != "" || !updated.Quota.NextRecoverAt.IsZero() || updated.Quota.BackoffLevel != 0 {
		t.Fatalf("updated auth quota = %+v, want cleared", updated.Quota)
	}
	state := updated.ModelStates["claude-reset-model"]
	if state == nil {
		t.Fatalf("expected model state to remain")
	}
	if state.Status != coreauth.StatusActive || state.StatusMessage != "" || state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("updated model state = status %q message %q unavailable %v next %v", state.Status, state.StatusMessage, state.Unavailable, state.NextRetryAfter)
	}
	if state.Quota.Exceeded || state.Quota.Reason != "" || !state.Quota.NextRecoverAt.IsZero() || state.Quota.BackoffLevel != 0 {
		t.Fatalf("updated model quota = %+v, want cleared", state.Quota)
	}
}

func TestResetQuota_DoesNotAcceptAuthIDOrFileName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       "reset-auth-id-only",
		FileName: "reset-auth-file-only.json",
		Provider: "claude",
		Status:   coreauth.StatusError,
	}
	authIndex := auth.EnsureIndex()
	if authIndex == auth.ID || authIndex == auth.FileName {
		t.Fatalf("test auth_index unexpectedly matches id or file name: %q", authIndex)
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	tests := []struct {
		name     string
		body     string
		wantCode int
	}{
		{name: "auth_id field ignored", body: `{"auth_id":"reset-auth-id-only"}`, wantCode: http.StatusBadRequest},
		{name: "id field ignored", body: `{"id":"reset-auth-id-only"}`, wantCode: http.StatusBadRequest},
		{name: "file name is not an index", body: `{"auth_index":"reset-auth-file-only.json"}`, wantCode: http.StatusNotFound},
		{name: "auth id is not an index", body: `{"auth_index":"reset-auth-id-only"}`, wantCode: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(rec)
			req := httptest.NewRequest(http.MethodPost, "/v0/management/reset-quota", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			h.ResetQuota(ctx)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d with body %s", rec.Code, tt.wantCode, rec.Body.String())
			}
		})
	}
}

func TestNormalizeKeyHash_RawKeyBecomesHash(t *testing.T) {
	rawKey := "sk-test-api-key-12345"
	expected := quota.KeyHash(rawKey)

	got := normalizeKeyHash(rawKey)
	if got != expected {
		t.Fatalf("normalizeKeyHash(%q) = %q, want %q", rawKey, got, expected)
	}
}

func TestNormalizeKeyHash_HashPassesThrough(t *testing.T) {
	hash := quota.KeyHash("sk-any-key")
	if len(hash) != 8 {
		t.Fatalf("expected 8-char hash, got %q", hash)
	}

	got := normalizeKeyHash(hash)
	if got != hash {
		t.Fatalf("normalizeKeyHash(%q) = %q, want %q", hash, got, hash)
	}
}

func TestNormalizeKeyHash_InvalidInput(t *testing.T) {
	if got := normalizeKeyHash(""); got != "" {
		t.Fatalf("expected empty for empty input, got %q", got)
	}
	if got := normalizeKeyHash("not-hex!"); got == "" || len(got) != 8 {
		t.Fatalf("expected hash for not-hex input, got %q", got)
	}
}

func TestPutQuotaConfig_UpdateConfigNotifiesManager(t *testing.T) {
	qm, err := quota.NewManager(quota.QuotaConfig{Enabled: false})
	if err != nil {
		t.Fatalf("NewManager failed: %v", err)
	}
	defer qm.Stop()

	h := NewHandlerWithoutConfigFilePath(&config.Config{Quota: quota.QuotaConfig{Enabled: false}}, nil)
	h.SetQuotaManager(qm)

	// Simulate PutQuotaConfig
	h.mu.Lock()
	h.cfg.Quota.Enabled = true
	h.cfg.Quota.Default.DailyCents = 500
	h.cfg.Quota.Default.WeeklyCents = 2000
	if h.quotaManager != nil {
		h.quotaManager.UpdateConfig(h.cfg.Quota)
	}
	h.mu.Unlock()

	// Verify through the manager
	cfg := qm.Config()
	if !cfg.Enabled {
		t.Fatal("expected enabled=true after UpdateConfig")
	}
	if cfg.Default.DailyCents != 500 {
		t.Fatalf("expected daily_cents=500, got %d", cfg.Default.DailyCents)
	}
	if cfg.Default.WeeklyCents != 2000 {
		t.Fatalf("expected weekly_cents=2000, got %d", cfg.Default.WeeklyCents)
	}
}
