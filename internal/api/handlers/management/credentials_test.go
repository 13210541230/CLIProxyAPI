package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// Unlike /auth-files, the credentials list must include config-sourced
// provider entries so every provider credential can be concurrency limited.
func TestListAuthCredentialsIncludesConfigEntries(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	manager := coreauth.NewManager(nil, nil, nil)
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "file-1",
		FileName: "codex.json",
		Provider: "codex",
		Label:    "codex-oauth",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path":   t.TempDir() + "/codex.json",
			"source": t.TempDir() + "/codex.json",
		},
	})
	registerAuthForLookupTest(t, manager, &coreauth.Auth{
		ID:       "key-1",
		Provider: "openai",
		Label:    "openai-compat-x",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"source":       "config:openai[sk-test]",
			"config_index": "0",
			"auth_kind":    "apikey",
		},
	})

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-credentials", nil)

	h.ListAuthCredentials(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Credentials []struct {
			ID       string `json:"id"`
			Provider string `json:"provider"`
			Kind     string `json:"auth_kind"`
			Source   string `json:"source"`
			Disabled bool   `json:"disabled"`
		} `json:"credentials"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode: %v body=%s", errDecode, rec.Body.String())
	}
	byID := map[string]bool{}
	for _, cred := range payload.Credentials {
		byID[cred.ID] = true
	}
	if !byID["file-1"] || !byID["key-1"] {
		t.Fatalf("credentials ids = %v, want both file-1 and key-1", byID)
	}
	for _, cred := range payload.Credentials {
		if cred.ID == "key-1" {
			if cred.Source != "config" || cred.Kind != "apikey" || cred.Provider != "openai" || cred.Disabled {
				t.Fatalf("config entry fields = %+v, want source=config kind=apikey provider=openai enabled", cred)
			}
		}
		if cred.ID == "file-1" && cred.Source != "file" {
			t.Fatalf("file entry source = %q, want file", cred.Source)
		}
	}

	// The auth-files view keeps excluding config entries (file semantics).
	filesRec := httptest.NewRecorder()
	filesCtx, _ := gin.CreateTestContext(filesRec)
	filesCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/auth-files", nil)
	h.ListAuthFiles(filesCtx)
	if filesRec.Code != http.StatusOK {
		t.Fatalf("auth-files status = %d body=%s", filesRec.Code, filesRec.Body.String())
	}
	if contains := jsonContainsID(filesRec.Body.Bytes(), "key-1"); contains {
		t.Fatalf("auth-files must not include config entry key-1: %s", filesRec.Body.String())
	}
}

func jsonContainsID(body []byte, id string) bool {
	var payload struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
	}
	if errDecode := json.Unmarshal(body, &payload); errDecode != nil {
		return false
	}
	for _, file := range payload.Files {
		if file.ID == id {
			return true
		}
	}
	return false
}
