package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/quota"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataUsesConcreteGeminiPathAndQuotaHash(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gpt-5.6-sol:countTokens", nil)
	ginCtx.Set("userApiKey", "metadata-secret")
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	meta := requestExecutionMetadata(ctx)
	if got := meta[coreexecutor.RequestPathMetadataKey]; got != "/v1beta/models/gpt-5.6-sol:countTokens" {
		t.Fatalf("request path metadata = %v", got)
	}
	if got := meta[quota.KeyHashMetadataKey]; got != quota.KeyHash("metadata-secret") {
		t.Fatalf("quota hash metadata = %v, want %q", got, quota.KeyHash("metadata-secret"))
	}
	if got := meta[quota.KeyHashMetadataKey]; got == "metadata-secret" {
		t.Fatal("quota metadata contains raw API key")
	}
}
