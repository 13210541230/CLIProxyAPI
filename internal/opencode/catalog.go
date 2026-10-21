package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// DefaultCapabilitiesURL is OpenCode's machine readable provider catalog. Unlike
// /v1/models it declares the SDK (and therefore the wire protocol) per model.
const DefaultCapabilitiesURL = "https://models.opencode.ai/api.json"

// ModelInfo describes one Go-plan model resolved from the catalog.
type ModelInfo struct {
	ID               string
	Protocol         Protocol
	Unsupported      bool
	Context          int
	MaxInput         int
	MaxOutput        int
	Reasoning        bool
	ToolCall         bool
	InputModalities  []string
	OutputModalities []string
}

type capabilityProvider struct {
	ID     string                     `json:"id"`
	API    string                     `json:"api"`
	NPM    string                     `json:"npm"`
	Models map[string]capabilityModel `json:"models"`
}

type capabilityModel struct {
	ID               string                     `json:"id"`
	Provider         *capabilityModelProvider   `json:"provider"`
	Limit            *capabilityModelLimit      `json:"limit"`
	Reasoning        bool                       `json:"reasoning"`
	ToolCall         bool                       `json:"tool_call"`
	StructuredOutput bool                       `json:"structured_output"`
	Modalities       *capabilityModelModalities `json:"modalities"`
}

type capabilityModelProvider struct {
	NPM string `json:"npm"`
}

type capabilityModelLimit struct {
	Context int `json:"context"`
	Input   int `json:"input"`
	Output  int `json:"output"`
}

type capabilityModelModalities struct {
	Input  []string `json:"input"`
	Output []string `json:"output"`
}

type modelsResponse struct {
	Data []struct {
		ID string `json:"id"`
	} `json:"data"`
}

// Catalog caches the Go-plan model set and its protocol mapping.
type Catalog struct {
	mu        sync.RWMutex
	models    map[string]ModelInfo
	overrides map[string]Protocol
	fetchedAt time.Time
	lastErr   error
	loaded    bool
}

// NewCatalog creates a catalog with optional manual protocol overrides.
func NewCatalog(overrides map[string]Protocol) *Catalog {
	normalized := make(map[string]Protocol, len(overrides))
	for model, protocol := range overrides {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		if protocol != ProtocolChat && protocol != ProtocolResponses && protocol != ProtocolAnthropic {
			continue
		}
		normalized[strings.ToLower(model)] = protocol
	}
	return &Catalog{models: make(map[string]ModelInfo), overrides: normalized}
}

// Protocol resolves the wire protocol for a model. The boolean is false when the
// model is unknown, in which case callers should fall back to the inbound format.
func (c *Catalog) Protocol(model string) (Protocol, bool) {
	info, ok := c.Lookup(model)
	if !ok || info.Unsupported {
		return "", false
	}
	return info.Protocol, true
}

// Lookup returns the catalog entry for a model.
func (c *Catalog) Lookup(model string) (ModelInfo, bool) {
	key := strings.ToLower(strings.TrimSpace(model))
	if override, ok := c.overrides[key]; ok {
		return ModelInfo{ID: key, Protocol: override}, true
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	info, ok := c.models[key]
	return info, ok
}

// Models returns the resolved model set sorted by identifier.
func (c *Catalog) Models() []ModelInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]ModelInfo, 0, len(c.models))
	for _, info := range c.models {
		if info.Unsupported {
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// FetchedAt reports when the catalog last refreshed successfully.
func (c *Catalog) FetchedAt() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.fetchedAt
}

// LastError reports the last refresh failure, if any.
func (c *Catalog) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastErr
}

// Loaded reports whether a successful refresh already happened.
func (c *Catalog) Loaded() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.loaded
}

