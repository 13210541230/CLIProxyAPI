package auditlog

import (
	"context"
	"testing"
	"time"
)

func TestLogDeleteAndDeleteAll(t *testing.T) {
	ctx := context.Background()
	log, errOpen := Open(ctx, t.TempDir())
	if errOpen != nil {
		t.Fatalf("open log: %v", errOpen)
	}
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		if errInsert := log.Insert(ctx, Record{
			KeyHash:   "abcdef12",
			CreatedAt: now,
			Model:     "gpt-5.6-luna",
			RequestID: string(rune('a' + i)),
			Outcome:   "allow",
		}, 0); errInsert != nil {
			t.Fatalf("insert %d: %v", i, errInsert)
		}
	}
	listed, errList := log.List(ctx, Filter{}, 1, 50)
	if errList != nil {
		t.Fatalf("list: %v", errList)
	}
	if listed.Total != 3 {
		t.Fatalf("total = %d, want 3", listed.Total)
	}
	firstID := listed.Records[2].ID // records sort newest-first

	removed, errDelete := log.Delete(ctx, []int64{firstID, 999999})
	if errDelete != nil {
		t.Fatalf("delete: %v", errDelete)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1 (unknown ids are ignored)", removed)
	}
	afterDelete, errList := log.List(ctx, Filter{}, 1, 50)
	if errList != nil {
		t.Fatalf("list after delete: %v", errList)
	}
	if afterDelete.Total != 2 {
		t.Fatalf("total after delete = %d, want 2", afterDelete.Total)
	}
	for _, record := range afterDelete.Records {
		if record.ID == firstID {
			t.Fatalf("deleted id %d still present", firstID)
		}
	}

	empty, errEmpty := log.Delete(ctx, []int64{})
	if errEmpty != nil || empty != 0 {
		t.Fatalf("empty delete = (%d, %v), want (0, nil)", empty, errEmpty)
	}

	removedAll, errAll := log.DeleteAll(ctx)
	if errAll != nil {
		t.Fatalf("delete all: %v", errAll)
	}
	if removedAll != 2 {
		t.Fatalf("removedAll = %d, want 2", removedAll)
	}
	final, errFinal := log.List(ctx, Filter{}, 1, 50)
	if errFinal != nil {
		t.Fatalf("list final: %v", errFinal)
	}
	if final.Total != 0 {
		t.Fatalf("total after delete all = %d, want 0", final.Total)
	}
}
