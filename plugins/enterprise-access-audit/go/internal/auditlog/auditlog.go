package auditlog

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/textclean"
)

var ErrClosed = errors.New("enterprise audit log is closed")

// Record is one finalized or in-flight audit entry stored as JSONL.
type Record struct {
	ID                    int64     `json:"id"`
	KeyHash               string    `json:"key_hash"`
	CreatedAt             time.Time `json:"created_at"`
	Model                 string    `json:"model"`
	SourceFormat          string    `json:"source_format"`
	RequestID             string    `json:"request_id"`
	Outcome               string    `json:"outcome"`
	StatusCode            int       `json:"status_code"`
	Text                  string    `json:"text"`
	TextAvailable         bool      `json:"text_available"`
	TextUnavailableReason string    `json:"text_unavailable_reason,omitempty"`
	TextTruncated         bool      `json:"text_truncated"`
	SecuritySignal        string    `json:"security_signal,omitempty"`
	SecurityMessage       string    `json:"security_message,omitempty"`
}

type Filter struct {
	From           *time.Time
	To             *time.Time
	KeyHash        string
	Model          string
	SourceFormat   string
	Outcome        string
	SecuritySignal string
}

type Page struct {
	Records  []Record
	Page     int
	PageSize int
	Total    int
	HasNext  bool
}

// Log owns per-key JSONL files and keeps only unfinished requests in memory.
type Log struct {
	mu      sync.Mutex
	dir     string
	nextID  int64
	pending map[string]Record
	closed  bool
}

func Open(ctx context.Context, dir string) (*Log, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create audit log directory %q: %w", dir, err)
	}
	log := &Log{dir: dir, nextID: 1, pending: make(map[string]Record)}
	if err := log.initializeNextID(ctx); err != nil {
		return nil, err
	}
	if err := log.sanitizeExistingRecords(ctx); err != nil {
		return nil, err
	}
	return log, nil
}

func (l *Log) initializeNextID(ctx context.Context) error {
	return l.withFiles(ctx, func(_ string, records []Record) error {
		for _, record := range records {
			if record.ID >= l.nextID {
				l.nextID = record.ID + 1
			}
		}
		return nil
	})
}

func (l *Log) sanitizeExistingRecords(ctx context.Context) error {
	for _, path := range l.filePathsLocked() {
		if err := ctx.Err(); err != nil {
			return err
		}
		records, err := readRecords(path)
		if err != nil {
			return err
		}
		cleaned := make([]Record, 0, len(records))
		changed := false
		for _, record := range records {
			if !record.TextAvailable || record.Text == "" {
				cleaned = append(cleaned, record)
				continue
			}
			text, ok := textclean.Clean(record.Text)
			if !ok {
				changed = true
				continue
			}
			if text != record.Text {
				record.Text = text
				changed = true
			}
			cleaned = append(cleaned, record)
		}
		if changed {
			if err := rewriteFile(path, cleaned); err != nil {
				return err
			}
		}
	}
	return nil
}

func (l *Log) Close() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true
	return nil
}

func (l *Log) Insert(ctx context.Context, record Record, maxTextBytes int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	record = l.prepareLocked(record, maxTextBytes)
	return l.appendLocked(record)
}

func (l *Log) UpsertDraft(ctx context.Context, record Record, maxTextBytes int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	record = l.prepareLocked(record, maxTextBytes)
	if record.RequestID == "" {
		return l.appendLocked(record)
	}
	if record.Outcome == "rejected" {
		return l.appendLocked(record)
	}
	if current, exists := l.pending[record.RequestID]; exists {
		record = mergeDraft(current, record)
	}
	l.pending[record.RequestID] = record
	return nil
}

func (l *Log) Finalize(ctx context.Context, record Record, allowInsert bool, maxTextBytes int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return ErrClosed
	}
	if pending, exists := l.pending[record.RequestID]; exists {
		record = mergeFinal(pending, record)
		record = l.prepareLocked(record, maxTextBytes)
		delete(l.pending, record.RequestID)
		return l.appendLocked(record)
	}
	if existing, exists := l.findByRequestIDLocked(ctx, record.RequestID); exists {
		if existing.Outcome == "rejected" || existing.RequestID == record.RequestID {
			return nil
		}
	}
	if !allowInsert {
		return nil
	}
	record.Text = ""
	record.TextAvailable = false
	if record.TextUnavailableReason == "" {
		record.TextUnavailableReason = "request_body_unavailable"
	}
	record = l.prepareLocked(record, maxTextBytes)
	return l.appendLocked(record)
}

