package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestBuildConfigModelsIgnoresConfiguredContextWindowAndUsesDefault(t *testing.T) {
	models := []config.CodexModel{{
		Name:          "gpt-5-codex",
		Alias:         "codex-custom",
		ContextWindow: 262144,
	}}

	out := buildConfigModels(models, "openai", "openai")
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 200000 {
		t.Fatalf("expected default context length 200000, got %d", got)
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

func TestOpenAICompatConfigModelsUseDefaultContextWindow(t *testing.T) {
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
		contextLength := defaultModelContextWindow
		maxCompletionTokens := 0
		if upstream := registry.LookupStaticModelInfo(m.Name); upstream != nil {
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
	if got := ms[0].ContextLength; got != 200000 {
		t.Fatalf("expected default context length 200000, got %d", got)
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
	models := []*ModelInfo{{ID: "gpt-5", Name: "models/gpt-5"}}

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
	models := []*ModelInfo{{ID: "gpt-5", Name: "models/gpt-5", ContextLength: 200000}}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 2 {
		t.Fatalf("expected 2 models, got %d", len(out))
	}
	if out[1].ContextLength != 200000 {
		t.Fatalf("expected forked alias context length unchanged at 200000, got %d", out[1].ContextLength)
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

func TestApplyOAuthModelAlias_DoesNotOverrideContextWindow(t *testing.T) {
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
	if got := out[0].ContextLength; got != 200000 {
		t.Fatalf("expected context length unchanged at 200000, got %d", got)
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
	models := []*ModelInfo{{ID: "gpt-5", Name: "models/gpt-5"}}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	if len(out) != 3 {
		t.Fatalf("expected 3 models, got %d", len(out))
	}
}

func TestApplyGlobalAliasContextWindow_OverridesByAlias(t *testing.T) {
	cfg := &config.Config{ModelAliasContextWindow: map[string]int{"g5": 272000}}
	models := []*ModelInfo{{ID: "g5", ContextLength: 200000}}

	out := applyGlobalAliasContextWindow(cfg, models)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 272000 {
		t.Fatalf("expected global alias context length 272000, got %d", got)
	}
}

func TestApplyGlobalAliasContextWindow_BeatsOAuthAliasContextWindow(t *testing.T) {
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"codex": {{Name: "gpt-5", Alias: "g5", ContextWindow: 262144}},
		},
		ModelAliasContextWindow: map[string]int{"g5": 272000},
	}
	models := []*ModelInfo{{ID: "gpt-5", Name: "models/gpt-5", ContextLength: 200000}}

	out := applyOAuthModelAlias(cfg, "codex", "oauth", models)
	out = applyGlobalAliasContextWindow(cfg, out)
	if len(out) != 1 {
		t.Fatalf("expected 1 model, got %d", len(out))
	}
	if got := out[0].ContextLength; got != 272000 {
		t.Fatalf("expected global alias context length 272000, got %d", got)
	}
}
