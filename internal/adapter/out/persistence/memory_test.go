package persistence

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var ctx = context.Background()

func rec(id, patient string, at time.Time) model.ClinicalRecord {
	return model.ClinicalRecord{ID: id, PatientID: patient, CreatedBy: "d", CreatedAt: at, UpdatedAt: at, Version: 1}
}

func key(k, resource string) out.IdempotencyRecord {
	return out.IdempotencyRecord{ActorID: "d", Operation: "op", Key: k, RequestHash: "h", ResourceID: resource, ResponseStatus: 201}
}

func TestMemoryCreateGetAndIdempotency(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	if err := m.CreateRecord(ctx, rec("r1", "p1", now), key("k1", "r1")); err != nil {
		t.Fatal(err)
	}
	got, err := m.GetRecord(ctx, "r1")
	if err != nil || got.PatientID != "p1" {
		t.Fatalf("got %+v %v", got, err)
	}
	saved, ok, err := m.FindIdempotency(ctx, "d", "op", "k1")
	if err != nil || !ok || saved.ResourceID != "r1" {
		t.Fatalf("idempotency %+v %v %v", saved, ok, err)
	}
	if _, ok, _ := m.FindIdempotency(ctx, "other", "op", "k1"); ok {
		t.Fatal("keys are scoped by actor")
	}
	if _, err := m.GetRecord(ctx, "missing"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMemoryRejectsDuplicatesAtomically(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	_ = m.CreateRecord(ctx, rec("r1", "p1", now), key("k1", "r1"))
	if err := m.CreateRecord(ctx, rec("r2", "p2", now), key("k1", "r2")); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("got %v", err)
	}
	if err := m.CreateRecord(ctx, rec("r3", "p1", now), key("k3", "r3")); !errors.Is(err, out.ErrRecordExists) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.GetRecord(ctx, "r2"); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("a rejected write must leave no record behind")
	}
	if _, ok, _ := m.FindIdempotency(ctx, "d", "op", "k3"); ok {
		t.Fatal("a rejected write must leave no key behind")
	}
}

func TestMemoryListOldestFirstWithFilterAndWindow(t *testing.T) {
	m := NewMemory()
	base := time.Now()
	for i, p := range []string{"p1", "p2", "p3"} {
		_ = m.CreateRecord(ctx, rec("r"+p, p, base.Add(time.Duration(i)*time.Minute)), key("k"+p, "r"+p))
	}
	items, total, _ := m.ListRecords(ctx, out.RecordFilter{Limit: 2})
	if total != 3 || len(items) != 2 || items[0].PatientID != "p1" || items[1].PatientID != "p2" {
		t.Fatalf("got %d %+v", total, items)
	}
	items, total, _ = m.ListRecords(ctx, out.RecordFilter{PatientIDs: []string{"p1", "p2"}, Offset: 1, Limit: 5})
	if total != 2 || len(items) != 1 || items[0].PatientID != "p2" {
		t.Fatalf("got %d %+v", total, items)
	}
	items, total, _ = m.ListRecords(ctx, out.RecordFilter{PatientIDs: []string{}, Limit: 5})
	if total != 0 || len(items) != 0 {
		t.Fatal("an empty scope sees nothing")
	}
}

func TestMemoryIsSafeForConcurrentCreates(t *testing.T) {
	m := NewMemory()
	var wg sync.WaitGroup
	errs := make(chan error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- m.CreateRecord(ctx, rec("r", "p1", time.Now()), key("same", "r"))
		}()
	}
	wg.Wait()
	close(errs)
	created := 0
	for err := range errs {
		if err == nil {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("exactly one concurrent create must win, got %d", created)
	}
}