func (l *Log) GetByRequestID(ctx context.Context, requestID string) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Record{}, ErrClosed
	}
	if record, exists := l.pending[requestID]; exists {
		return record, nil
	}
	record, exists := l.findByRequestIDLocked(ctx, requestID)
	if !exists {
		return Record{}, sql.ErrNoRows
	}
	return record, nil
}

func (l *Log) GetByID(ctx context.Context, id int64) (Record, error) {
	if err := ctx.Err(); err != nil {
		return Record{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Record{}, ErrClosed
	}
	for _, path := range l.filePathsLocked() {
		records, err := readRecords(path)
		if err != nil {
			return Record{}, err
		}
		for _, record := range records {
			if record.ID == id {
				return record, nil
			}
		}
	}
	return Record{}, sql.ErrNoRows
}

func (l *Log) List(ctx context.Context, filter Filter, page, pageSize int) (Page, error) {
	if page < 1 || pageSize < 1 {
		return Page{}, fmt.Errorf("page and page_size must be positive")
	}
	offset := (page - 1) * pageSize
	if offset < 0 || offset > 100000000 {
		return Page{}, fmt.Errorf("page is too large")
	}
	if err := ctx.Err(); err != nil {
		return Page{}, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return Page{}, ErrClosed
	}
	all, err := l.filteredLocked(ctx, filter)
	if err != nil {
		return Page{}, err
	}
	total := len(all)
	if offset >= total {
		return Page{Records: []Record{}, Page: page, PageSize: pageSize, Total: total}, nil
	}
	end := offset + pageSize
	if end > total {
		end = total
	}
	records := append([]Record(nil), all[offset:end]...)
	return Page{Records: records, Page: page, PageSize: pageSize, Total: total, HasNext: end < total}, nil
}

func (l *Log) Cleanup(ctx context.Context, cutoff time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return 0, ErrClosed
	}
	var removed int64
	for _, path := range l.filePathsLocked() {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		records, err := readRecords(path)
		if err != nil {
			return removed, err
		}
		kept := records[:0]
		for _, record := range records {
			if record.CreatedAt.Before(cutoff) {
				removed++
				continue
			}
			kept = append(kept, record)
		}
		if len(kept) == len(records) {
			continue
		}
		if err := rewriteFile(path, kept); err != nil {
			return removed, err
		}
	}
	return removed, nil
}

func (l *Log) prepareLocked(record Record, maxTextBytes int) Record {
	if record.ID == 0 {
		record.ID = l.nextID
		l.nextID++
	} else if record.ID >= l.nextID {
		l.nextID = record.ID + 1
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	} else {
		record.CreatedAt = record.CreatedAt.UTC()
	}
	if maxTextBytes > 0 && len([]byte(record.Text)) > maxTextBytes {
		record.Text = truncateUTF8(record.Text, maxTextBytes)
		record.TextTruncated = true
	}
	return record
}

func (l *Log) appendLocked(record Record) error {
	path := filepath.Join(l.dir, "key-"+record.KeyHash+".jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("open audit log %q: %w", path, err)
	}
	encoded, err := json.Marshal(record)
	if err == nil {
		encoded = append(encoded, '\n')
		_, err = file.Write(encoded)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return fmt.Errorf("append audit log %q: %w", path, err)
	}
	return nil
}

func (l *Log) filteredLocked(ctx context.Context, filter Filter) ([]Record, error) {
	all := make([]Record, 0)
	for _, path := range l.filePathsLocked() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		records, err := readRecords(path)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			if matches(record, filter) {
				all = append(all, record)
			}
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if all[i].CreatedAt.Equal(all[j].CreatedAt) {
			return all[i].ID > all[j].ID
		}
		return all[i].CreatedAt.After(all[j].CreatedAt)
	})
	return all, nil
}

func (l *Log) findByRequestIDLocked(ctx context.Context, requestID string) (Record, bool) {
	var found Record
	for _, path := range l.filePathsLocked() {
		if err := ctx.Err(); err != nil {
			return Record{}, false
		}
		records, err := readRecords(path)
		if err != nil {
			continue
		}
		for _, record := range records {
			if record.RequestID == requestID && (found.ID == 0 || record.ID > found.ID) {
				found = record
			}
		}
	}
	return found, found.ID != 0
}

