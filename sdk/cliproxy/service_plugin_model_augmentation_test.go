package cliproxy

import "testing"

func TestAppendUniqueModelInfosPreservesNativeModelsAndAddsPluginModels(t *testing.T) {
	native := []*ModelInfo{{ID: "gpt-5.4"}, {ID: "gpt-5.6-sol"}}
	additional := []*ModelInfo{{ID: "gpt-5.6-sol", OwnedBy: "oai-basispoints"}, {ID: "gpt-6-astra", OwnedBy: "oai-basispoints"}}

	got := appendUniqueModelInfos(native, additional)
	if len(got) != 3 {
		t.Fatalf("merged model count = %d, want 3: %#v", len(got), got)
	}
	if got[0].ID != "gpt-5.4" || got[1].ID != "gpt-5.6-sol" || got[2].ID != "gpt-6-astra" {
		t.Fatalf("merged models = %#v, want native models followed by new plugin model", got)
	}
	if got[1] != native[1] {
		t.Fatal("duplicate plugin model replaced the native model metadata")
	}
}