// Refresh reloads the capability catalog and the live /v1/models list.
// Capability failures keep the previous snapshot; model-list failures are tolerated.
func (c *Catalog) Refresh(ctx context.Context, client *http.Client, capabilitiesURL, modelsURL, key string) error {
	if client == nil {
		client = http.DefaultClient
	}
	resolved, errCapabilities := c.fetchCapabilities(ctx, client, capabilitiesURL)
	if errCapabilities != nil {
		c.mu.Lock()
		c.lastErr = errCapabilities
		c.mu.Unlock()
		return errCapabilities
	}
	live, errModels := fetchModelIDs(ctx, client, modelsURL, key)
	c.mu.Lock()
	defer c.mu.Unlock()
	merged := make(map[string]ModelInfo, len(resolved))
	for id, info := range resolved {
		merged[id] = info
	}
	for _, id := range live {
		if _, ok := merged[id]; !ok {
			// Models advertised by /v1/models but missing from the capability
			// catalog are still usable; assume chat until proven otherwise.
			merged[id] = ModelInfo{ID: id, Protocol: ProtocolChat}
		}
	}
	c.models = merged
	c.fetchedAt = time.Now()
	c.loaded = true
	if errModels != nil {
		c.lastErr = errModels
	} else {
		c.lastErr = nil
	}
	return nil
}

func (c *Catalog) fetchCapabilities(ctx context.Context, client *http.Client, endpoint string) (map[string]ModelInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgentValue)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("opencode capability catalog returned HTTP %d", resp.StatusCode)
	}
	var providers map[string]capabilityProvider
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<20)).Decode(&providers); err != nil {
		return nil, err
	}
	out := make(map[string]ModelInfo)
	for providerID, provider := range providers {
		if !isGoProvider(providerID, provider.API) {
			continue
		}
		for modelID, model := range provider.Models {
			if strings.TrimSpace(model.ID) != "" {
				modelID = model.ID
			}
			modelID = strings.TrimSpace(modelID)
			if modelID == "" {
				continue
			}
			npm := provider.NPM
			if model.Provider != nil && strings.TrimSpace(model.Provider.NPM) != "" {
				npm = model.Provider.NPM
			}
			info := ModelInfo{ID: modelID, Reasoning: model.Reasoning, ToolCall: model.ToolCall}
			if protocol, ok := protocolForSDK(npm); ok {
				info.Protocol = protocol
			} else {
				info.Unsupported = true
			}
			if model.Limit != nil {
				info.Context = model.Limit.Context
				info.MaxInput = model.Limit.Input
				info.MaxOutput = model.Limit.Output
			}
			if model.Modalities != nil {
				info.InputModalities = append([]string(nil), model.Modalities.Input...)
				info.OutputModalities = append([]string(nil), model.Modalities.Output...)
			}
			out[strings.ToLower(modelID)] = info
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("opencode capability catalog contained no Go models")
	}
	return out, nil
}

func isGoProvider(providerID, api string) bool {
	value := strings.ToLower(strings.TrimSpace(providerID + " " + api))
	if strings.Contains(value, "opencode-go") || strings.Contains(value, "/go/") {
		return true
	}
	// A bare "opencode" provider without zen markers still describes the paid plan.
	return strings.Contains(value, "opencode") && !strings.Contains(value, "zen") && !strings.Contains(value, "/inference")
}

func protocolForSDK(npm string) (Protocol, bool) {
	value := strings.ToLower(strings.TrimSpace(npm))
	switch {
	case strings.Contains(value, "anthropic"):
		return ProtocolAnthropic, true
	case value == "@ai-sdk/openai" || strings.HasSuffix(value, "/openai"):
		return ProtocolResponses, true
	case strings.Contains(value, "openai-compatible"):
		return ProtocolChat, true
	default:
		return "", false
	}
}

func fetchModelIDs(ctx context.Context, client *http.Client, endpoint, key string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgentValue)
	req.Header.Set("x-opencode-client", "cli")
	if strings.TrimSpace(key) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(key))
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("opencode model list returned HTTP %d", resp.StatusCode)
	}
	var payload modelsResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&payload); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(payload.Data))
	for _, item := range payload.Data {
		if id := strings.TrimSpace(item.ID); id != "" {
			ids = append(ids, strings.ToLower(id))
		}
	}
	return ids, nil
}
