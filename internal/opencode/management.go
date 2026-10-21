package opencode

import (
	"context"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

var defaultExecutor atomic.Pointer[Executor]

// Default returns the executor instance created at startup, if any.
func Default() *Executor { return defaultExecutor.Load() }

// AccountView is the read-only account/quota projection returned by the management API.
type AccountView struct {
	ID         string     `json:"id"`
	Label      string     `json:"label,omitempty"`
	Provider   string     `json:"provider"`
	Status     string     `json:"status"`
	Disabled   bool       `json:"disabled"`
	ProxyURL   string     `json:"proxy_url,omitempty"`
	KeyHint    string     `json:"api_key_hint,omitempty"`
	Quota      *QuotaView `json:"quota,omitempty"`
	QuotaError string     `json:"quota_error,omitempty"`
}

// QuotaView is the JSON projection of one account quota snapshot.
type QuotaView struct {
	Rolling   WindowView `json:"rolling"`
	Weekly    WindowView `json:"weekly"`
	Monthly   WindowView `json:"monthly"`
	FetchedAt string     `json:"fetched_at,omitempty"`
	Exhausted bool       `json:"exhausted"`
}

// WindowView is the JSON projection of a single quota window.
type WindowView struct {
	Status   string  `json:"status,omitempty"`
	Percent  float64 `json:"percent"`
	ResetsAt string  `json:"resets_at,omitempty"`
}

// QuotaEndpoint returns the usage endpoint for the executor base URL.
func (e *Executor) QuotaEndpoint() string {
	base := defaultBaseURL
	if e != nil && strings.TrimSpace(e.baseURL) != "" {
		base = strings.TrimRight(e.baseURL, "/")
	}
	return base + goQuotaPath
}

// RefreshQuotas refreshes quota snapshots for every OpenCode Go auth.
func (e *Executor) RefreshQuotas(ctx context.Context, auths []*coreauth.Auth, maxAge time.Duration) {
	if e == nil {
		return
	}
	endpoint := e.QuotaEndpoint()
	client := helps.NewProxyAwareHTTPClient(ctx, e.cfg, nil, 20*time.Second)
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(auth.Provider, ProviderName) {
			continue
		}
		if existing, ok := e.quota.Snapshot(auth.ID); ok && maxAge > 0 && time.Since(existing.FetchedAt) < maxAge {
			continue
		}
		key := APIKey(auth)
		if key == "" {
			e.quota.Record(auth.ID, QuotaSnapshot{FetchedAt: time.Now(), Err: "missing api_key"})
			continue
		}
		snapshot, err := FetchQuota(ctx, client, endpoint, key)
		if err != nil {
			snapshot = QuotaSnapshot{FetchedAt: time.Now(), Err: err.Error()}
		}
		e.quota.Record(auth.ID, snapshot)
	}
}

// AccountViews projects OpenCode auths with their quota snapshot.
func (e *Executor) AccountViews(auths []*coreauth.Auth) []AccountView {
	views := make([]AccountView, 0, len(auths))
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(auth.Provider, ProviderName) {
			continue
		}
		view := AccountView{
			ID:       auth.ID,
			Label:    accountLabel(auth),
			Provider: ProviderName,
			Status:   string(auth.Status),
			Disabled: auth.Disabled,
			ProxyURL: auth.ProxyURL,
			KeyHint:  maskKey(APIKey(auth)),
		}
		if e != nil {
			if snapshot, ok := e.quota.Snapshot(auth.ID); ok {
				view.Quota = quotaView(snapshot)
				view.QuotaError = snapshot.Err
			}
		}
		views = append(views, view)
	}
	sort.Slice(views, func(i, j int) bool { return views[i].ID < views[j].ID })
	return views
}

// SelectAccount prefers the account with the most remaining quota headroom.
// Accounts already exhausted or failing quota lookups are skipped when an
// alternative exists.
func (e *Executor) SelectAccount(auths []*coreauth.Auth) *coreauth.Auth {
	var best *coreauth.Auth
	bestScore := -1.0
	for _, auth := range auths {
		if auth == nil || !strings.EqualFold(auth.Provider, ProviderName) {
			continue
		}
		if auth.Disabled || strings.EqualFold(string(auth.Status), "disabled") {
			continue
		}
		score := 100.0
		if e != nil {
			if snapshot, ok := e.quota.Snapshot(auth.ID); ok {
				if snapshot.Exhausted() {
					continue
				}
				score = 100 - snapshot.MinPercent()
			}
		}
		if score > bestScore {
			best, bestScore = auth, score
		}
	}
	return best
}

// accountLabel prefers the label from the auth file over the provider name that
// the generic loader assigns to non-standard auth types.
func accountLabel(auth *coreauth.Auth) string {
	if auth == nil {
		return ""
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata["label"].(string); ok {
			if trimmed := strings.TrimSpace(value); trimmed != "" {
				return trimmed
			}
		}
	}
	if !strings.EqualFold(auth.Label, ProviderName) {
		return auth.Label
	}
	return ""
}

func quotaView(snapshot QuotaSnapshot) *QuotaView {
	return &QuotaView{
		Rolling:   windowView(snapshot.Rolling),
		Weekly:    windowView(snapshot.Weekly),
		Monthly:   windowView(snapshot.Monthly),
		FetchedAt: formatTime(snapshot.FetchedAt),
		Exhausted: snapshot.Exhausted(),
	}
}

func windowView(window Window) WindowView {
	return WindowView{Status: window.Status, Percent: window.Percent, ResetsAt: formatTime(window.ResetsAt)}
}

func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

func maskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 10 {
		return strings.Repeat("*", len(key))
	}
	return key[:6] + "…" + strings.Repeat("*", 4)
}
