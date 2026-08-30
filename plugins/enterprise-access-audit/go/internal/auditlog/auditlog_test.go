package auditlog

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOpenCleansFrameworkRecordsAndMarkers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "key-abcdef12.jsonl")
	content := strings.Join([]string{
		`{"id":1,"key_hash":"abcdef12","created_at":"2026-08-30T00:00:00Z","request_id":"framework","text":"<system-reminder>internal</system-reminder>","text_available":true}`,
		`{"id":2,"key_hash":"abcdef12","created_at":"2026-08-30T00:00:01Z","request_id":"human-old","text":"§1689§ <!-- +2h --> actual prompt\n<ctx-search-hint>internal</ctx-search-hint>","text_available":true}`,
		`{"id":3,"key_hash":"abcdef12","created_at":"2026-08-30T00:00:02Z","request_id":"human-new","text":"§1689§ actual prompt","text_available":true}`,
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	log, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer log.Close()
	page, err := log.List(context.Background(), Filter{}, 1, 10)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].Text != "actual prompt" || page.Records[0].RequestID != "human-new" {
		t.Fatalf("cleaned records = %+v", page.Records)
	}
}

func TestLogStoresOneDatedJSONLFilePerKeyAndFinalizesDraft(t *testing.T) {
	log, err := Open(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer log.Close()

	createdAt := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	draft := Record{KeyHash: "abcdef12", CreatedAt: createdAt, RequestID: "request-1", Model: "model", Text: "latest user", TextAvailable: true}
	if err := log.UpsertDraft(context.Background(), draft, 100); err != nil {
		t.Fatalf("UpsertDraft() error = %v", err)
	}
	files, err := filepath.Glob(filepath.Join(log.dir, "*.jsonl"))
	if err != nil || len(files) != 0 {
		t.Fatalf("draft created files = %v, %v; want no finalized file", files, err)
	}
	if got, err := log.GetByRequestID(context.Background(), "request-1"); err != nil || got.Text != "latest user" {
		t.Fatalf("GetByRequestID() = %+v, %v", got, err)
	}
	if err := log.Finalize(context.Background(), Record{KeyHash: "abcdef12", RequestID: "request-1", Outcome: "succeeded", StatusCode: 200}, false, 100); err != nil {
		t.Fatalf("Finalize() error = %v", err)
	}
	files, _ = filepath.Glob(filepath.Join(log.dir, "*.jsonl"))
	expectedPath := datedAuditLogPath(log.dir, "abcdef12", createdAt)
	if len(files) != 1 || files[0] != expectedPath {
		t.Fatalf("finalized files = %v, want %s", files, expectedPath)
	}
	page, err := log.List(context.Background(), Filter{KeyHash: "abcdef12"}, 1, 10)
	if err != nil || len(page.Records) != 1 || page.Records[0].Outcome != "succeeded" || page.Records[0].Text != "latest user" {
		t.Fatalf("List() = %+v, %v", page, err)
	}
}

func TestLogSkipsTornLinesAndCleansExpiredRecords(t *testing.T) {
	dir := t.TempDir()
	log, err := Open(context.Background(), dir)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	old := time.Date(2026, 8, 28, 12, 0, 0, 0, time.UTC)
	newTime := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	if err := log.Insert(context.Background(), Record{KeyHash: "abcdef12", CreatedAt: old, RequestID: "old", Text: "old", TextAvailable: true}, 100); err != nil {
		t.Fatalf("Insert(old) error = %v", err)
	}
	if err := log.Insert(context.Background(), Record{KeyHash: "abcdef12", CreatedAt: newTime, RequestID: "new", Text: "new", TextAvailable: true}, 100); err != nil {
		t.Fatalf("Insert(new) error = %v", err)
	}
	filePath := datedAuditLogPath(dir, "abcdef12", newTime)
	file, err := os.OpenFile(filePath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("open log for torn line: %v", err)
	}
	_, _ = file.WriteString("{torn json}\n")
	_ = file.Close()

	page, err := log.List(context.Background(), Filter{}, 1, 10)
	if err != nil || len(page.Records) != 2 {
		t.Fatalf("List() with torn line = %+v, %v", page, err)
	}
	removed, err := log.Cleanup(context.Background(), time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC))
	if err != nil || removed != 1 {
		t.Fatalf("Cleanup() = %d, %v", removed, err)
	}
	content, err := os.ReadFile(filePath)
	if err != nil || strings.Contains(string(content), "old") || !strings.Contains(string(content), "new") {
		t.Fatalf("compacted file = %q, error = %v", content, err)
	}
	_ = log.Close()
}
