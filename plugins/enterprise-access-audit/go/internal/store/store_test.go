package store

import (
	"context"
	"path/filepath"
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
