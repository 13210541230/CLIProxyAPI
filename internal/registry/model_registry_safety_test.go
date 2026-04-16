package registry

import (
	"strings"
	"testing"
	"time"
)

func TestGetModelInfoReturnsClone(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Min: 1, Max: 2, Levels: []string{"low", "high"}},
	}})

	first := r.GetModelInfo("m1", "gemini")
	if first == nil {
		t.Fatal("expected model info")
	}
	first.DisplayName = "mutated"
	first.Thinking.Levels[0] = "mutated"

	second := r.GetModelInfo("m1", "gemini")
	if second.DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second.DisplayName)
	}
	if second.Thinking == nil || len(second.Thinking.Levels) == 0 || second.Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second.Thinking)
	}
}

func TestGetModelsForClientReturnsClones(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Levels: []string{"low", "high"}},
	}})

	first := r.GetModelsForClient("client-1")
	if len(first) != 1 || first[0] == nil {
		t.Fatalf("expected one model, got %+v", first)
	}
	first[0].DisplayName = "mutated"
	first[0].Thinking.Levels[0] = "mutated"

	second := r.GetModelsForClient("client-1")
	if len(second) != 1 || second[0] == nil {
		t.Fatalf("expected one model on second fetch, got %+v", second)
	}
	if second[0].DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second[0].DisplayName)
	}
	if second[0].Thinking == nil || len(second[0].Thinking.Levels) == 0 || second[0].Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second[0].Thinking)
	}
}

func TestGetAvailableModelsByProviderReturnsClones(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "gemini", []*ModelInfo{{
		ID:          "m1",
		DisplayName: "Model One",
		Thinking:    &ThinkingSupport{Levels: []string{"low", "high"}},
	}})

	first := r.GetAvailableModelsByProvider("gemini")
	if len(first) != 1 || first[0] == nil {
		t.Fatalf("expected one model, got %+v", first)
	}
	first[0].DisplayName = "mutated"
	first[0].Thinking.Levels[0] = "mutated"

	second := r.GetAvailableModelsByProvider("gemini")
	if len(second) != 1 || second[0] == nil {
		t.Fatalf("expected one model on second fetch, got %+v", second)
	}
	if second[0].DisplayName != "Model One" {
		t.Fatalf("expected cloned display name, got %q", second[0].DisplayName)
	}
	if second[0].Thinking == nil || len(second[0].Thinking.Levels) == 0 || second[0].Thinking.Levels[0] != "low" {
		t.Fatalf("expected cloned thinking levels, got %+v", second[0].Thinking)
	}
}

func TestCleanupExpiredQuotasInvalidatesAvailableModelsCache(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1", Created: 1}})
	r.SetModelQuotaExceeded("client-1", "m1")
	if models := r.GetAvailableModels("openai"); len(models) != 1 {
		t.Fatalf("expected cooldown model to remain listed before cleanup, got %d", len(models))
	}

	r.mutex.Lock()
	quotaTime := time.Now().Add(-6 * time.Minute)
	r.models["m1"].QuotaExceededClients["client-1"] = &quotaTime
	r.mutex.Unlock()

	r.CleanupExpiredQuotas()

	if count := r.GetModelCount("m1"); count != 1 {
		t.Fatalf("expected model count 1 after cleanup, got %d", count)
	}
	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected model to stay available after cleanup, got %d", len(models))
	}
	if got := models[0]["id"]; got != "m1" {
		t.Fatalf("expected model id m1, got %v", got)
	}
}

func TestGetAvailableModelsReturnsClonedSupportedParameters(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:                  "m1",
		DisplayName:         "Model One",
		SupportedParameters: []string{"temperature", "top_p"},
	}})

	first := r.GetAvailableModels("openai")
	if len(first) != 1 {
		t.Fatalf("expected one model, got %d", len(first))
	}
	params, ok := first[0]["supported_parameters"].([]string)
	if !ok || len(params) != 2 {
		t.Fatalf("expected supported_parameters slice, got %#v", first[0]["supported_parameters"])
	}
	params[0] = "mutated"

	second := r.GetAvailableModels("openai")
	params, ok = second[0]["supported_parameters"].([]string)
	if !ok || len(params) != 2 || params[0] != "temperature" {
		t.Fatalf("expected cloned supported_parameters, got %#v", second[0]["supported_parameters"])
	}
}

func TestGetAvailableModelsMapsClaudeTokenMetadata(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "claude", []*ModelInfo{{
		ID:                  "claude-sonnet",
		OwnedBy:             "anthropic",
		ContextLength:       200000,
		MaxCompletionTokens: 8192,
		DisplayName:         "Claude Sonnet",
	}})

	models := r.GetAvailableModels("claude")
	if len(models) != 1 {
		t.Fatalf("expected one model, got %d", len(models))
	}
	if got := models[0]["max_input_tokens"]; got != 200000 {
		t.Fatalf("expected max_input_tokens 200000, got %#v", got)
	}
	if got := models[0]["max_tokens"]; got != 8192 {
		t.Fatalf("expected max_tokens 8192, got %#v", got)
	}
}

func TestGetAvailableModelsMapsOpenAIMetadataForCodex(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{
		ID:                  "gpt-5-codex",
		OwnedBy:             "openai",
		ContextLength:       400000,
		MaxCompletionTokens: 32000,
	}})

	models := r.GetAvailableModels("openai")
	if len(models) != 1 {
		t.Fatalf("expected one model, got %d", len(models))
	}
	if got := models[0]["context_window"]; got != 400000 {
		t.Fatalf("expected context_window 400000, got %#v", got)
	}
	if got := models[0]["auto_compact_token_limit"]; got != 32000 {
		t.Fatalf("expected auto_compact_token_limit 32000, got %#v", got)
	}
	if got := models[0]["effective_context_window_percent"]; got != 100 {
		t.Fatalf("expected effective_context_window_percent 100, got %#v", got)
	}
	truncation, ok := models[0]["truncation_policy"].(map[string]any)
	if !ok {
		t.Fatalf("expected truncation_policy map, got %#v", models[0]["truncation_policy"])
	}
	if got := truncation["mode"]; got != "tokens" {
		t.Fatalf("expected truncation mode tokens, got %#v", got)
	}
	if got := truncation["limit"]; got != 32000 {
		t.Fatalf("expected truncation limit 32000, got %#v", got)
	}
}

func TestLookupModelInfoReturnsCloneForStaticDefinitions(t *testing.T) {
	all := [][]*ModelInfo{
		GetClaudeModels(),
		GetGeminiModels(),
		GetGeminiVertexModels(),
		GetGeminiCLIModels(),
		GetAIStudioModels(),
		GetCodexProModels(),
		GetKimiModels(),
		GetAntigravityModels(),
	}

	modelID := ""
	for _, group := range all {
		for _, model := range group {
			if model != nil && strings.TrimSpace(model.ID) != "" {
				modelID = model.ID
				break
			}
		}
		if modelID != "" {
			break
		}
	}

	if modelID == "" {
		t.Skip("no static model definitions available")
	}

	first := LookupModelInfo(modelID)
	if first == nil {
		t.Fatalf("expected static model for %q", modelID)
	}
	first.DisplayName = "mutated"

	second := LookupModelInfo(modelID)
	if second == nil || second.DisplayName == "mutated" {
		t.Fatalf("expected static lookup clone, got %+v", second)
	}
}
