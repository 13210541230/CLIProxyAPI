package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/model"
	_ "modernc.org/sqlite"
)

var ErrClosed = errors.New("enterprise access audit store is closed")

// Settings contains persisted retention and text-size controls.
type Settings struct {
	RetentionDays       int
	DefaultAuditEnabled bool
	MaxTextBytes        int
}

// SettingsPatch updates only non-nil settings fields.
type SettingsPatch struct {
	RetentionDays       *int
	DefaultAuditEnabled *bool
	MaxTextBytes        *int
}

// AuditRecord is the bounded text-only record schema reserved for T2/T3.
type AuditRecord struct {
	ID                    int64
	KeyHash               string
	CreatedAt             time.Time
	Model                 string
	SourceFormat          string
	RequestID             string
	Outcome               string
	StatusCode            int
	Text                  string
	TextAvailable         bool
	TextUnavailableReason string
	TextTruncated         bool
	SecuritySignal        string
	SecurityMessage       string
}

// AuditFilter contains normalized bounded filters for management queries.
type AuditFilter struct {
	From           *time.Time
	To             *time.Time
	KeyHash        string
	Model          string
	SourceFormat   string
	Outcome        string
	SecuritySignal string
}

// AuditPage is a deterministic bounded page of audit records.
type AuditPage struct {
	Records  []AuditRecord
	Page     int
	PageSize int
	Total    int
	HasNext  bool
}

// Store owns one SQLite handle and serializes operations on that handle.
type Store struct {
	mu     sync.Mutex
	db     *sql.DB
	closed bool
}

// Open creates or migrates the plugin database deterministically.
func Open(ctx context.Context, cfg config.Config) (*Store, error) {
	db, errOpen := sql.Open("sqlite", "file:"+cfg.DatabasePath+"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)")
	if errOpen != nil {
		return nil, fmt.Errorf("open sqlite database: %w", errOpen)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	store := &Store{db: db}
	if errMigrate := store.migrate(ctx); errMigrate != nil {
		_ = db.Close()
		return nil, errMigrate
	}
	if errSettings := store.initializeSettings(ctx, SettingsFromConfig(cfg)); errSettings != nil {
		_ = db.Close()
		return nil, errSettings
	}
	return store, nil
}

// SettingsFromConfig converts validated runtime config to persisted defaults.
func SettingsFromConfig(cfg config.Config) Settings {
	return Settings{RetentionDays: cfg.RetentionDays, DefaultAuditEnabled: cfg.DefaultAuditEnabled, MaxTextBytes: cfg.MaxTextBytes}
}

func (s *Store) migrate(ctx context.Context) error {
	if _, errExec := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (version INTEGER PRIMARY KEY, applied_at INTEGER NOT NULL)`); errExec != nil {
		return fmt.Errorf("create schema migrations table: %w", errExec)
	}
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS policies (
			key_hash TEXT PRIMARY KEY NOT NULL,
			denied_models TEXT NOT NULL DEFAULT '[]',
			audit_enabled INTEGER NOT NULL DEFAULT 1 CHECK (audit_enabled IN (0, 1)),
			updated_at INTEGER NOT NULL
		);
		CREATE TABLE IF NOT EXISTS settings (
			name TEXT PRIMARY KEY NOT NULL,
			value TEXT NOT NULL
		);
		CREATE TABLE IF NOT EXISTS audit_records (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			key_hash TEXT NOT NULL,
			created_at INTEGER NOT NULL,
			model TEXT NOT NULL,
			source_format TEXT NOT NULL,
			request_id TEXT NOT NULL,
			outcome TEXT NOT NULL,
			status_code INTEGER NOT NULL DEFAULT 0,
			text TEXT NOT NULL DEFAULT '',
			text_available INTEGER NOT NULL DEFAULT 1 CHECK (text_available IN (0, 1)),
			text_unavailable_reason TEXT NOT NULL DEFAULT ''
		);`,
		`CREATE INDEX IF NOT EXISTS idx_audit_records_created_at ON audit_records(created_at DESC, id DESC);
		 CREATE INDEX IF NOT EXISTS idx_audit_records_key_hash ON audit_records(key_hash);
		 CREATE INDEX IF NOT EXISTS idx_audit_records_model ON audit_records(model);`,
		`ALTER TABLE audit_records ADD COLUMN text_truncated INTEGER NOT NULL DEFAULT 0 CHECK (text_truncated IN (0, 1));`,
		`ALTER TABLE audit_records ADD COLUMN security_signal TEXT NOT NULL DEFAULT '';
			 CREATE INDEX IF NOT EXISTS idx_audit_records_security_signal ON audit_records(security_signal);`,
		`ALTER TABLE audit_records ADD COLUMN security_message TEXT NOT NULL DEFAULT '';`,
	}
	for version, migration := range migrations {
		var applied int
		if errScan := s.db.QueryRowContext(ctx, `SELECT COUNT(1) FROM schema_migrations WHERE version = ?`, version+1).Scan(&applied); errScan != nil {
			return fmt.Errorf("check schema migration %d: %w", version+1, errScan)
		}
		if applied != 0 {
			continue
		}
		tx, errBegin := s.db.BeginTx(ctx, nil)
		if errBegin != nil {
			return fmt.Errorf("begin schema migration %d: %w", version+1, errBegin)
		}
		if _, errExec := tx.ExecContext(ctx, migration); errExec != nil {
			_ = tx.Rollback()
			return fmt.Errorf("apply schema migration %d: %w", version+1, errExec)
		}
		if _, errExec := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version+1, time.Now().Unix()); errExec != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record schema migration %d: %w", version+1, errExec)
		}
		if errCommit := tx.Commit(); errCommit != nil {
			return fmt.Errorf("commit schema migration %d: %w", version+1, errCommit)
		}
	}
	return nil
}