func (l *Log) filePathsLocked() []string {
	entries, err := os.ReadDir(l.dir)
	if err != nil {
		return nil
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), "key-") || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		paths = append(paths, filepath.Join(l.dir, entry.Name()))
	}
	sort.Strings(paths)
	return paths
}

func (l *Log) withFiles(ctx context.Context, operation func(string, []Record) error) error {
	for _, path := range l.filePathsLocked() {
		if err := ctx.Err(); err != nil {
			return err
		}
		records, err := readRecords(path)
		if err != nil {
			return err
		}
		if err := operation(path, records); err != nil {
			return err
		}
	}
	return nil
}

func readRecords(path string) ([]Record, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open audit log %q: %w", path, err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	result := make([]Record, 0)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var record Record
		if err := json.Unmarshal([]byte(line), &record); err != nil {
			// A torn or manually edited line must not hide the remaining records.
			continue
		}
		if record.ID == 0 || record.KeyHash == "" {
			continue
		}
		result = append(result, record)
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("read audit log %q: %w", path, err)
	}
	return result, nil
}

func rewriteFile(path string, records []Record) error {
	if len(records) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove expired audit log %q: %w", path, err)
		}
		return nil
	}
	tmp := path + ".tmp"
	file, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create compacted audit log %q: %w", tmp, err)
	}
	writer := bufio.NewWriter(file)
	for _, record := range records {
		encoded, marshalErr := json.Marshal(record)
		if marshalErr != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("encode compacted audit record: %w", marshalErr)
		}
		if _, writeErr := writer.Write(append(encoded, '\n')); writeErr != nil {
			_ = file.Close()
			_ = os.Remove(tmp)
			return fmt.Errorf("write compacted audit log: %w", writeErr)
		}
	}
	if err := writer.Flush(); err != nil {
		_ = file.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("flush compacted audit log: %w", err)
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("close compacted audit log: %w", err)
	}
	if err := os.Remove(path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace audit log %q: %w", path, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename compacted audit log %q: %w", path, err)
	}
	return nil
}

func mergeDraft(current, next Record) Record {
	if current.TextAvailable && !next.TextAvailable {
		next.Text = current.Text
		next.TextAvailable = true
		next.TextUnavailableReason = current.TextUnavailableReason
		next.TextTruncated = current.TextTruncated
	}
	if next.ID == 0 {
		next.ID = current.ID
	}
	if next.CreatedAt.IsZero() {
		next.CreatedAt = current.CreatedAt
	}
	return next
}

func mergeFinal(draft, final Record) Record {
	final.ID = draft.ID
	final.KeyHash = draft.KeyHash
	final.CreatedAt = draft.CreatedAt
	final.Text = draft.Text
	final.TextAvailable = draft.TextAvailable
	final.TextUnavailableReason = draft.TextUnavailableReason
	final.TextTruncated = draft.TextTruncated
	if final.Model == "" {
		final.Model = draft.Model
	}
	if final.SourceFormat == "" {
		final.SourceFormat = draft.SourceFormat
	}
	if final.RequestID == "" {
		final.RequestID = draft.RequestID
	}
	return final
}

func matches(record Record, filter Filter) bool {
	if filter.From != nil && record.CreatedAt.Before(*filter.From) {
		return false
	}
	if filter.To != nil && record.CreatedAt.After(*filter.To) {
		return false
	}
	if filter.KeyHash != "" && record.KeyHash != filter.KeyHash {
		return false
	}
	if filter.Model != "" && record.Model != filter.Model {
		return false
	}
	if filter.SourceFormat != "" && record.SourceFormat != filter.SourceFormat {
		return false
	}
	if filter.Outcome != "" && record.Outcome != filter.Outcome {
		return false
	}
	return filter.SecuritySignal == "" || record.SecuritySignal == filter.SecuritySignal
}

func truncateUTF8(value string, maxBytes int) string {
	if maxBytes <= 0 || len([]byte(value)) <= maxBytes {
		return value
	}
	result := value[:maxBytes]
	for len(result) > 0 && !utf8.ValidString(result) {
		result = result[:len(result)-1]
	}
	return result
}
