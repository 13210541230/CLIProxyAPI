package pluginhost

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigCodexClaimDoesNotAddBasisPointsScheduler(t *testing.T) {
	var node yaml.Node
	if err := yaml.Unmarshal([]byte("exclusive-scheduler-providers: [codex]\n"), &node); err != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", err)
	}
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"enterprise-access-audit": {Enabled: boolPtr(true), Raw: *node.Content[0]},
			},
		},
	}
	got, err := runtimeConfigFromConfig(cfg)
	if err != nil {
		t.Fatalf("runtimeConfigFromConfig() error = %v", err)
	}
	if owners := got.ExclusiveSchedulers["codex"]; len(owners) != 1 || owners[0] != "enterprise-access-audit" {
		t.Fatalf("Codex exclusive owners = %#v, want enterprise-access-audit", owners)
	}
	if owners := got.ExclusiveSchedulers["oai-basispoints"]; len(owners) != 0 {
		t.Fatalf("BPS executor unexpectedly claimed an account scheduler: %#v", owners)
	}
}
