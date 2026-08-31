package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/auditlog"
	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/model"
)

// retireLegacyAudit removes the old SQLite audit table after making an optional
// consistent backup. Policies and settings remain in the active database.
func (s *Store) retireLegacyAudit(ctx context.Context, databasePath string, backup bool) error {
	var tableName string
	errQuery := s.db.QueryRowContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'audit_records'`).Scan(&tableName)
	if errors.Is(errQuery, sql.ErrNoRows) {
		return nil
	}
	if errQuery != nil {
		return fmt.Errorf("inspect legacy audit table: %w", errQuery)
	}
	if tableName != "audit_records" {
		return fmt.Errorf("unexpected legacy audit table name %q", tableName)
	}
	if backup {
		if _, errCheckpoint := s.db.ExecContext(ctx, `PRAGMA wal_checkpoint(FULL)`); errCheckpoint != nil {
			return fmt.Errorf("checkpoint legacy audit database: %w", errCheckpoint)
		}
		backupPath := nextLegacyBackupPath(databasePath)
		if _, errBackup := s.db.ExecContext(ctx, `VACUUM INTO ?`, backupPath); errBackup != nil {
			return fmt.Errorf("backup legacy audit database: %w", errBackup)
		}
	}
	if _, errDrop := s.db.ExecContext(ctx, `DROP INDEX IF EXISTS idx_audit_records_created_at;
		DROP INDEX IF EXISTS idx_audit_records_key_hash;
		DROP INDEX IF EXISTS idx_audit_records_model;
		DROP INDEX IF EXISTS idx_audit_records_security_signal;
		DROP TABLE IF EXISTS audit_records;`); errDrop != nil {
		return fmt.Errorf("remove legacy audit table: %w", errDrop)
	}
	if _, errVacuum := s.db.ExecContext(ctx, `VACUUM`); errVacuum != nil {
		return fmt.Errorf("compact policy database after audit reset: %w", errVacuum)
	}
	return nil
}

func nextLegacyBackupPath(databasePath string) string {
	base := databasePath + ".legacy-" + time.Now().UTC().Format("20060102-150405")
	path := base + ".sqlite"
	for index := 1; ; index++ {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return path
		}
		path = fmt.Sprintf("%s-%d.sqlite", base, index)
	}
}

func (s *Store) InsertAudit(ctx context.Context, record AuditRecord) error {
	return s.writeAudit(ctx, record, func(log *auditlog.Log, normalized AuditRecord, maxText int) error {
		return log.Insert(ctx, toAuditLogRecord(normalized), maxText)
	})
}

func (s *Store) UpsertAuditDraft(ctx context.Context, record AuditRecord) error {
	return s.writeAudit(ctx, record, func(log *auditlog.Log, normalized AuditRecord, maxText int) error {
		return log.UpsertDraft(ctx, toAuditLogRecord(normalized), maxText)
	})
}

func (s *Store) writeAudit(ctx context.Context, record AuditRecord, operation func(*auditlog.Log, AuditRecord, int) error) error {
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
	record.KeyHash = keyHash
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.audit == nil {
		return fmt.Errorf("JSONL audit log is unavailable")
	}
	settings, errSettings := s.getSettingsLocked(ctx, Settings{RetentionDays: 30, DefaultAuditEnabled: true, MaxTextBytes: 32 * 1024})
	if errSettings != nil {
		return errSettings
	}
	return operation(s.audit, record, settings.MaxTextBytes)
}

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
	record.KeyHash = keyHash
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	if s.audit == nil {
		return fmt.Errorf("JSONL audit log is unavailable")
	}
	settings, errSettings := s.getSettingsLocked(ctx, Settings{RetentionDays: 30, DefaultAuditEnabled: true, MaxTextBytes: 32 * 1024})
	if errSettings != nil {
		return errSettings
	}
	return s.audit.Finalize(ctx, toAuditLogRecord(record), allowInsert, settings.MaxTextBytes)
}

func (s *Store) GetAuditByRequestID(ctx context.Context, requestID string) (AuditRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AuditRecord{}, ErrClosed
	}
	if s.audit == nil {
		return AuditRecord{}, fmt.Errorf("JSONL audit log is unavailable")
	}
	record, err := s.audit.GetByRequestID(ctx, requestID)
	if err != nil {
		return AuditRecord{}, err
	}
	return fromAuditLogRecord(record), nil
}

func (s *Store) GetAuditByID(ctx context.Context, id int64) (AuditRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AuditRecord{}, ErrClosed
	}
	if s.audit == nil {
		return AuditRecord{}, fmt.Errorf("JSONL audit log is unavailable")
	}
	record, err := s.audit.GetByID(ctx, id)
	if err != nil {
		return AuditRecord{}, err
	}
	return fromAuditLogRecord(record), nil
}

func (s *Store) ListAudit(ctx context.Context, filter AuditFilter, page, pageSize int) (AuditPage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return AuditPage{}, ErrClosed
	}
	if s.audit == nil {
		return AuditPage{}, fmt.Errorf("JSONL audit log is unavailable")
	}
	result, err := s.audit.List(ctx, auditlog.Filter{From: filter.From, To: filter.To, KeyHash: filter.KeyHash, KeyHashes: filter.KeyHashes, Model: filter.Model, SourceFormat: filter.SourceFormat, Outcome: filter.Outcome, SecuritySignal: filter.SecuritySignal}, page, pageSize)
	if err != nil {
		return AuditPage{}, err
	}
	pageResult := AuditPage{Records: make([]AuditRecord, 0, len(result.Records)), Page: result.Page, PageSize: result.PageSize, Total: result.Total, HasNext: result.HasNext}
	for _, record := range result.Records {
		pageResult.Records = append(pageResult.Records, fromAuditLogRecord(record))
	}
	return pageResult, nil
}

func (s *Store) CleanupExpired(ctx context.Context, now time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return 0, ErrClosed
	}
	if s.audit == nil {
		return 0, fmt.Errorf("JSONL audit log is unavailable")
	}
	settings, errSettings := s.getSettingsLocked(ctx, Settings{RetentionDays: 30, DefaultAuditEnabled: true, MaxTextBytes: 32 * 1024})
	if errSettings != nil {
		return 0, errSettings
	}
	cutoff := now.Add(-time.Duration(settings.RetentionDays) * 24 * time.Hour)
	return s.audit.Cleanup(ctx, cutoff)
}

func toAuditLogRecord(record AuditRecord) auditlog.Record {
	return auditlog.Record{ID: record.ID, KeyHash: record.KeyHash, CreatedAt: record.CreatedAt, Model: record.Model, SourceFormat: record.SourceFormat, RequestID: record.RequestID, Outcome: record.Outcome, StatusCode: record.StatusCode, Text: record.Text, TextAvailable: record.TextAvailable, TextUnavailableReason: record.TextUnavailableReason, TextTruncated: record.TextTruncated, SecuritySignal: record.SecuritySignal, SecurityMessage: record.SecurityMessage}
}

func fromAuditLogRecord(record auditlog.Record) AuditRecord {
	return AuditRecord{ID: record.ID, KeyHash: record.KeyHash, CreatedAt: record.CreatedAt, Model: record.Model, SourceFormat: record.SourceFormat, RequestID: record.RequestID, Outcome: record.Outcome, StatusCode: record.StatusCode, Text: record.Text, TextAvailable: record.TextAvailable, TextUnavailableReason: record.TextUnavailableReason, TextTruncated: record.TextTruncated, SecuritySignal: record.SecuritySignal, SecurityMessage: record.SecurityMessage}
}
