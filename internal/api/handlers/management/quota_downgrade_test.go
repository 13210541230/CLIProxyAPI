package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quota"
)

func TestDowngradeHandlers_NormalizeKeyAndProtectReason(t *testing.T) {
	gin.SetMode(gin.TestMode)
	qm, err := quota.NewManager(quota.QuotaConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer qm.Stop()

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetQuotaManager(qm)
	rawKey := "sk-downgrade-test-key"
	expectedHash := quota.KeyHash(rawKey)

	invokeQuotaDowngradeHandler(t, h.PostDowngradeKey, http.MethodPost, "/v0/management/quota/downgrade", `{"key_hash":"`+rawKey+`","reason":"spend_limit_exceeded","fallback_model":"gpt-5.6-luna","expires_in_seconds":3600}`, http.StatusOK)
	listRecorder := invokeQuotaDowngradeHandler(t, h.GetDowngradedKeys, http.MethodGet, "/v0/management/quota/downgraded", "", http.StatusOK)
	var list struct {
		Entries []quota.DowngradeEntry `json:"entries"`
	}
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list.Entries) != 1 {
		t.Fatalf("entries = %+v, want one", list.Entries)
	}
	entry := list.Entries[0]
	if entry.KeyHash != expectedHash || entry.KeyHash == rawKey || entry.Reason != "spend_limit_exceeded" || entry.FallbackModel != "gpt-5.6-luna" || entry.DowngradedAt.IsZero() || entry.ExpiresAt.IsZero() || entry.CreatedAt.IsZero() {
		t.Fatalf("unexpected stored entry: %+v", entry)
	}
	createdAt := entry.CreatedAt

	time.Sleep(1100 * time.Millisecond)
	invokeQuotaDowngradeHandler(t, h.PostDowngradeKey, http.MethodPost, "/v0/management/quota/downgrade", `{"key_hash":"`+expectedHash+`","reason":"spend_limit_exceeded","fallback_model":"gpt-5.6-terra","expires_in_seconds":7200}`, http.StatusOK)
	listRecorder = invokeQuotaDowngradeHandler(t, h.GetDowngradedKeys, http.MethodGet, "/v0/management/quota/downgraded", "", http.StatusOK)
	if err := json.Unmarshal(listRecorder.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal refreshed list: %v", err)
	}
	if len(list.Entries) != 1 || !list.Entries[0].CreatedAt.Equal(createdAt) || list.Entries[0].FallbackModel != "gpt-5.6-terra" || !list.Entries[0].DowngradedAt.After(entry.DowngradedAt) {
		t.Fatalf("idempotent refresh entry = %+v, original=%+v", list.Entries, entry)
	}

	invokeQuotaDowngradeHandler(t, h.PostResumeDowngradeKey, http.MethodPost, "/v0/management/quota/downgrade/resume", `{"key_hash":"`+expectedHash+`","expected_reason":"other_reason"}`, http.StatusOK)
	if downgraded, _, err := qm.IsDowngraded(expectedHash); err != nil || !downgraded {
		t.Fatalf("mismatched expected_reason removed state: downgraded=%v err=%v", downgraded, err)
	}
	invokeQuotaDowngradeHandler(t, h.PostResumeDowngradeKey, http.MethodPost, "/v0/management/quota/downgrade/resume", `{"key_hash":"`+expectedHash+`","expected_reason":"spend_limit_exceeded"}`, http.StatusOK)
	invokeQuotaDowngradeHandler(t, h.PostResumeDowngradeKey, http.MethodPost, "/v0/management/quota/downgrade/resume", `{"key_hash":"`+expectedHash+`","expected_reason":"spend_limit_exceeded"}`, http.StatusOK)
}

func TestDowngradeHandlers_RejectInvalidFallbackWithoutWritingState(t *testing.T) {
	gin.SetMode(gin.TestMode)
	qm, err := quota.NewManager(quota.QuotaConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer qm.Stop()

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	h.SetQuotaManager(qm)
	for _, fallbackModel := range []string{"", "unknown-model", "claude-sonnet-4-6", "gpt-image-1.5"} {
		t.Run(fallbackModel, func(t *testing.T) {
			invokeQuotaDowngradeHandler(t, h.PostDowngradeKey, http.MethodPost, "/v0/management/quota/downgrade", `{"key_hash":"abcdef12","fallback_model":"`+fallbackModel+`"}`, http.StatusBadRequest)
		})
	}
	entries, err := qm.ListDowngraded()
	if err != nil {
		t.Fatalf("ListDowngraded: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("invalid fallback wrote state: %+v", entries)
	}
}

func TestPostValidateFallbackModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)
	invokeQuotaDowngradeHandler(t, h.PostValidateFallbackModel, http.MethodPost, "/v0/management/quota/validate-model", `{"fallback_model":"gpt-5.6-luna"}`, http.StatusOK)
	for _, fallbackModel := range []string{"", "unknown-model", "claude-sonnet-4-6", "gpt-image-1.5"} {
		t.Run(fallbackModel, func(t *testing.T) {
			invokeQuotaDowngradeHandler(t, h.PostValidateFallbackModel, http.MethodPost, "/v0/management/quota/validate-model", `{"fallback_model":"`+fallbackModel+`"}`, http.StatusBadRequest)
		})
	}
}

func invokeQuotaDowngradeHandler(t *testing.T, handler gin.HandlerFunc, method, path, body string, wantStatus int) *httptest.ResponseRecorder {
	t.Helper()
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, path, strings.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	handler(ctx)
	if recorder.Code != wantStatus {
		t.Fatalf("%s %s status=%d want=%d body=%s", method, path, recorder.Code, wantStatus, recorder.Body.String())
	}
	return recorder
}
