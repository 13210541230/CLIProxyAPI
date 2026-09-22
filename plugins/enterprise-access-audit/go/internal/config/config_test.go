package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseYAMLUsesDeterministicDefaults(t *testing.T) {
	cfg, err := ParseYAML(nil, t.TempDir())
	if err != nil {
		t.Fatalf("ParseYAML() error = %v", err)
	}
	if cfg.RetentionDays != DefaultRetentionDays || cfg.MaxTextBytes != DefaultMaxTextBytes || cfg.AuditEnabled || cfg.DefaultAuditEnabled {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if filepath.Base(cfg.DatabasePath) != DefaultDatabaseFilename {
		t.Fatalf("database path = %q", cfg.DatabasePath)
	}
	if filepath.Base(cfg.AccountPool.DataDir) != "account-pool" {
		t.Fatalf("account pool data directory = %q", cfg.AccountPool.DataDir)
	}
	if _, errStat := os.Stat(cfg.AccountPool.DataDir); errStat != nil {
		t.Fatalf("account pool data directory was not created: %v", errStat)
	}
}

func TestParseYAMLDefaultsAccountPoolDataDirWhenEnabled(t *testing.T) {
	workingDir := t.TempDir()
	cfg, err := ParseYAML([]byte("data_dir: plugin-state\naccount_pool:\n  enabled: true\n"), workingDir)
	if err != nil {
		t.Fatalf("ParseYAML() error = %v", err)
	}
	want := filepath.Join(workingDir, "plugin-state", "account-pool")
	if cfg.AccountPool.DataDir != want {
		t.Fatalf("account pool data directory = %q, want %q", cfg.AccountPool.DataDir, want)
	}
	if !cfg.AccountPool.Enabled {
		t.Fatal("account pool should be enabled")
	}
	if _, errStat := os.Stat(want); errStat != nil {
		t.Fatalf("account pool data directory was not created: %v", errStat)
	}
}

func TestParseYAMLAcceptsFlatAccountPoolKeys(t *testing.T) {
	workingDir := t.TempDir()
	raw := "data_dir: plugin-state\naccount_pool.enabled: true\naccount_pool.data_dir: custom-pool\naccount_pool.reserve_seconds: 20\n"
	cfg, err := ParseYAML([]byte(raw), workingDir)
	if err != nil {
		t.Fatalf("ParseYAML() error = %v", err)
	}
	if !cfg.AccountPool.Enabled {
		t.Fatal("flat account_pool.enabled was not applied")
	}
	want := filepath.Join(workingDir, "custom-pool")
	if cfg.AccountPool.DataDir != want {
		t.Fatalf("account pool data directory = %q, want %q", cfg.AccountPool.DataDir, want)
	}
	if cfg.AccountPool.ReserveSeconds != 20 {
		t.Fatalf("reserve seconds = %d, want 20", cfg.AccountPool.ReserveSeconds)
	}
	if _, errStat := os.Stat(want); errStat != nil {
		t.Fatalf("account pool data directory was not created: %v", errStat)
	}
}

func TestParseYAMLNestedAccountPoolWinsOverFlatKeys(t *testing.T) {
	raw := "account_pool:\n  enabled: true\n  reserve_seconds: 7\naccount_pool.enabled: false\naccount_pool.reserve_seconds: 99\n"
	cfg, err := ParseYAML([]byte(raw), t.TempDir())
	if err != nil {
		t.Fatalf("ParseYAML() error = %v", err)
	}
	if !cfg.AccountPool.Enabled || cfg.AccountPool.ReserveSeconds != 7 {
		t.Fatalf("nested block should win: %+v", cfg.AccountPool)
	}
}

func TestNormalizeRejectsUnsafeBounds(t *testing.T) {
	base := Default()
	for _, test := range []struct {
		name string
		edit func(*Config)
	}{
		{"retention low", func(cfg *Config) { cfg.RetentionDays = MinRetentionDays - 1 }},
		{"retention high", func(cfg *Config) { cfg.RetentionDays = MaxRetentionDays + 1 }},
		{"text low", func(cfg *Config) { cfg.MaxTextBytes = MinMaxTextBytes - 1 }},
		{"text high", func(cfg *Config) { cfg.MaxTextBytes = MaxMaxTextBytes + 1 }},
		{"cleanup low", func(cfg *Config) { cfg.CleanupInterval = 500 * time.Millisecond }},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := base
			test.edit(&cfg)
			if _, err := Normalize(cfg, t.TempDir()); err == nil {
				t.Fatal("Normalize() accepted unsafe setting")
			}
		})
	}
}
