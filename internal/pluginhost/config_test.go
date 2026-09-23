package pluginhost

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestRuntimeConfigYAMLAddsHostDefaultsToRawPluginConfig(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("config1: true\nconfig2: value\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	if len(node.Content) != 1 {
		t.Fatalf("yaml node content length = %d, want 1", len(node.Content))
	}
	item := config.PluginInstanceConfig{
		Priority: 3,
		Raw:      *node.Content[0],
	}

	got := string(runtimeConfigYAML(item, true))
	for _, want := range []string{
		"config1: true",
		"config2: value",
		"enabled: true",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigYAMLDefaultsEnabledFalse(t *testing.T) {
	item := config.PluginInstanceConfig{
		Priority: 3,
	}

	got := string(runtimeConfigYAML(item, false))
	for _, want := range []string{
		"enabled: false",
		"priority: 3",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("runtimeConfigYAML() missing %q in:\n%s", want, got)
		}
	}
}

func TestRuntimeConfigFromConfigExtractsStoreVersion(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("store:\n  version: 1.0.3\n  release-tag: v1.0.3\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     *node.Content[0],
				},
			},
		},
	}

	got, errRuntimeConfig := runtimeConfigFromConfig(cfg)
	if errRuntimeConfig != nil {
		t.Fatalf("runtimeConfigFromConfig() error = %v", errRuntimeConfig)
	}
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}

func TestRuntimeConfigFromConfigDerivesStoreVersionFromReleaseTag(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("store:\n  release-tag: v1.0.3\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	enabled := true
	cfg := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"alpha": {
					Enabled: &enabled,
					Raw:     *node.Content[0],
				},
			},
		},
	}

	got, errRuntimeConfig := runtimeConfigFromConfig(cfg)
	if errRuntimeConfig != nil {
		t.Fatalf("runtimeConfigFromConfig() error = %v", errRuntimeConfig)
	}
	if got.Items["alpha"].Version != "1.0.3" {
		t.Fatalf("runtimeConfigFromConfig() version = %q, want 1.0.3", got.Items["alpha"].Version)
	}
}

// Deliberate releases extract no claim: disabling the whole plugin system (or
// the instance itself) while keeping exclusive-scheduler-providers in the
// config is an explicit operator action that returns providers to default
// scheduling. Claims are only honored for deliberately enabled instances.
func TestRuntimeConfigFromConfigReleasesExclusiveClaimsWhenDisabled(t *testing.T) {
	var node yaml.Node
	if errDecode := yaml.Unmarshal([]byte("exclusive-scheduler-providers: [codex, codex]\n"), &node); errDecode != nil {
		t.Fatalf("yaml.Unmarshal() error = %v", errDecode)
	}
	disabled := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: false,
			Configs: map[string]config.PluginInstanceConfig{
				"pool": {Enabled: boolPtr(true), Raw: *node.Content[0]},
			},
		},
	}
	gotDisabled, errDisabled := runtimeConfigFromConfig(disabled)
	if errDisabled != nil {
		t.Fatalf("runtimeConfigFromConfig(disabled) error = %v", errDisabled)
	}
	if owners := gotDisabled.ExclusiveSchedulers["codex"]; len(owners) != 0 {
		t.Fatalf("exclusive owners with plugins disabled = %#v, want none (deliberate release)", owners)
	}

	instanceOff := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"pool": {Enabled: boolPtr(false), Raw: *node.Content[0]},
			},
		},
	}
	gotInstanceOff, errInstanceOff := runtimeConfigFromConfig(instanceOff)
	if errInstanceOff != nil {
		t.Fatalf("runtimeConfigFromConfig(instance off) error = %v", errInstanceOff)
	}
	if owners := gotInstanceOff.ExclusiveSchedulers["codex"]; len(owners) != 0 {
		t.Fatalf("exclusive owners with instance disabled = %#v, want none (deliberate release)", owners)
	}

	enabled := &config.Config{
		Plugins: config.PluginsConfig{
			Enabled: true,
			Configs: map[string]config.PluginInstanceConfig{
				"pool": {Enabled: boolPtr(true), Raw: *node.Content[0]},
			},
		},
	}
	gotEnabled, errEnabled := runtimeConfigFromConfig(enabled)
	if errEnabled != nil {
		t.Fatalf("runtimeConfigFromConfig(enabled) error = %v", errEnabled)
	}
	owners := gotEnabled.ExclusiveSchedulers["codex"]
	if len(owners) != 1 || owners[0] != "pool" {
		t.Fatalf("exclusive scheduler owners = %#v, want [pool] (deduped)", owners)
	}
}

func boolPtr(value bool) *bool { return &value }
