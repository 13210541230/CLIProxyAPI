package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestManagementProbeContextIsPinnedAndNotClientSupplied(t *testing.T) {
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		marked bool
	}{
		{"internal pinned", WithManagementCredentialProbe(context.Background(), "auth-a"), true},
		{"ordinary pinned", WithPinnedAuthID(context.Background(), "auth-a"), false},
		{"empty target", WithManagementCredentialProbe(context.Background(), ""), false},
		{"changed target", WithPinnedAuthID(WithManagementCredentialProbe(context.Background(), "auth-a"), "auth-b"), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			meta := requestExecutionMetadata(tc.ctx)
			if marked, _ := meta["management_credential_probe"].(bool); marked != tc.marked {
				t.Fatalf("capability = %v", meta)
			}
			if _, exists := meta["quota_key_hash"]; exists {
				t.Fatal("management probe impersonated a business API key")
			}
		})
	}
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("management_credential_probe", "true")
	c.Request.Header.Set("source", "plugin_host_model_callback")
	c.Set("management_credential_probe", true)
	ctx := WithPinnedAuthID(context.WithValue(context.Background(), "gin", c), "auth-a")
	if requestExecutionMetadata(ctx)["management_credential_probe"] != nil {
		t.Fatal("HTTP client acquired management probe scope")
	}
}
