package config

import (
	"path/filepath"
	"testing"
	"time"
)

func TestParseYAMLUsesDeterministicDefaults(t *testing.T) {
	cfg, err := ParseYAML(nil, t.TempDir())
	if err != nil {
		t.Fatalf("ParseYAML() error = %v", err)
	}
	if cfg.RetentionDays != DefaultRetentionDays || cfg.MaxTextBytes != DefaultMaxTextBytes || !cfg.DefaultAuditEnabled {
		t.Fatalf("unexpected defaults: %+v", cfg)
	}
	if filepath.Base(cfg.DatabasePath) != DefaultDatabaseFilename {
		t.Fatalf("database path = %q", cfg.DatabasePath)
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
