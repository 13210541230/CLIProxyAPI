package management

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/plugins/enterprise-access-audit/go/internal/store"
)

func insertAuditRecord(t *testing.T, manager interface {
	WithStore(context.Context, func(*store.Store) error) error
}, requestID string) {
	t.Helper()
	err := manager.WithStore(context.Background(), func(active *store.Store) error {
		return active.InsertAudit(context.Background(), store.AuditRecord{
			KeyHash:       "abcdef12",
			CreatedAt:     time.Now().UTC(),
			Model:         "gpt-5.6-luna",
			SourceFormat:  "codex",
			RequestID:     requestID,
			Outcome:       "allow",
			StatusCode:    200,
			Text:          "hello " + requestID,
			TextAvailable: true,
		})
	})
	if err != nil {
		t.Fatalf("insert audit record: %v", err)
	}
}

func TestAuditDeleteRoute(t *testing.T) {
	handler, manager := newTestHandler(t)
	ctx := context.Background()
	path := "/v0/management/enterprise-access-audit/audit/delete"

	// Method gate: DELETE is rejected, POST is allowed.
	if response := handler.Handle(ctx, ManagementRequest{Method: http.MethodDelete, Path: path}); response.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("DELETE status = %d, want 405", response.StatusCode)
	}

	// Validation: empty payload and invalid ids are rejected.
	if response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPost, Path: path, Body: []byte(`{}`)}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400", response.StatusCode)
	}
	if response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPost, Path: path, Body: []byte(`{"ids":[0]}`)}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid id status = %d, want 400", response.StatusCode)
	}
	if response := handler.Handle(ctx, ManagementRequest{Method: http.MethodPost, Path: path, Body: []byte(`{`)}); response.StatusCode != http.StatusBadRequest {
		t.Fatalf("malformed body status = %d, want 400", response.StatusCode)
	}

	insertAuditRecord(t, manager, "req-del-1")
	insertAuditRecord(t, manager, "req-del-2")

	var list struct {
		Records []struct {
			ID int64 `json:"id"`
		} `json:"records"`
		Pagination struct {
			Total int `json:"total"`
		} `json:"pagination"`
	}
	listResponse := handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: "/v0/management/enterprise-access-audit/audit"})
	if listResponse.StatusCode != http.StatusOK {
		t.Fatalf("list status = %d", listResponse.StatusCode)
	}
	decodeResponse(t, listResponse, &list)
	if list.Pagination.Total != 2 || len(list.Records) != 2 {
		t.Fatalf("list total = %d rows = %d, want 2/2", list.Pagination.Total, len(list.Records))
	}

	target := list.Records[0].ID
	var deleted struct {
		Deleted int64 `json:"deleted"`
	}
	delResponse := handler.Handle(ctx, ManagementRequest{Method: http.MethodPost, Path: path, Body: []byte(`{"ids":[` + itoa(target) + `]}`)})
	if delResponse.StatusCode != http.StatusOK {
		t.Fatalf("delete status = %d body=%s", delResponse.StatusCode, delResponse.Body)
	}
	decodeResponse(t, delResponse, &deleted)
	if deleted.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", deleted.Deleted)
	}

	listResponse = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: "/v0/management/enterprise-access-audit/audit"})
	decodeResponse(t, listResponse, &list)
	if list.Pagination.Total != 1 {
		t.Fatalf("total after delete = %d, want 1", list.Pagination.Total)
	}
	for _, record := range list.Records {
		if record.ID == target {
			t.Fatalf("deleted id %d still listed", target)
		}
	}

	deleted.Deleted = 0
	delResponse = handler.Handle(ctx, ManagementRequest{Method: http.MethodPost, Path: path, Body: []byte(`{"all":true}`)})
	if delResponse.StatusCode != http.StatusOK {
		t.Fatalf("delete-all status = %d body=%s", delResponse.StatusCode, delResponse.Body)
	}
	decodeResponse(t, delResponse, &deleted)
	if deleted.Deleted != 1 {
		t.Fatalf("deleted all = %d, want 1", deleted.Deleted)
	}

	listResponse = handler.Handle(ctx, ManagementRequest{Method: http.MethodGet, Path: "/v0/management/enterprise-access-audit/audit"})
	decodeResponse(t, listResponse, &list)
	if list.Pagination.Total != 0 {
		t.Fatalf("total after delete-all = %d, want 0", list.Pagination.Total)
	}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
