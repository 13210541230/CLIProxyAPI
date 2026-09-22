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
	// Account-pool defaults.
	DefaultAccountPoolReserveSeconds = 10
	DefaultAccountPoolWindowSeconds  = 15
	DefaultAccountPoolMaxWaitSeconds = 30
	// DefaultAccountPoolMaxBusyRejections is the number of consecutive full
	// queue timeouts tolerated before a session fails over to another in-pool
	// account. Successful admissions reset the counter.
	DefaultAccountPoolMaxBusyRejections = 3
)

// AccountPoolConfig is the validated account-pool sub-configuration.
type AccountPoolConfig struct {
	Enabled           bool
	DataDir           string
	ReserveSeconds    int
	WindowSeconds     int
	MaxWaitSeconds    int
	MaxBusyRejections int
}

// Config is the validated runtime configuration for the plugin.
type Config struct {
	DataDir             string
	DatabasePath        string
	RetentionDays       int
	AuditEnabled        bool
	DefaultAuditEnabled bool
	MaxTextBytes        int
	CleanupInterval     time.Duration
	AccountPool         AccountPoolConfig
}

// Default returns deterministic plugin defaults before path resolution.
func Default() Config {
	return Config{
		DataDir:             DefaultDataDirectory,
		RetentionDays:       DefaultRetentionDays,
		DefaultAuditEnabled: false,
		MaxTextBytes:        DefaultMaxTextBytes,
		CleanupInterval:     DefaultCleanupInterval,
	}
}

type yamlConfig struct {
	DataDir             string           `yaml:"data_dir"`
	DatabasePath        string           `yaml:"database_path"`
	RetentionDays       int              `yaml:"retention_days"`
	AuditEnabled        *bool            `yaml:"audit_enabled"`
	DefaultAuditEnabled *bool            `yaml:"default_audit_enabled"`
	MaxTextBytes        int              `yaml:"max_text_bytes"`
	CleanupInterval     int              `yaml:"cleanup_interval_seconds"`
	AccountPool         *accountPoolYAML `yaml:"account_pool"`
	// Flat dotted keys. The Management Center plugin config editor writes
	// ConfigField names literally, so account_pool.enabled arrives as a single
	// YAML key rather than a nested block. Accept both shapes; an explicit
	// nested account_pool block always wins.
	AccountPoolEnabledFlat *bool  `yaml:"account_pool.enabled"`
	AccountPoolDataDirFlat string `yaml:"account_pool.data_dir"`
	AccountPoolReserveFlat int    `yaml:"account_pool.reserve_seconds"`
	AccountPoolWindowFlat  int    `yaml:"account_pool.window_seconds"`
	AccountPoolMaxWaitFlat int    `yaml:"account_pool.max_wait_seconds"`
	AccountPoolMaxBusyFlat int    `yaml:"account_pool.max_busy_rejections"`
}

