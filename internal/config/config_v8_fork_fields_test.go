package config

import (
	"strings"
	"testing"
)

// The v8 migration comments out configuration sections it does not recognise.
// Fork-only fields therefore have to be registered in the v8 layout, otherwise
// an operator's quota limits, Codex gateway, or plugin settings disappear the
// first time the config is saved.
func TestV8MigrationKeepsForkOnlyConfiguration(t *testing.T) {
	const gateway = "https://relay.example.invalid/codex"
	raw := []byte(`host: 127.0.0.1
port: 8317
api-keys: [client]
codex:
  base-url: ` + gateway + `
  disable-codex-cloaking: false
quota:
  enabled: true
  db-path: "~/.cli-proxy-api/quota.db"
  mode: cost
  default:
    daily-cents: 15000
    weekly-cents: 50000
plugins:
  enabled: true
  dir: plugins
  configs:
    enterprise-access-audit:
      enabled: true
      priority: 10
`)

	migrated, changed, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatalf("migrate legacy config: %v", err)
	}
	if !changed {
		t.Fatal("legacy layout was not migrated")
	}
	if err = ValidateV8Config(migrated); err != nil {
		t.Fatalf("migrated config is invalid: %v", err)
	}

	cfg, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatalf("parse migrated config: %v", err)
	}
	if got := strings.TrimSpace(cfg.Codex.BaseURL); got != gateway {
		t.Errorf("codex.base-url = %q, want %q (fork gateway must survive migration)", got, gateway)
	}
	if !cfg.Quota.Enabled {
		t.Error("quota.enabled lost during migration")
	}
	if cfg.Quota.Default.DailyCents != 15000 || cfg.Quota.Default.WeeklyCents != 50000 {
		t.Errorf("quota limits = %d/%d, want 15000/50000", cfg.Quota.Default.DailyCents, cfg.Quota.Default.WeeklyCents)
	}
	if got := strings.TrimSpace(cfg.Quota.DBPath); got != "~/.cli-proxy-api/quota.db" {
		t.Errorf("quota.db-path = %q", got)
	}
	instance, ok := cfg.Plugins.Configs["enterprise-access-audit"]
	if !ok {
		t.Fatal("plugins.configs.enterprise-access-audit lost during migration")
	}
	if instance.Enabled == nil || !*instance.Enabled {
		t.Error("enterprise-access-audit enabled flag lost during migration")
	}
	if instance.Priority != 10 {
		t.Errorf("enterprise-access-audit priority = %d, want 10", instance.Priority)
	}

	// A second pass over the migrated output must not lose the fork fields.
	// (The layout pass may still report changes because it adds scope metadata.)
	again, _, errAgain := NormalizeConfigLayout(migrated, true)
	if errAgain != nil {
		t.Fatalf("re-migrate: %v", errAgain)
	}
	cfgAgain, errAgainParse := ParseConfigBytes(again)
	if errAgainParse != nil {
		t.Fatalf("parse re-migrated config: %v", errAgainParse)
	}
	if got := strings.TrimSpace(cfgAgain.Codex.BaseURL); got != gateway {
		t.Errorf("re-migrated codex.base-url = %q, want %q", got, gateway)
	}
	if !cfgAgain.Quota.Enabled || cfgAgain.Quota.Default.DailyCents != 15000 {
		t.Error("quota limits lost after a second migration pass")
	}
	if _, ok := cfgAgain.Plugins.Configs["enterprise-access-audit"]; !ok {
		t.Error("enterprise-access-audit lost after a second migration pass")
	}
}