func (s *Store) initializeSettings(ctx context.Context, defaults Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	_, errExec := s.db.ExecContext(ctx, `INSERT OR IGNORE INTO settings(name, value) VALUES
		('retention_days', ?), ('default_audit_enabled', ?), ('max_text_bytes', ?)`, defaults.RetentionDays, boolInt(defaults.DefaultAuditEnabled), defaults.MaxTextBytes)
	if errExec != nil {
		return fmt.Errorf("initialize plugin settings: %w", errExec)
	}
	return nil
}

// Close releases the SQLite handle and makes subsequent operations fail safely.
func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	if s.db == nil {
		return nil
	}
	if errClose := s.db.Close(); errClose != nil {
		return fmt.Errorf("close sqlite database: %w", errClose)
	}
	return nil
}

// GetPolicy returns a stored policy or the T1 default policy for an absent key.
func (s *Store) GetPolicy(ctx context.Context, keyHash string) (model.Policy, error) {
	return s.GetPolicyWithDefault(ctx, keyHash, true)
}

// GetPolicyWithDefault applies the persisted default-audit setting only when the key has no row.
func (s *Store) GetPolicyWithDefault(ctx context.Context, keyHash string, defaultAuditEnabled bool) (model.Policy, error) {
	keyHash, errHash := model.NormalizeKeyHash(keyHash)
	if errHash != nil {
		return model.Policy{}, errHash
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return model.Policy{}, ErrClosed
	}
	var rawModels string
	var auditEnabled int
	var updatedAt int64
	errQuery := s.db.QueryRowContext(ctx, `SELECT denied_models, audit_enabled, updated_at FROM policies WHERE key_hash = ?`, keyHash).Scan(&rawModels, &auditEnabled, &updatedAt)
	if errors.Is(errQuery, sql.ErrNoRows) {
		policy := model.DefaultPolicy(keyHash)
		policy.AuditEnabled = defaultAuditEnabled
		return policy, nil
	}
	if errQuery != nil {
		return model.Policy{}, fmt.Errorf("read policy for %s: %w", keyHash, errQuery)
	}
	var denied []string
	if errUnmarshal := json.Unmarshal([]byte(rawModels), &denied); errUnmarshal != nil {
		return model.Policy{}, fmt.Errorf("decode policy for %s: %w", keyHash, errUnmarshal)
	}
	denied, errNormalize := model.NormalizeModelIDs(denied)
	if errNormalize != nil {
		return model.Policy{}, fmt.Errorf("normalize policy for %s: %w", keyHash, errNormalize)
	}
	return model.Policy{KeyHash: keyHash, DeniedModels: denied, AuditEnabled: auditEnabled != 0, UpdatedAt: updatedAt}, nil
}