type accountPoolYAML struct {
	Enabled           bool   `yaml:"enabled"`
	DataDir           string `yaml:"data_dir"`
	ReserveSeconds    int    `yaml:"reserve_seconds"`
	WindowSeconds     int    `yaml:"window_seconds"`
	MaxWaitSeconds    int    `yaml:"max_wait_seconds"`
	MaxBusyRejections int    `yaml:"max_busy_rejections"`
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
	if decoded.AuditEnabled != nil {
		defaults.AuditEnabled = *decoded.AuditEnabled
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
	defaults.AccountPool = accountPoolConfigFromYAML(effectiveAccountPool(decoded), defaults.DataDir)
	return Normalize(defaults, workingDir)
}

// effectiveAccountPool merges the flat dotted account_pool.* keys into the
// nested block shape. Nested values win field-by-field when both are present.
func effectiveAccountPool(decoded yamlConfig) *accountPoolYAML {
	hasFlat := decoded.AccountPoolEnabledFlat != nil ||
		decoded.AccountPoolDataDirFlat != "" ||
		decoded.AccountPoolReserveFlat != 0 ||
		decoded.AccountPoolWindowFlat != 0 ||
		decoded.AccountPoolMaxWaitFlat != 0 ||
		decoded.AccountPoolMaxBusyFlat != 0
	if decoded.AccountPool == nil && !hasFlat {
		return nil
	}
	flat := accountPoolYAML{}
	if decoded.AccountPoolEnabledFlat != nil {
		flat.Enabled = *decoded.AccountPoolEnabledFlat
	}
	flat.DataDir = decoded.AccountPoolDataDirFlat
	flat.ReserveSeconds = decoded.AccountPoolReserveFlat
	flat.WindowSeconds = decoded.AccountPoolWindowFlat
	flat.MaxWaitSeconds = decoded.AccountPoolMaxWaitFlat
	flat.MaxBusyRejections = decoded.AccountPoolMaxBusyFlat
	if decoded.AccountPool == nil {
		return &flat
	}
	merged := *decoded.AccountPool
	if !merged.Enabled && flat.Enabled {
		merged.Enabled = true
	}
	if merged.DataDir == "" {
		merged.DataDir = flat.DataDir
	}
	if merged.ReserveSeconds == 0 {
		merged.ReserveSeconds = flat.ReserveSeconds
	}
	if merged.WindowSeconds == 0 {
		merged.WindowSeconds = flat.WindowSeconds
	}
	if merged.MaxWaitSeconds == 0 {
		merged.MaxWaitSeconds = flat.MaxWaitSeconds
	}
	if merged.MaxBusyRejections == 0 {
		merged.MaxBusyRejections = flat.MaxBusyRejections
	}
	return &merged
}

func accountPoolConfigFromYAML(decoded *accountPoolYAML, pluginDataDir string) AccountPoolConfig {
	cfg := AccountPoolConfig{
		DataDir:           filepath.Join(pluginDataDir, "account-pool"),
		ReserveSeconds:    DefaultAccountPoolReserveSeconds,
		WindowSeconds:     DefaultAccountPoolWindowSeconds,
		MaxWaitSeconds:    DefaultAccountPoolMaxWaitSeconds,
		MaxBusyRejections: DefaultAccountPoolMaxBusyRejections,
	}
	if decoded == nil {
		return cfg
	}
	cfg.Enabled = decoded.Enabled
	if decoded.DataDir != "" {
		cfg.DataDir = decoded.DataDir
	}
	if decoded.ReserveSeconds > 0 {
		cfg.ReserveSeconds = decoded.ReserveSeconds
	}
	if decoded.WindowSeconds > 0 {
		cfg.WindowSeconds = decoded.WindowSeconds
	}
	if decoded.MaxWaitSeconds > 0 {
		cfg.MaxWaitSeconds = decoded.MaxWaitSeconds
	}
	if decoded.MaxBusyRejections > 0 {
		cfg.MaxBusyRejections = decoded.MaxBusyRejections
	}
	if cfg.DataDir == "" {
		cfg.DataDir = filepath.Join(pluginDataDir, "account-pool")
	}
	return cfg
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
	if input.AccountPool.DataDir == "" {
		input.AccountPool.DataDir = filepath.Join(input.DataDir, "account-pool")
	}
	input.AccountPool.DataDir = resolvePath(workingDir, input.AccountPool.DataDir)
	if errMkdir := os.MkdirAll(input.AccountPool.DataDir, 0o750); errMkdir != nil {
		return Config{}, fmt.Errorf("create account pool data directory %q: %w", input.AccountPool.DataDir, errMkdir)
	}
	if input.AccountPool.ReserveSeconds <= 0 {
		input.AccountPool.ReserveSeconds = DefaultAccountPoolReserveSeconds
	}
	if input.AccountPool.WindowSeconds <= 0 {
		input.AccountPool.WindowSeconds = DefaultAccountPoolWindowSeconds
	}
	if input.AccountPool.MaxWaitSeconds <= 0 {
		input.AccountPool.MaxWaitSeconds = DefaultAccountPoolMaxWaitSeconds
	}
	if input.AccountPool.MaxBusyRejections <= 0 {
		input.AccountPool.MaxBusyRejections = DefaultAccountPoolMaxBusyRejections
	}
	if input.AccountPool.ReserveSeconds > 300 || input.AccountPool.MaxWaitSeconds < 1 || input.AccountPool.MaxWaitSeconds > 300 {
		return Config{}, fmt.Errorf("account pool reserve/max_wait seconds are out of bounds")
	}
	if input.AccountPool.MaxBusyRejections > 100 {
		return Config{}, fmt.Errorf("account pool max_busy_rejections must be between 1 and 100")
	}
	return input, nil
}

func resolvePath(base, value string) string {
	if filepath.IsAbs(value) {
		return filepath.Clean(value)
	}
	return filepath.Clean(filepath.Join(base, value))
}
