package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func treatment(id, patient string, at time.Time) model.Treatment {
	return model.Treatment{ID: id, RecordID: "r-" + patient, PatientID: patient, ClinicalReason: "c", Status: model.StatusPlanned,
		Procedures: []model.Procedure{{ID: "p-" + id, TreatmentID: id, Code: "OPER", AppointmentID: "a", Status: model.StatusPlanned}},
		CreatedBy:  "d", CreatedAt: at, UpdatedAt: at, Version: 1}
}

func TestMemoryTreatmentsCreateUpdateAndConflict(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	tr := treatment("t1", "p1", now)
	if err := m.CreateTreatment(ctx, tr, key("k1", "t1"), []out.OutboxEvent{{EventID: "ev"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.CreateTreatment(ctx, treatment("t2", "p1", now), key("k1", "t2"), nil); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.GetTreatment(ctx, "t2"); !errors.Is(err, out.ErrNotFound) || len(m.Outbox()) != 1 {
		t.Fatal("a rejected create leaves nothing behind")
	}
	started := tr
	started.Status, started.Version = model.StatusInProgress, 2
	if err := m.UpdateTreatment(ctx, started, 1, key("k2", "t1")); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetTreatment(ctx, "t1"); got.Status != model.StatusInProgress || got.Version != 2 {
		t.Fatalf("got %+v", got)
	}
	if err := m.UpdateTreatment(ctx, started, 1, key("k3", "t1")); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("a stale update must conflict, got %v", err)
	}
	if _, ok, _ := m.FindIdempotency(ctx, "d", "op", "k3"); ok {
		t.Fatal("a conflicting update must not store its key")
	}
}

func TestMemoryListTreatmentsScopedNewestFirst(t *testing.T) {
	m := NewMemory()
	base := time.Now()
	for i, p := range []string{"p1", "p2", "p1"} {
		id := "t" + string(rune('a'+i))
		_ = m.CreateTreatment(ctx, treatment(id, p, base.Add(time.Duration(i)*time.Minute)), key("k"+id, id), nil)
	}
	items, total, _ := m.ListTreatments(ctx, out.RecordFilter{PatientIDs: []string{"p1"}, Limit: 5})
	if total != 2 || len(items) != 2 || items[0].ID != "tc" || items[1].ID != "ta" {
		t.Fatalf("got %d %+v", total, items)
	}
}