// ReplacePolicies applies all patches in one transaction and preserves omitted fields.
func (s *Store) ReplacePolicies(ctx context.Context, patches []model.PolicyPatch) error {
	if len(patches) == 0 {
		return nil
	}
	normalized := make([]model.PolicyPatch, len(patches))
	seen := make(map[string]struct{}, len(patches))
	for index, patch := range patches {
		keyHash, errHash := model.NormalizeKeyHash(patch.KeyHash)
		if errHash != nil {
			return fmt.Errorf("validate policy patch %d: %w", index, errHash)
		}
		if _, exists := seen[keyHash]; exists {
			return fmt.Errorf("duplicate policy key hash %q", keyHash)
		}
		seen[keyHash] = struct{}{}
		normalized[index] = patch
		normalized[index].KeyHash = keyHash
		if patch.DeniedModels != nil {
			models, errModels := model.NormalizeModelIDs(*patch.DeniedModels)
			if errModels != nil {
				return fmt.Errorf("validate denied models for %s: %w", keyHash, errModels)
			}
			normalized[index].DeniedModels = &models
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("begin policy replacement: %w", errBegin)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	defaultAuditEnabled, errDefault := getDefaultAuditEnabledTx(ctx, tx)
	if errDefault != nil {
		return rollback(errDefault)
	}
	for _, patch := range normalized {
		current, errCurrent := getPolicyTx(ctx, tx, patch.KeyHash, defaultAuditEnabled)
		if errCurrent != nil {
			return rollback(errCurrent)
		}
		if patch.DeniedModels != nil {
			current.DeniedModels = append([]string(nil), (*patch.DeniedModels)...)
		}
		if patch.AuditEnabled != nil {
			current.AuditEnabled = *patch.AuditEnabled
		}
		rawModels, errMarshal := json.Marshal(current.DeniedModels)
		if errMarshal != nil {
			return rollback(fmt.Errorf("encode policy for %s: %w", patch.KeyHash, errMarshal))
		}
		if _, errExec := tx.ExecContext(ctx, `INSERT INTO policies(key_hash, denied_models, audit_enabled, updated_at) VALUES (?, ?, ?, ?)
			ON CONFLICT(key_hash) DO UPDATE SET denied_models=excluded.denied_models, audit_enabled=excluded.audit_enabled, updated_at=excluded.updated_at`, patch.KeyHash, string(rawModels), boolInt(current.AuditEnabled), time.Now().Unix()); errExec != nil {
			return rollback(fmt.Errorf("write policy for %s: %w", patch.KeyHash, errExec))
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("commit policy replacement: %w", errCommit)
	}
	return nil
}

func getDefaultAuditEnabledTx(ctx context.Context, tx *sql.Tx) (bool, error) {
	var value string
	errQuery := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE name = 'default_audit_enabled'`).Scan(&value)
	if errors.Is(errQuery, sql.ErrNoRows) {
		return true, nil
	}
	if errQuery != nil {
		return false, fmt.Errorf("read default audit setting in policy transaction: %w", errQuery)
	}
	return value == "1", nil
}

func getPolicyTx(ctx context.Context, tx *sql.Tx, keyHash string, defaultAuditEnabled bool) (model.Policy, error) {
	var rawModels string
	var auditEnabled int
	var updatedAt int64
	errQuery := tx.QueryRowContext(ctx, `SELECT denied_models, audit_enabled, updated_at FROM policies WHERE key_hash = ?`, keyHash).Scan(&rawModels, &auditEnabled, &updatedAt)
	if errors.Is(errQuery, sql.ErrNoRows) {
		policy := model.DefaultPolicy(keyHash)
		policy.AuditEnabled = defaultAuditEnabled
		return policy, nil
	}
	if errQuery != nil {
		return model.Policy{}, fmt.Errorf("read policy for %s in transaction: %w", keyHash, errQuery)
	}
	var denied []string
	if errUnmarshal := json.Unmarshal([]byte(rawModels), &denied); errUnmarshal != nil {
		return model.Policy{}, fmt.Errorf("decode policy for %s in transaction: %w", keyHash, errUnmarshal)
	}
	denied, errNormalize := model.NormalizeModelIDs(denied)
	if errNormalize != nil {
		return model.Policy{}, fmt.Errorf("normalize policy for %s in transaction: %w", keyHash, errNormalize)
	}
	return model.Policy{KeyHash: keyHash, DeniedModels: denied, AuditEnabled: auditEnabled != 0, UpdatedAt: updatedAt}, nil
}

// ListPolicies returns deterministic policies for the requested hashes.
func (s *Store) ListPolicies(ctx context.Context, hashes []string) ([]model.Policy, error) {
	return s.ListPoliciesWithDefault(ctx, hashes, true)
}

// ListPoliciesWithDefault applies the supplied audit default to absent rows.
func (s *Store) ListPoliciesWithDefault(ctx context.Context, hashes []string, defaultAuditEnabled bool) ([]model.Policy, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return nil, ErrClosed
	}
	if len(hashes) == 0 {
		rows, errQuery := s.db.QueryContext(ctx, `SELECT key_hash, denied_models, audit_enabled, updated_at FROM policies ORDER BY key_hash`)
		if errQuery != nil {
			return nil, fmt.Errorf("list policies: %w", errQuery)
		}
		defer rows.Close()
		return scanPolicies(rows)
	}
	result := make([]model.Policy, 0, len(hashes))
	for _, rawHash := range hashes {
		keyHash, errHash := model.NormalizeKeyHash(rawHash)
		if errHash != nil {
			return nil, errHash
		}
		var rawModels string
		var auditEnabled int
		var updatedAt int64
		errQuery := s.db.QueryRowContext(ctx, `SELECT denied_models, audit_enabled, updated_at FROM policies WHERE key_hash = ?`, keyHash).Scan(&rawModels, &auditEnabled, &updatedAt)
		if errors.Is(errQuery, sql.ErrNoRows) {
			policy := model.DefaultPolicy(keyHash)
			policy.AuditEnabled = defaultAuditEnabled
			result = append(result, policy)
			continue
		}
		if errQuery != nil {
			return nil, fmt.Errorf("read policy for %s: %w", keyHash, errQuery)
		}
		var denied []string
		if errUnmarshal := json.Unmarshal([]byte(rawModels), &denied); errUnmarshal != nil {
			return nil, fmt.Errorf("decode policy for %s: %w", keyHash, errUnmarshal)
		}
		denied, errNormalize := model.NormalizeModelIDs(denied)
		if errNormalize != nil {
			return nil, errNormalize
		}
		result = append(result, model.Policy{KeyHash: keyHash, DeniedModels: denied, AuditEnabled: auditEnabled != 0, UpdatedAt: updatedAt})
	}
	return result, nil
}

func scanPolicies(rows *sql.Rows) ([]model.Policy, error) {
	result := make([]model.Policy, 0)
	for rows.Next() {
		var policy model.Policy
		var rawModels string
		var auditEnabled int
		if errScan := rows.Scan(&policy.KeyHash, &rawModels, &auditEnabled, &policy.UpdatedAt); errScan != nil {
			return nil, fmt.Errorf("scan policy: %w", errScan)
		}
		if errUnmarshal := json.Unmarshal([]byte(rawModels), &policy.DeniedModels); errUnmarshal != nil {
			return nil, fmt.Errorf("decode policy: %w", errUnmarshal)
		}
		var errNormalize error
		policy.DeniedModels, errNormalize = model.NormalizeModelIDs(policy.DeniedModels)
		if errNormalize != nil {
			return nil, errNormalize
		}
		policy.AuditEnabled = auditEnabled != 0
		result = append(result, policy)
	}
	if errRows := rows.Err(); errRows != nil {
		return nil, fmt.Errorf("iterate policies: %w", errRows)
	}
	return result, nil
}

// GetSettings reads persisted settings, falling back to the supplied defaults when absent.
func (s *Store) GetSettings(ctx context.Context, fallback Settings) (Settings, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return Settings{}, ErrClosed
	}
	return s.getSettingsLocked(ctx, fallback)
}

func (s *Store) getSettingsLocked(ctx context.Context, fallback Settings) (Settings, error) {
	result := fallback
	rows, errQuery := s.db.QueryContext(ctx, `SELECT name, value FROM settings`)
	if errQuery != nil {
		return Settings{}, fmt.Errorf("read settings: %w", errQuery)
	}
	defer rows.Close()
	for rows.Next() {
		var name, value string
		if errScan := rows.Scan(&name, &value); errScan != nil {
			return Settings{}, fmt.Errorf("scan setting: %w", errScan)
		}
		switch name {
		case "retention_days":
			if _, errScan := fmt.Sscanf(value, "%d", &result.RetentionDays); errScan != nil {
				return Settings{}, fmt.Errorf("parse retention_days: %w", errScan)
			}
		case "default_audit_enabled":
			result.DefaultAuditEnabled = value == "1"
		case "max_text_bytes":
			if _, errScan := fmt.Sscanf(value, "%d", &result.MaxTextBytes); errScan != nil {
				return Settings{}, fmt.Errorf("parse max_text_bytes: %w", errScan)
			}
		}
	}
	if errRows := rows.Err(); errRows != nil {
		return Settings{}, fmt.Errorf("iterate settings: %w", errRows)
	}
	return result, nil
}

// UpdateSettings atomically updates settings and validates every provided value first.
func (s *Store) UpdateSettings(ctx context.Context, patch SettingsPatch) error {
	if patch.RetentionDays != nil && (*patch.RetentionDays < config.MinRetentionDays || *patch.RetentionDays > config.MaxRetentionDays) {
		return fmt.Errorf("retention_days must be between %d and %d", config.MinRetentionDays, config.MaxRetentionDays)
	}
	if patch.MaxTextBytes != nil && (*patch.MaxTextBytes < config.MinMaxTextBytes || *patch.MaxTextBytes > config.MaxMaxTextBytes) {
		return fmt.Errorf("max_text_bytes must be between %d and %d", config.MinMaxTextBytes, config.MaxMaxTextBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	tx, errBegin := s.db.BeginTx(ctx, nil)
	if errBegin != nil {
		return fmt.Errorf("begin settings update: %w", errBegin)
	}
	rollback := func(err error) error {
		_ = tx.Rollback()
		return err
	}
	if patch.RetentionDays != nil {
		if _, errExec := tx.ExecContext(ctx, `INSERT INTO settings(name, value) VALUES ('retention_days', ?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, *patch.RetentionDays); errExec != nil {
			return rollback(fmt.Errorf("update retention_days: %w", errExec))
		}
	}
	if patch.DefaultAuditEnabled != nil {
		if _, errExec := tx.ExecContext(ctx, `INSERT INTO settings(name, value) VALUES ('default_audit_enabled', ?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, boolInt(*patch.DefaultAuditEnabled)); errExec != nil {
			return rollback(fmt.Errorf("update default_audit_enabled: %w", errExec))
		}
	}
	if patch.MaxTextBytes != nil {
		if _, errExec := tx.ExecContext(ctx, `INSERT INTO settings(name, value) VALUES ('max_text_bytes', ?) ON CONFLICT(name) DO UPDATE SET value=excluded.value`, *patch.MaxTextBytes); errExec != nil {
			return rollback(fmt.Errorf("update max_text_bytes: %w", errExec))
		}
	}
	if errCommit := tx.Commit(); errCommit != nil {
		return fmt.Errorf("commit settings update: %w", errCommit)
	}
	return nil
}

// InsertAudit persists only a bounded audit record identified by a canonical hash.
func (s *Store) InsertAudit(ctx context.Context, record AuditRecord) error {
	return s.upsertAudit(ctx, record, false)
}

// UpsertAuditDraft inserts or refreshes one request draft without storing raw payloads.
func (s *Store) UpsertAuditDraft(ctx context.Context, record AuditRecord) error {
	return s.upsertAudit(ctx, record, true)
}

func (s *Store) upsertAudit(ctx context.Context, record AuditRecord, byRequestID bool) error {
	keyHash, errHash := model.NormalizeKeyHash(record.KeyHash)
	if errHash != nil {
		return errHash
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now()
	}
	if record.Model != "" {
		var errModel error
		record.Model, errModel = model.NormalizeModelID(record.Model)
		if errModel != nil {
			return errModel
		}
	}
	record.KeyHash = keyHash
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	settings, errSettings := s.getSettingsLocked(ctx, Settings{RetentionDays: config.DefaultRetentionDays, DefaultAuditEnabled: true, MaxTextBytes: config.DefaultMaxTextBytes})
	if errSettings != nil {
		return errSettings
	}
	if len([]byte(record.Text)) > settings.MaxTextBytes {
		record.Text = string([]byte(record.Text)[:settings.MaxTextBytes])
		record.TextTruncated = true
	}
	if byRequestID && record.RequestID != "" {
		var existingID int64
		var existingAvailable int
		if errQuery := s.db.QueryRowContext(ctx, `SELECT id, text_available FROM audit_records WHERE request_id = ? ORDER BY id DESC LIMIT 1`, record.RequestID).Scan(&existingID, &existingAvailable); errQuery == nil {
			if existingAvailable != 0 && !record.TextAvailable {
				return nil
			}
			_, errUpdate := s.db.ExecContext(ctx, `UPDATE audit_records SET model=COALESCE(NULLIF(?, ''), model), source_format=COALESCE(NULLIF(?, ''), source_format), text=?, text_available=?, text_unavailable_reason=?, text_truncated=?, security_signal=COALESCE(NULLIF(?, ''), security_signal), security_message=COALESCE(NULLIF(?, ''), security_message) WHERE id=?`, record.Model, record.SourceFormat, record.Text, boolInt(record.TextAvailable), record.TextUnavailableReason, boolInt(record.TextTruncated), record.SecuritySignal, record.SecurityMessage, existingID)
			if errUpdate != nil {
				return fmt.Errorf("update audit draft %s: %w", record.RequestID, errUpdate)
			}
			return nil
		} else if !errors.Is(errQuery, sql.ErrNoRows) {
			return fmt.Errorf("find audit draft %s: %w", record.RequestID, errQuery)
		}
	}
	_, errExec := s.db.ExecContext(ctx, `INSERT INTO audit_records(key_hash, created_at, model, source_format, request_id, outcome, status_code, text, text_available, text_unavailable_reason, text_truncated, security_signal, security_message) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, record.KeyHash, record.CreatedAt.Unix(), record.Model, record.SourceFormat, record.RequestID, record.Outcome, record.StatusCode, record.Text, boolInt(record.TextAvailable), record.TextUnavailableReason, boolInt(record.TextTruncated), record.SecuritySignal, record.SecurityMessage)
	if errExec != nil {
		return fmt.Errorf("insert audit record: %w", errExec)
	}
	return nil
}

// FinalizeAudit updates a correlated draft and can create a metadata-only row when no draft exists.
func (s *Store) FinalizeAudit(ctx context.Context, record AuditRecord, allowInsert bool) error {
	keyHash, errHash := model.NormalizeKeyHash(record.KeyHash)
	if errHash != nil {
		return errHash
	}
	if record.Model != "" {
		var errModel error
		record.Model, errModel = model.NormalizeModelID(record.Model)
		if errModel != nil {
			return errModel
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	var id int64
	var existingOutcome string
	errQuery := s.db.QueryRowContext(ctx, `SELECT id, outcome FROM audit_records WHERE request_id = ? ORDER BY id DESC LIMIT 1`, record.RequestID).Scan(&id, &existingOutcome)
	if errQuery == nil {
		if existingOutcome == "rejected" {
			return nil
		}
		_, errUpdate := s.db.ExecContext(ctx, `UPDATE audit_records SET outcome=?, status_code=?, model=COALESCE(NULLIF(?, ''), model), source_format=COALESCE(NULLIF(?, ''), source_format), security_signal=COALESCE(NULLIF(?, ''), security_signal), security_message=COALESCE(NULLIF(?, ''), security_message) WHERE id=?`, record.Outcome, record.StatusCode, record.Model, record.SourceFormat, record.SecuritySignal, record.SecurityMessage, id)
		if errUpdate != nil {
			return fmt.Errorf("finalize audit request %s: %w", record.RequestID, errUpdate)
		}
		return nil
	}
	if !errors.Is(errQuery, sql.ErrNoRows) {
		return fmt.Errorf("find audit request %s: %w", record.RequestID, errQuery)
	}
	if !allowInsert {
		return nil
	}
	record.KeyHash = keyHash
	record.Text = ""
	record.TextAvailable = false
	if record.TextUnavailableReason == "" {
		record.TextUnavailableReason = "request_body_unavailable"
	}
	_, errInsert := s.db.ExecContext(ctx, `INSERT INTO audit_records(key_hash, created_at, model, source_format, request_id, outcome, status_code, text, text_available, text_unavailable_reason, text_truncated, security_signal, security_message) VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, ?, 0, ?, ?)`, keyHash, time.Now().Unix(), record.Model, record.SourceFormat, record.RequestID, record.Outcome, record.StatusCode, record.TextUnavailableReason, record.SecuritySignal, record.SecurityMessage)
	if errInsert != nil {
		return fmt.Errorf("insert completion audit request %s: %w", record.RequestID, errInsert)
	}
	return nil
}

// GetAuditByRequestID returns one bounded record for lifecycle correlation tests and diagnostics.
func (s *Store) GetAuditByRequestID(ctx context.Context, requestID string) (AuditRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AuditRecord{}, ErrClosed
	}
	return scanAuditRecord(s.db.QueryRowContext(ctx, `SELECT id, key_hash, created_at, model, source_format, request_id, outcome, status_code, text, text_available, text_unavailable_reason, text_truncated, security_signal, security_message FROM audit_records WHERE request_id = ? ORDER BY id DESC LIMIT 1`, requestID))
}

// GetAuditByID returns one bounded audit record for the management detail endpoint.
func (s *Store) GetAuditByID(ctx context.Context, id int64) (AuditRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AuditRecord{}, ErrClosed
	}
	return scanAuditRecord(s.db.QueryRowContext(ctx, `SELECT id, key_hash, created_at, model, source_format, request_id, outcome, status_code, text, text_available, text_unavailable_reason, text_truncated, security_signal, security_message FROM audit_records WHERE id = ?`, id))
}

// ListAudit returns deterministic pages ordered by newest creation time and ID.
func (s *Store) ListAudit(ctx context.Context, filter AuditFilter, page, pageSize int) (AuditPage, error) {
	if page < 1 || pageSize < 1 {
		return AuditPage{}, fmt.Errorf("page and page_size must be positive")
	}
	offset := (page - 1) * pageSize
	if offset < 0 || offset > 100000000 {
		return AuditPage{}, fmt.Errorf("page is too large")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AuditPage{}, ErrClosed
	}
	where := make([]string, 0, 6)
	args := make([]any, 0, 6)
	if filter.From != nil {
		where = append(where, "created_at >= ?")
		args = append(args, filter.From.Unix())
	}
	if filter.To != nil {
		where = append(where, "created_at <= ?")
		args = append(args, filter.To.Unix())
	}
	if filter.KeyHash != "" {
		where = append(where, "key_hash = ?")
		args = append(args, filter.KeyHash)
	}
	if filter.Model != "" {
		where = append(where, "model = ?")
		args = append(args, filter.Model)
	}
	if filter.SourceFormat != "" {
		where = append(where, "source_format = ?")
		args = append(args, filter.SourceFormat)
	}
	if filter.Outcome != "" {
		where = append(where, "outcome = ?")
		args = append(args, filter.Outcome)
	}
	if filter.SecuritySignal != "" {
		where = append(where, "security_signal = ?")
		args = append(args, filter.SecuritySignal)
	}
	whereSQL := ""
	if len(where) > 0 {
		whereSQL = " WHERE " + strings.Join(where, " AND ")
	}
	var total int
	if errCount := s.db.QueryRowContext(ctx, "SELECT COUNT(1) FROM audit_records"+whereSQL, args...).Scan(&total); errCount != nil {
		return AuditPage{}, fmt.Errorf("count audit records: %w", errCount)
	}
	queryArgs := append(append([]any(nil), args...), pageSize+1, offset)
	rows, errQuery := s.db.QueryContext(ctx, "SELECT id, key_hash, created_at, model, source_format, request_id, outcome, status_code, text, text_available, text_unavailable_reason, text_truncated, security_signal, security_message FROM audit_records"+whereSQL+" ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?", queryArgs...)
	if errQuery != nil {
		return AuditPage{}, fmt.Errorf("list audit records: %w", errQuery)
	}
	defer rows.Close()
	records := make([]AuditRecord, 0, pageSize)
	for rows.Next() {
		record, errScan := scanAuditRecord(rows)
		if errScan != nil {
			return AuditPage{}, fmt.Errorf("scan audit record: %w", errScan)
		}
		records = append(records, record)
	}
	if errRows := rows.Err(); errRows != nil {
		return AuditPage{}, fmt.Errorf("iterate audit records: %w", errRows)
	}
	hasNext := len(records) > pageSize
	if hasNext {
		records = records[:pageSize]
	}
	return AuditPage{Records: records, Page: page, PageSize: pageSize, Total: total, HasNext: hasNext}, nil
}

func scanAuditRecord(scanner interface{ Scan(...any) error }) (AuditRecord, error) {
	var record AuditRecord
	var createdAt int64
	var available, truncated int
	if errScan := scanner.Scan(&record.ID, &record.KeyHash, &createdAt, &record.Model, &record.SourceFormat, &record.RequestID, &record.Outcome, &record.StatusCode, &record.Text, &available, &record.TextUnavailableReason, &truncated, &record.SecuritySignal, &record.SecurityMessage); errScan != nil {
		return AuditRecord{}, errScan
	}
	record.CreatedAt = time.Unix(createdAt, 0)
	record.TextAvailable = available != 0
	record.TextTruncated = truncated != 0
	return record, nil
}

// CleanupExpired removes audit records older than the persisted retention period.
func (s *Store) CleanupExpired(ctx context.Context, now time.Time) (int64, error) {
	settings, errSettings := s.GetSettings(ctx, Settings{RetentionDays: config.DefaultRetentionDays})
	if errSettings != nil {
		return 0, errSettings
	}
	cutoff := now.Add(-time.Duration(settings.RetentionDays) * 24 * time.Hour).Unix()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	result, errExec := s.db.ExecContext(ctx, `DELETE FROM audit_records WHERE created_at < ?`, cutoff)
	if errExec != nil {
		return 0, fmt.Errorf("cleanup expired audit records: %w", errExec)
	}
	count, errCount := result.RowsAffected()
	if errCount != nil {
		return 0, fmt.Errorf("count cleaned audit records: %w", errCount)
	}
	return count, nil
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
