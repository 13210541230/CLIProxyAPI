package quota

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type modelRouterDelegate interface {
	RouteModel(context.Context, pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool)
}

type modelRouterSkipDelegate interface {
	RouteModelExcept(context.Context, pluginapi.ModelRouteRequest, string) (pluginapi.ModelRouteResponse, bool)
}

type modelRouterDetector interface {
	HasModelRouters() bool
}

type modelRouterSkipDetector interface {
	HasModelRoutersExcept(string) bool
}

type builtinProviderLister interface {
	BuiltinProviders() []string
}

// ModelRouterHost composes the built-in quota router with the dynamic plugin host.
// Quota decisions always run first; unhandled requests preserve plugin-host behavior.
type ModelRouterHost struct {
	quota    *DowngradeRouter
	delegate any
}

// NewModelRouterHost creates a quota-first model-router host.
func NewModelRouterHost(manager *Manager, delegate any) *ModelRouterHost {
	return &ModelRouterHost{
		quota:    NewDowngradeRouter(manager),
		delegate: delegate,
	}
}

// RouteModel routes through quota first and then the dynamic delegate.
func (h *ModelRouterHost) RouteModel(ctx context.Context, req pluginapi.ModelRouteRequest) (pluginapi.ModelRouteResponse, bool) {
	if h == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	req = h.withAvailableProviders(req)
	if h.quota != nil {
		resp, errRoute := h.quota.RouteModel(ctx, req)
		if errRoute == nil && resp.Handled {
			return resp, true
		}
	}
	return h.routeDelegate(ctx, req, "")
}

// RouteModelExcept preserves skip-plugin semantics for the dynamic delegate.
func (h *ModelRouterHost) RouteModelExcept(ctx context.Context, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if h == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	req = h.withAvailableProviders(req)
	if h.quota != nil {
		resp, errRoute := h.quota.RouteModel(ctx, req)
		if errRoute == nil && resp.Handled {
			return resp, true
		}
	}
	return h.routeDelegate(ctx, req, skipPluginID)
}

// HasModelRouters reports whether either the built-in or dynamic router is present.
func (h *ModelRouterHost) HasModelRouters() bool {
	if h == nil {
		return false
	}
	if h.quota != nil {
		return true
	}
	if detector, ok := h.delegate.(modelRouterDetector); ok {
		return detector.HasModelRouters()
	}
	return false
}

// HasModelRoutersExcept reports router availability while preserving skip detection.
func (h *ModelRouterHost) HasModelRoutersExcept(skipPluginID string) bool {
	if h == nil {
		return false
	}
	if h.quota != nil {
		return true
	}
	if detector, ok := h.delegate.(modelRouterSkipDetector); ok {
		return detector.HasModelRoutersExcept(skipPluginID)
	}
	if detector, ok := h.delegate.(modelRouterDetector); ok && skipPluginID == "" {
		return detector.HasModelRouters()
	}
	return false
}

func (h *ModelRouterHost) routeDelegate(ctx context.Context, req pluginapi.ModelRouteRequest, skipPluginID string) (pluginapi.ModelRouteResponse, bool) {
	if h == nil || h.delegate == nil {
		return pluginapi.ModelRouteResponse{}, false
	}
	if skipPluginID != "" {
		delegate, ok := h.delegate.(modelRouterSkipDelegate)
		if !ok {
			return pluginapi.ModelRouteResponse{}, false
		}
		return delegate.RouteModelExcept(ctx, req, skipPluginID)
	}
	delegate, ok := h.delegate.(modelRouterDelegate)
	if !ok {
		return pluginapi.ModelRouteResponse{}, false
	}
	return delegate.RouteModel(ctx, req)
}

func (h *ModelRouterHost) withAvailableProviders(req pluginapi.ModelRouteRequest) pluginapi.ModelRouteRequest {
	if len(req.AvailableProviders) > 0 || h == nil || h.delegate == nil {
		return req
	}
	lister, ok := h.delegate.(builtinProviderLister)
	if !ok {
		return req
	}
	providers := lister.BuiltinProviders()
	if len(providers) == 0 {
		return req
	}
	req.AvailableProviders = append([]string(nil), providers...)
	return req
}
