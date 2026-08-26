package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	DefaultRetentionDays    = 30
	MinRetentionDays        = 1
	MaxRetentionDays        = 3650
	DefaultMaxTextBytes     = 32 * 1024
	MinMaxTextBytes         = 1
	MaxMaxTextBytes         = 1024 * 1024
	DefaultCleanupInterval  = time.Hour
	MinCleanupInterval      = time.Second
	MaxCleanupInterval      = 24 * time.Hour
	DefaultDataDirectory    = ".cli-proxy-api/plugins/enterprise-access-audit"
	DefaultDatabaseFilename = "enterprise-access-audit.sqlite"
)

// Config is the validated runtime configuration for the plugin.
type Config struct {
	DataDir             string
	DatabasePath        string
	RetentionDays       int
	DefaultAuditEnabled bool
	MaxTextBytes        int
	CleanupInterval     time.Duration
}

// Default returns deterministic plugin defaults before path resolution.
func Default() Config {
	return Config{
		DataDir:             DefaultDataDirectory,
		RetentionDays:       DefaultRetentionDays,
		DefaultAuditEnabled: true,
		MaxTextBytes:        DefaultMaxTextBytes,
		CleanupInterval:     DefaultCleanupInterval,
	}
}

type yamlConfig struct {
	DataDir             string `yaml:"data_dir"`
	DatabasePath        string `yaml:"database_path"`
	RetentionDays       int    `yaml:"retention_days"`
	DefaultAuditEnabled *bool  `yaml:"default_audit_enabled"`
	MaxTextBytes        int    `yaml:"max_text_bytes"`
	CleanupInterval     int    `yaml:"cleanup_interval_seconds"`
}

// ParseYAML decodes host-supplied configuration and applies defaults and validation.
func ParseYAML(raw []byte, workingDir string) (Config, error) {
	defaults := Default()
	decoded := yamlConfig{}
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &decoded); errUnmarshal != nil {
			return Config{}, fmt.Errorf("decode plugin config: %w", errUnmarshal)
		}
	}
	if decoded.DataDir != "" {
		defaults.DataDir = decoded.DataDir
	}
	defaults.DatabasePath = decoded.DatabasePath
	if decoded.RetentionDays != 0 {
		defaults.RetentionDays = decoded.RetentionDays
	}
	if decoded.DefaultAuditEnabled != nil {
		defaults.DefaultAuditEnabled = *decoded.DefaultAuditEnabled
	}
	if decoded.MaxTextBytes != 0 {
		defaults.MaxTextBytes = decoded.MaxTextBytes
	}
	if decoded.CleanupInterval != 0 {
		defaults.CleanupInterval = time.Duration(decoded.CleanupInterval) * time.Second
	}
	return Normalize(defaults, workingDir)
}

// Normalize resolves paths, creates the data directory, and validates bounded settings.
func Normalize(input Config, workingDir string) (Config, error) {
	defaults := Default()
	if input.DataDir == "" {
		input.DataDir = defaults.DataDir
	}
	if workingDir == "" {
		var errWorkingDir error
		workingDir, errWorkingDir = os.Getwd()
		if errWorkingDir != nil {
			return Config{}, fmt.Errorf("resolve working directory: %w", errWorkingDir)
		}
	}
	workingDir, errAbs := filepath.Abs(workingDir)
	if errAbs != nil {
		return Config{}, fmt.Errorf("resolve working directory: %w", errAbs)
	}
	input.DataDir = resolvePath(workingDir, input.DataDir)
	if input.DatabasePath == "" {
		input.DatabasePath = filepath.Join(input.DataDir, DefaultDatabaseFilename)
	} else {
		input.DatabasePath = resolvePath(workingDir, input.DatabasePath)
	}
	if input.RetentionDays < MinRetentionDays || input.RetentionDays > MaxRetentionDays {
		return Config{}, fmt.Errorf("retention_days must be between %d and %d", MinRetentionDays, MaxRetentionDays)
	}
	if input.MaxTextBytes < MinMaxTextBytes || input.MaxTextBytes > MaxMaxTextBytes {
		return Config{}, fmt.Errorf("max_text_bytes must be between %d and %d", MinMaxTextBytes, MaxMaxTextBytes)
	}
	if input.CleanupInterval < MinCleanupInterval || input.CleanupInterval > MaxCleanupInterval {
		return Config{}, fmt.Errorf("cleanup interval must be between %s and %s", MinCleanupInterval, MaxCleanupInterval)
	}
	if errMkdir := os.MkdirAll(input.DataDir, 0o750); errMkdir != nil {
		return Config{}, fmt.Errorf("create plugin data directory %q: %w", input.DataDir, errMkdir)
	}
	if errMkdir := os.MkdirAll(filepath.Dir(input.DatabasePath), 0o750); errMkdir != nil {
		return Config{}, fmt.Errorf("create database directory %q: %w", filepath.Dir(input.DatabasePath), errMkdir)
	}
	return input, nil
}

func resolvePath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}
