package pluginhost

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
)

func TestHostManagementProbeKeepsIdentityBoundary(t *testing.T) {
	for _, tc := range []struct {
		name                  string
		source, pin           string
		probe, extra, allowed bool
	}{
		{"management pinned", "plugin_host_model_callback", "auth-a", true, false, true},
		{"normal business", "", "auth-a", false, false, false},
		{"untrusted source", "http", "auth-a", true, false, false},
		{"missing pin", "plugin_host_model_callback", "", true, false, false},
		{"wrong pin", "plugin_host_model_callback", "auth-b", true, false, false},
		{"multiple candidates", "plugin_host_model_callback", "auth-a", true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			host := newHostWithRecords(capabilityRecord{id: "pool", plugin: pluginapi.Plugin{Capabilities: pluginapi.Capabilities{SchedulerExclusiveProviders: []string{"codex"}, Scheduler: schedulerFunc(func(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, error) {
				return pluginapi.SchedulerPickResponse{Decision: pluginapi.SchedulerDecisionSelected, AuthID: "auth-a", Handled: true}, nil
			})}}})
			host.exclusiveOwners = map[string][]string{"codex": {"pool"}}
			req := schedulerRequest("auth-a")
			req.Provider = "codex"
			if tc.extra {
				req = schedulerRequest("auth-a", "auth-b")
				req.Provider = "codex"
			}
			req.Options.Metadata = map[string]any{"source": tc.source, "pinned_auth_id": tc.pin, "management_credential_probe": tc.probe}
			resp, handled, err := host.PickAuth(context.Background(), req)
			if tc.allowed {
				if err != nil || !handled || resp.AuthID != "auth-a" {
					t.Fatalf("probe: %v %v %+v", err, handled, resp)
				}
			} else if err == nil || !strings.Contains(err.Error(), "identity_missing") {
				t.Fatalf("business identity boundary: %v", err)
			}
		})
	}
}
