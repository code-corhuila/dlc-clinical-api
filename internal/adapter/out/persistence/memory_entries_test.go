package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func entry(id, record string, at time.Time) model.Entry {
	return model.Entry{ID: id, RecordID: record, Kind: model.KindEvolution, Text: "t", AuthorID: "d", CreatedAt: at, UpdatedAt: at, Version: 1}
}

func TestMemoryEntriesAreAtomicWithKeyAndOutbox(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	ev := []out.OutboxEvent{{EventID: "ev-1", EventType: "ConsultationRecorded"}}
	if err := m.CreateEntry(ctx, entry("e1", "r1", now), key("k1", "e1"), ev); err != nil {
		t.Fatal(err)
	}
	if got, err := m.GetEntry(ctx, "e1"); err != nil || got.RecordID != "r1" {
		t.Fatalf("got %+v %v", got, err)
	}
	if len(m.Outbox()) != 1 || m.Outbox()[0].EventID != "ev-1" {
		t.Fatalf("outbox = %+v", m.Outbox())
	}
	if err := m.CreateEntry(ctx, entry("e2", "r1", now), key("k1", "e2"), ev); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.GetEntry(ctx, "e2"); !errors.Is(err, out.ErrNotFound) || len(m.Outbox()) != 1 {
		t.Fatal("a rejected write must leave neither entry nor event behind")
	}
}

func TestMemoryListEntriesOldestFirstPerRecord(t *testing.T) {
	m := NewMemory()
	base := time.Now()
	for i, id := range []string{"a", "b", "c"} {
		_ = m.CreateEntry(ctx, entry(id, "r1", base.Add(time.Duration(i)*time.Minute)), key("k"+id, id), nil)
	}
	_ = m.CreateEntry(ctx, entry("x", "r2", base), key("kx", "x"), nil)
	items, total, _ := m.ListEntries(ctx, "r1", 1, 5)
	if total != 3 || len(items) != 2 || items[0].ID != "b" || items[1].ID != "c" {
		t.Fatalf("got %d %+v", total, items)
	}
	if items, total, _ := m.ListEntries(ctx, "r1", 9, 5); total != 3 || len(items) != 0 {
		t.Fatal("beyond the last page is empty")
	}
}
