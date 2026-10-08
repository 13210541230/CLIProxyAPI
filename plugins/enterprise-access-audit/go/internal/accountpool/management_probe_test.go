package accountpool

import (
	"testing"
	"time"
)

func TestManagementProbeUsesPinnedCandidateWithReadyPolicy(t *testing.T) {
	p := mustPolicy(t, []Pool{{ID: "eng", Name: "Engineering", Enabled: true}}, []Member{{PoolID: "eng", AuthID: "auth-b", Enabled: true}}, []Binding{{APIKeyHash: "abcd1234", PoolID: "eng"}})
	for _, tc := range []struct {
		name        string
		probe       bool
		source, pin string
		extra       bool
		selected    bool
	}{
		{"management", true, "plugin_host_model_callback", "auth-a", false, true},
		{"business", false, "", "auth-a", false, false},
		{"untrusted", true, "http", "auth-a", false, false},
		{"wrong pin", true, "plugin_host_model_callback", "auth-b", false, false},
		{"multiple candidates", true, "plugin_host_model_callback", "auth-a", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newLayeredService(t, true, &p)
			req := SchedulerPickRequest{Provider: "codex", Options: schedulerOptions{Metadata: map[string]any{"source": tc.source, "pinned_auth_id": tc.pin, "management_credential_probe": tc.probe}}, Candidates: []schedulerAuthCandidate{oauthCandidate("auth-a", 0)}}
			if tc.extra {
				req.Candidates = append(req.Candidates, oauthCandidate("auth-b", 0))
			}
			r := svc.Pick(req)
			if tc.selected {
				if r.Decision != "selected" || r.AuthID != "auth-a" {
					t.Fatalf("pinned management: %+v", r)
				}
			} else if r.Decision != "reject" {
				t.Fatalf("missing business identity: %+v", r)
			}
		})
	}
}

func TestManagementProbeRetainsAdmissionLimits(t *testing.T) {
	svc := New(Options{DataDir: t.TempDir(), Enabled: true, MaxWait: time.Millisecond, Reserve: time.Second})
	svc.engine.Configure(map[string]Limit{"auth-a": {Max: 1}})
	metadata := map[string]any{"source": "plugin_host_model_callback", "pinned_auth_id": "auth-a", "management_credential_probe": true, "selected_auth_id": "auth-a"}
	if got := svc.admissionNamespace("auth-a", metadata); got != "management-probe:auth-a" {
		t.Fatalf("admission namespace differs from pick: %q", got)
	}
	if r := svc.AdmitIntercept("probe-1", nil, metadata); r != nil {
		t.Fatalf("first admission: %+v", r)
	}
	if r := svc.AdmitIntercept("probe-2", nil, metadata); r == nil || !r.Terminate {
		t.Fatal("concurrency cap bypassed")
	}
	svc.Complete("probe-1")
	if r := svc.AdmitIntercept("probe-3", nil, metadata); r != nil {
		t.Fatalf("released admission: %+v", r)
	}
	svc.Complete("probe-3")
}
