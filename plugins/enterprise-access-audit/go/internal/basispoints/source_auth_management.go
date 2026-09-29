package basispoints

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const sourceAuthAPI = "/v0/management/enterprise-access-audit/basispoints/source-auths"

type sourceAuthEntry struct {
	Index    string `json:"auth_index"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
	Path     string `json:"path"`
	Runtime  bool   `json:"runtime_only"`
	Disabled bool   `json:"disabled"`
}

type sourceAuthManagementRequest struct {
	Method     string `json:"Method"`
	Path       string `json:"Path"`
	Body       []byte `json:"Body"`
	CallbackID string `json:"host_callback_id"`
}

func (s *Service) registerSourceAuthManagement(_ []byte) (any, error) {
	return map[string]any{
		"routes": []map[string]string{
			{"Method": "GET", "Path": sourceAuthAPI},
			{"Method": "PATCH", "Path": sourceAuthAPI},
		},
	}, nil
}

func sourceAuthResponse(status int, body any) map[string]any {
	return map[string]any{"StatusCode": status, "Headers": http.Header{
		"Content-Type": {"application/json; charset=utf-8"}, "Cache-Control": {"no-store"},
		"X-Content-Type-Options": {"nosniff"},
	}, "Body": jsonBytes(body)}
}

func (s *Service) handleSourceAuthManagement(raw []byte) (any, error) {
	var request sourceAuthManagementRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fail(400, "invalid_request", "invalid management request")
	}
	if request.Path != sourceAuthAPI || (request.Method != http.MethodGet && request.Method != http.MethodPatch) {
		return sourceAuthResponse(404, map[string]string{"error": "not found"}), nil
	}
	var result any
	var err error
	if request.Method == http.MethodGet {
		result, err = s.listSourceAuthSettings(request.CallbackID)
	} else {
		result, err = s.saveSourceAuthSettings(request)
	}
	if err != nil {
		var api *APIError
		if errors.As(err, &api) {
			return sourceAuthResponse(api.Status, map[string]string{"error": api.Message}), nil
		}
		return sourceAuthResponse(502, map[string]string{"error": "账号端点设置读取或保存失败，请检查 CPA 插件日志。"}), nil
	}
	return sourceAuthResponse(200, result), nil
}

func (s *Service) sourceAuthEntries(callbackID string) ([]sourceAuthEntry, error) {
	var response struct {
		Files []sourceAuthEntry `json:"files"`
	}
	if err := s.call("host.auth.list", map[string]any{"host_callback_id": callbackID}, &response); err != nil {
		return nil, err
	}
	entries := make([]sourceAuthEntry, 0, len(response.Files))
	seen := make(map[string]bool)
	for _, entry := range response.Files {
		provider := strings.ToLower(strings.TrimSpace(entry.Provider))
		path := filepath.Clean(strings.TrimSpace(entry.Path))
		if provider != AuthProviderID || entry.Runtime || strings.TrimSpace(entry.Index) == "" ||
			entry.Name == "" || filepath.Base(entry.Name) != entry.Name || !strings.HasSuffix(strings.ToLower(entry.Name), ".json") ||
			path == "." || seen[entry.Index] {
			continue
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		seen[entry.Index] = true
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Name != entries[j].Name {
			return entries[i].Name < entries[j].Name
		}
		return entries[i].Index < entries[j].Index
	})
	return entries, nil
}

func (s *Service) listSourceAuthSettings(callbackID string) (any, error) {
	entries, err := s.sourceAuthEntries(callbackID)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	manager := s.state
	s.mu.RUnlock()
	if manager == nil {
		return nil, errors.New("enterprise access audit state is unavailable")
	}
	enabled, err := manager.ListBasisPointsEnabled(context.Background())
	if err != nil {
		return nil, err
	}
	files := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		files = append(files, map[string]any{
			"auth_index":          entry.Index,
			"name":                entry.Name,
			"disabled":            entry.Disabled,
			"basispoints_enabled": enabled[entry.Index],
		})
	}
	return map[string]any{"files": files}, nil
}

func (s *Service) saveSourceAuthSettings(request sourceAuthManagementRequest) (any, error) {
	var patch struct {
		Index   string `json:"auth_index"`
		Enabled *bool  `json:"basispoints_enabled"`
	}
	decoder := json.NewDecoder(bytes.NewReader(request.Body))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&patch) != nil || decoder.Decode(new(any)) != io.EOF || strings.TrimSpace(patch.Index) == "" || patch.Enabled == nil {
		return nil, fail(400, "invalid_patch", "必须提供 auth_index 和 basispoints_enabled。")
	}
	entries, err := s.sourceAuthEntries(request.CallbackID)
	if err != nil {
		return nil, err
	}
	found := false
	for _, entry := range entries {
		if entry.Index == patch.Index {
			found = true
			break
		}
	}
	if !found {
		return nil, fail(404, "auth_not_found", "认证账号不存在或已重新加载，请刷新列表后重试。")
	}
	s.mu.RLock()
	manager := s.state
	s.mu.RUnlock()
	if manager == nil {
		return nil, errors.New("enterprise access audit state is unavailable")
	}
	if err := manager.SetBasisPointsEnabled(context.Background(), patch.Index, *patch.Enabled); err != nil {
		return nil, err
	}
	return map[string]any{"saved": true, "basispoints_enabled": *patch.Enabled}, nil
}
