package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBuildConfigModelsCopiesConfiguredContextWindow(t *testing.T) {
	models := []config.CodexModel{{
		Name:          "gpt-5-codex",
		Alias:         "codex-custom",
		ContextWindow: 262144,
	}}

	out := buildConfigModels(models, "openai", "openai")
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 262144 {
		t.Fatalf("expected context length 262144, got %d", got)
	}
}

func TestBuildConfigModelsDefaultsContextWindowTo200K(t *testing.T) {
	models := []config.CodexModel{{
		Name:  "gpt-5-codex",
		Alias: "codex-custom",
	}}

	out := buildConfigModels(models, "openai", "openai")
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 200000 {
		t.Fatalf("expected default context length 200000, got %d", got)
	}
}

func TestBuildGeminiConfigModelsDefaultsContextWindowTo200K(t *testing.T) {
	entry := &config.GeminiKey{Models: []config.GeminiModel{{Name: "gemini-2.5-pro", Alias: "gemini-custom"}}}

	out := buildGeminiConfigModels(entry)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 200000 {
		t.Fatalf("expected default context length 200000, got %d", got)
	}
}

func TestOpenAICompatConfigModelsCarryTokenMetadata(t *testing.T) {
	compat := config.OpenAICompatibility{
		Name: "routin",
		Models: []config.OpenAICompatibilityModel{{
			Name:          "gpt-5.4",
			Alias:         "gpt-5.4",
			ContextWindow: 1048576,
		}},
	}

	ms := make([]*ModelInfo, 0, len(compat.Models))
	for _, m := range compat.Models {
		modelID := m.Alias
		if modelID == "" {
			modelID = m.Name
		}
		thinking := m.Thinking
		if thinking == nil {
			thinking = &registry.ThinkingSupport{Levels: []string{"low", "medium", "high"}}
		}
		contextLength := m.ContextWindow
		maxCompletionTokens := 0
		if upstream := registry.LookupStaticModelInfo(m.Name); upstream != nil {
			if contextLength == 0 {
				contextLength = upstream.ContextLength
			}
			if maxCompletionTokens == 0 {
				maxCompletionTokens = upstream.MaxCompletionTokens
			}
			if m.Thinking == nil && upstream.Thinking != nil {
				thinking = upstream.Thinking
			}
		}
		ms = append(ms, &ModelInfo{
			ID:                  modelID,
			OwnedBy:             compat.Name,
			Type:                "openai-compatibility",
			DisplayName:         modelID,
			ContextLength:       contextLength,
			MaxCompletionTokens: maxCompletionTokens,
			Thinking:            thinking,
		})
	}

	if len(ms) != 1 {
		t.Fatalf("expected 1 model, got %d", len(ms))
	}
	if got := ms[0].ContextLength; got != 1048576 {
		t.Fatalf("expected context length 1048576, got %d", got)
	}
	if got := ms[0].MaxCompletionTokens; got != 128000 {
		t.Fatalf("expected max completion tokens 128000, got %d", got)
	}
}

func TestApplyOAuthModelAlias_Rename(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if out[0].ID != "g5" {
		t.Fatalf("expected model id %q, got %q", "g5", out[0].ID)
	}
	if out[0].Name != "models/g5" {
		t.Fatalf("expected model name %q, got %q", "models/g5", out[0].Name)
	}
}

func TestApplyOAuthModelAlias_ForkAddsAlias(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
}

func TestApplyDefaultModelContextWindow_DefaultsUnsetModelsTo200K(t *testing.T) {
	models := []*ModelInfo{{ID: "gpt-5", ContextLength: 0}, {ID: "gpt-5.4", ContextLength: 1048576}}

	out := applyDefaultModelContextWindow(models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 200000 {
		t.Fatalf("expected default context length 200000, got %d", got)
	}
	if got := out[1].ContextLength; got != 1048576 {
		t.Fatalf("expected existing context length preserved, got %d", got)
	}
}

func TestApplyOAuthModelAlias_ContextWindowOverride(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", ContextWindow: 262144},
			},
		},
	}
	models := []*ModelInfo{{ID: "gpt-5", Name: "models/gpt-5", ContextLength: 200000}}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 262144 {
		t.Fatalf("expected aliased model context length 262144, got %d", got)
	}
}

func TestApplyOAuthModelAlias_ForkAddsMultipleAliases(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5", Fork: true},
				{Name: "gpt-5", Alias: "g5-2", Fork: true},
			},
		},
	}
	models := []*ModelInfo{
		{ID: "gpt-5", Name: "models/gpt-5"},
	}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
	if out[0].ID != "gpt-5" {
		t.Fatalf("expected first model id %q, got %q", "gpt-5", out[0].ID)
	}
	if out[1].ID != "g5" {
		t.Fatalf("expected second model id %q, got %q", "g5", out[1].ID)
	}
	if out[1].Name != "models/g5" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5", out[1].Name)
	}
	if out[2].ID != "g5-2" {
		t.Fatalf("expected third model id %q, got %q", "g5-2", out[2].ID)
	}
	if out[2].Name != "models/g5-2" {
		t.Fatalf("expected forked model name %q, got %q", "models/g5-2", out[2].Name)
	}
}
