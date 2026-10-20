package quota

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type testModelRouterDelegate struct {
	hasRouters bool
	called     bool
	skip       string
}

func (d *testModelRouterDelegate) RouteModel(_ context.Context, _ pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	d.called = true
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "openai", TargetModel: "delegate-model"}, true
}

func (d *testModelRouterDelegate) RouteModelExcept(_ context.Context, _ pluginapi.ModelRouteRequest, skip string) (pluginapi.ModelRouteResponse, bool) {
	d.called = true
	d.skip = skip
	return pluginapi.ModelRouteResponse{Handled: true, TargetKind: pluginapi.ModelRouteTargetProvider, Target: "openai", TargetModel: "delegate-model"}, true
}

func (d *testModelRouterDelegate) HasModelRouters() bool             { return d.hasRouters }
func (d *testModelRouterDelegate) HasModelRoutersExcept(string) bool { return d.hasRouters }

func (d *testModelRouterDelegate) BuiltinProviders() []string { return []string{"codex", "openai"} }

func TestModelRouterHostPrioritizesQuotaAndPreservesDelegateFallback(t *testing.T) {
	manager := newDowngradeRouterTestManager(t)
	keyHash := KeyHash("host-key")
	if err := manager.DowngradeKey(keyHash, automaticPauseReason, "gpt-5.6-luna", timeHourFromNow()); err != nil {
		t.Fatalf("DowngradeKey(): %v", err)
	}
	delegate := &testModelRouterDelegate{hasRouters: true}
	host := NewModelRouterHost(manager, delegate)

	resp, handled := host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "gpt-5.6-sol",
		Metadata: map[string]any{
			KeyHashMetadataKey: keyHash,
			"request_path":     "/v1/chat/completions",
		},
	})
	if !handled || resp.Target != "codex" || resp.TargetModel != "gpt-5.6-luna" {
		t.Fatalf("quota route = handled:%v response:%#v", handled, resp)
	}
	if delegate.called {
		t.Fatal("delegate called before quota route")
	}

	resp, handled = host.RouteModel(context.Background(), pluginapi.ModelRouteRequest{
		SourceFormat:   "openai",
		RequestedModel: "gpt-5.6-sol",
		Metadata:       map[string]any{"request_path": "/v1/chat/completions"},
	})
	if !handled || resp.Target != "openai" || resp.TargetModel != "delegate-model" || !delegate.called {
		t.Fatalf("delegate route = handled:%v response:%#v called:%v", handled, resp, delegate.called)
	}
}

func TestModelRouterHostPreservesSkipPluginSemantics(t *testing.T) {
	delegate := &testModelRouterDelegate{hasRouters: true}
	host := NewModelRouterHost(nil, delegate)
	resp, handled := host.RouteModelExcept(context.Background(), pluginapi.ModelRouteRequest{SourceFormat: "openai"}, "origin-plugin")
	if !handled || resp.TargetModel != "delegate-model" || delegate.skip != "origin-plugin" {
		t.Fatalf("skip route = handled:%v response:%#v skip:%q", handled, resp, delegate.skip)
	}
	if !host.HasModelRoutersExcept("origin-plugin") {
		t.Fatal("HasModelRoutersExcept() = false, want delegate detector result")
	}
}
