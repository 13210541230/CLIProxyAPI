package quota

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func newDowngradeRouterTestManager(t *testing.T) *Manager {
	t.Helper()
	manager, err := NewManager(QuotaConfig{DBPath: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager(): %v", err)
	}
	t.Cleanup(manager.Stop)
	return manager
}

func TestDowngradeRouterRoutesEligibleRequest(t *testing.T) {
	manager := newDowngradeRouterTestManager(t)
	keyHash := KeyHash("router-key")
	if err := manager.DowngradeKey(keyHash, automaticPauseReason, "gpt-5.6-luna", timeHourFromNow()); err != nil {
		t.Fatalf("DowngradeKey(): %v", err)
	}

	router := NewDowngradeRouter(manager)
	resp, err := router.RouteModel(context.Background(), pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "gpt-5.6-sol",
		Metadata: map[string]any{
			KeyHashMetadataKey: keyHash,
			"request_path":     "/v1/chat/completions",
		},
	})
	if err != nil {
		t.Fatalf("RouteModel(): %v", err)
	}
	if !resp.Handled || resp.TargetKind != pluginapi.ModelRouteTargetProvider || resp.Target != "codex" || resp.TargetModel != "gpt-5.6-luna" {
		t.Fatalf("route response = %#v", resp)
	}
}

func TestDowngradeRouterExcludesTokenCountAndMediaRequests(t *testing.T) {
	manager := newDowngradeRouterTestManager(t)
	keyHash := KeyHash("router-exclusion-key")
	if err := manager.DowngradeKey(keyHash, automaticPauseReason, "gpt-5.6-luna", timeHourFromNow()); err != nil {
		t.Fatalf("DowngradeKey(): %v", err)
	}
	router := NewDowngradeRouter(manager)

	for _, path := range []string{
		"/v1/messages/count_tokens",
		"/v1beta/models/gpt-5.6-sol:countTokens",
		"/v1/images/generations",
		"/v1/realtime",
		"/v1/models",
	} {
		resp, err := router.RouteModel(context.Background(), pluginapi.ModelRouteRequest{
			SourceFormat:   "openai",
			RequestedModel: "gpt-5.6-sol",
			Metadata: map[string]any{
				KeyHashMetadataKey: keyHash,
				"request_path":     path,
			},
		})
		if err != nil {
			t.Fatalf("RouteModel(%q): %v", path, err)
		}
		if resp.Handled {
			t.Fatalf("RouteModel(%q) = %#v, want unhandled", path, resp)
		}
	}
}

func TestDowngradeRouterDoesNotLoopOnFallbackModel(t *testing.T) {
	manager := newDowngradeRouterTestManager(t)
	keyHash := KeyHash("router-loop-key")
	if err := manager.DowngradeKey(keyHash, automaticPauseReason, "gpt-5.6-luna", timeHourFromNow()); err != nil {
		t.Fatalf("DowngradeKey(): %v", err)
	}

	resp, err := NewDowngradeRouter(manager).RouteModel(context.Background(), pluginapi.ModelRouteRequest{
		SourceFormat:   "openai-response",
		RequestedModel: "gpt-5.6-luna(high)",
		Metadata: map[string]any{
			KeyHashMetadataKey: keyHash,
		},
	})
	if err != nil {
		t.Fatalf("RouteModel(): %v", err)
	}
	if resp.Handled {
		t.Fatalf("route response = %#v, want unhandled", resp)
	}
}

func TestDowngradeRouterRequiresHashedMetadata(t *testing.T) {
	manager := newDowngradeRouterTestManager(t)
	if err := manager.DowngradeKey(KeyHash("router-secret-key"), automaticPauseReason, "gpt-5.6-luna", timeHourFromNow()); err != nil {
		t.Fatalf("DowngradeKey(): %v", err)
	}

	resp, err := NewDowngradeRouter(manager).RouteModel(context.Background(), pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "gpt-5.6-sol",
		Metadata:       map[string]any{"userApiKey": "router-secret-key"},
	})
	if err != nil {
		t.Fatalf("RouteModel(): %v", err)
	}
	if resp.Handled {
		t.Fatalf("route response = %#v, want unhandled without hashed metadata", resp)
	}
}

func timeHourFromNow() time.Time { return time.Now().Add(time.Hour) }
