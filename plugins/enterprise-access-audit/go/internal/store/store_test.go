package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/model"
)

func testConfig(t *testing.T) config.Config {
	t.Helper()
	cfg, err := config.Normalize(config.Default(), t.TempDir())
	if err != nil {
		t.Fatalf("config.Normalize() error = %v", err)
	}
	cfg.DatabasePath = filepath.Join(cfg.DataDir, "test.sqlite")
	return cfg
}

func TestStorePersistsPolicyAcrossRestart(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	first, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	models := []string{"GPT-4", "gpt-4-mini"}
	audit := false
	if err := first.ReplacePolicies(ctx, []model.PolicyPatch{{KeyHash: "ABCDEF12", DeniedModels: &models, AuditEnabled: &audit}}); err != nil {
		t.Fatalf("ReplacePolicies() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	second, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	defer second.Close()
	policy, err := second.GetPolicy(ctx, "abcdef12")
	if err != nil {
		t.Fatalf("GetPolicy() error = %v", err)
	}
	if policy.AuditEnabled || len(policy.DeniedModels) != 2 || policy.DeniedModels[0] != "gpt-4" {
		t.Fatalf("unexpected persisted policy: %+v", policy)
	}
}

func TestReplacePoliciesAbsentRowInheritsPersistedDefaultAudit(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	defaultAudit := false
	if err := store.UpdateSettings(ctx, SettingsPatch{DefaultAuditEnabled: &defaultAudit}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	models := []string{"model-a"}
	if err := store.ReplacePolicies(ctx, []model.PolicyPatch{{KeyHash: "abcdef12", DeniedModels: &models}}); err != nil {
		t.Fatalf("ReplacePolicies() error = %v", err)
	}
	policy, err := store.GetPolicy(ctx, "abcdef12")
	if err != nil {
		t.Fatalf("GetPolicy() error = %v", err)
	}
	if policy.AuditEnabled || len(policy.DeniedModels) != 1 || policy.DeniedModels[0] != "model-a" {
		t.Fatalf("absent policy did not inherit persisted default: %+v", policy)
	}
}

func TestBatchReplacementIsAtomicAndPreservesOmittedFields(t *testing.T) {
	ctx := context.Background()
	store, err := Open(ctx, testConfig(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	models := []string{"model-a"}
	audit := false
	if err := store.ReplacePolicies(ctx, []model.PolicyPatch{{KeyHash: "abcdef12", DeniedModels: &models, AuditEnabled: &audit}}); err != nil {
		t.Fatalf("initial replacement error = %v", err)
	}
	newModels := []string{"model-b"}
	if err := store.ReplacePolicies(ctx, []model.PolicyPatch{{KeyHash: "abcdef12", DeniedModels: &newModels}, {KeyHash: "bad"}}); err == nil {
		t.Fatal("invalid batch unexpectedly committed")
	}
	policy, err := store.GetPolicy(ctx, "abcdef12")
	if err != nil {
		t.Fatalf("GetPolicy() error = %v", err)
	}
	if len(policy.DeniedModels) != 1 || policy.DeniedModels[0] != "model-a" || policy.AuditEnabled {
		t.Fatalf("atomicity or field preservation failed: %+v", policy)
	}
	if err := store.ReplacePolicies(ctx, []model.PolicyPatch{{KeyHash: "abcdef12", DeniedModels: &newModels}}); err != nil {
		t.Fatalf("partial replacement error = %v", err)
	}
	policy, err = store.GetPolicy(ctx, "abcdef12")
	if err != nil || policy.AuditEnabled || policy.DeniedModels[0] != "model-b" {
		t.Fatalf("omitted audit field was not preserved: %+v, %v", policy, err)
	}
}

func TestStoreRetiresLegacySQLiteAuditRowsWithBackup(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	legacy, err := sql.Open("sqlite", "file:"+cfg.DatabasePath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	_, err = legacy.ExecContext(ctx, `
		CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL);
		CREATE TABLE policies (key_hash TEXT PRIMARY KEY NOT NULL, denied_models TEXT NOT NULL DEFAULT '[]', audit_enabled INTEGER NOT NULL DEFAULT 1, updated_at INTEGER NOT NULL);
		CREATE TABLE settings (name TEXT PRIMARY KEY NOT NULL, value TEXT NOT NULL);
		CREATE TABLE audit_records (id INTEGER PRIMARY KEY AUTOINCREMENT, key_hash TEXT NOT NULL, created_at INTEGER NOT NULL, model TEXT NOT NULL, source_format TEXT NOT NULL, request_id TEXT NOT NULL, outcome TEXT NOT NULL, status_code INTEGER NOT NULL DEFAULT 0, text TEXT NOT NULL DEFAULT '', text_available INTEGER NOT NULL DEFAULT 1, text_unavailable_reason TEXT NOT NULL DEFAULT '', text_truncated INTEGER NOT NULL DEFAULT 0, security_signal TEXT NOT NULL DEFAULT '', security_message TEXT NOT NULL DEFAULT '');
		INSERT INTO schema_migrations(version, applied_at) VALUES (1, 1), (2, 1), (3, 1), (4, 1), (5, 1);
		INSERT INTO audit_records(key_hash, created_at, model, source_format, request_id, outcome, text) VALUES ('abcdef12', 1, 'model', 'openai', 'legacy-request', 'succeeded', 'legacy text');
	`)
	if err != nil {
		_ = legacy.Close()
		t.Fatalf("create legacy database: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy database: %v", err)
	}

	store, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	var activeAuditTables int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'audit_records'`).Scan(&activeAuditTables); err != nil {
		t.Fatalf("active audit table query: %v", err)
	}
	if activeAuditTables != 0 {
		t.Fatalf("active SQLite audit table count = %d, want 0", activeAuditTables)
	}
	backups, err := filepath.Glob(cfg.DatabasePath + ".legacy-*.sqlite")
	if err != nil || len(backups) != 1 {
		t.Fatalf("legacy backups = %v, error = %v", backups, err)
	}
	backup, err := sql.Open("sqlite", "file:"+backups[0])
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer backup.Close()
	var legacyRows int
	if err := backup.QueryRowContext(ctx, `SELECT COUNT(1) FROM audit_records`).Scan(&legacyRows); err != nil || legacyRows != 1 {
		t.Fatalf("backup legacy rows = %d, error = %v", legacyRows, err)
	}
}

func TestStoreWritesAuditTextToJSONLAndKeepsSQLiteForPolicyState(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	store, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	createdAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if err := store.InsertAudit(ctx, AuditRecord{KeyHash: "abcdef12", CreatedAt: createdAt, RequestID: "request-1", Model: "model", Text: "user text", TextAvailable: true}); err != nil {
		t.Fatalf("InsertAudit() error = %v", err)
	}
	logPath := filepath.Join(cfg.DataDir, "key-abcdef12-20260830.jsonl")
	content, err := os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(content), `"text":"user text"`) {
		t.Fatalf("JSONL content = %q, error = %v", content, err)
	}
	var auditTableCount int
	if err := store.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM sqlite_master WHERE type = 'table' AND name = 'audit_records'`).Scan(&auditTableCount); err != nil {
		t.Fatalf("audit table query error = %v", err)
	}
	if auditTableCount != 0 {
		t.Fatalf("SQLite audit table count = %d, want 0", auditTableCount)
	}
}

func TestStoreSettingsAndRetentionCleanup(t *testing.T) {
	ctx := context.Background()
	cfg := testConfig(t)
	store, err := Open(ctx, cfg)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	old := time.Now().Add(-31 * 24 * time.Hour)
	if err := store.InsertAudit(ctx, AuditRecord{KeyHash: "abcdef12", CreatedAt: old, Model: "model", Text: "old", TextAvailable: true}); err != nil {
		t.Fatalf("InsertAudit() error = %v", err)
	}
	removed, err := store.CleanupExpired(ctx, time.Now())
	if err != nil || removed != 1 {
		t.Fatalf("CleanupExpired() = %d, %v", removed, err)
	}
	retention := 7
	if err := store.UpdateSettings(ctx, SettingsPatch{RetentionDays: &retention}); err != nil {
		t.Fatalf("UpdateSettings() error = %v", err)
	}
	settings, err := store.GetSettings(ctx, SettingsFromConfig(cfg))
	if err != nil || settings.RetentionDays != 7 {
		t.Fatalf("GetSettings() = %+v, %v", settings, err)
	}
}

func TestClosedStoreReturnsBoundedError(t *testing.T) {
	store, err := Open(context.Background(), testConfig(t))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if _, err := store.GetPolicy(context.Background(), "abcdef12"); err != ErrClosed {
		t.Fatalf("GetPolicy() error = %v, want ErrClosed", err)
	}
}
