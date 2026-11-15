package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func TestMemoryProceduresCompleteOnceWithExtras(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	tr := treatment("t1", "p1", now)
	tr.Procedures[0].RecordID, tr.Procedures[0].PatientID, tr.Procedures[0].Version = "r-p1", "p1", 1
	_ = m.CreateTreatment(ctx, tr, key("k-plan", "t1"), nil)

	p, err := m.GetProcedure(ctx, "p-t1")
	if err != nil || p.TreatmentID != "t1" || p.PatientID != "p1" || p.Version != 1 || p.Status != model.StatusPlanned {
		t.Fatalf("got %+v %v", p, err)
	}
	done := p
	done.Status, done.Version, done.CompletedBy, done.CompletedAt = model.StatusCompleted, 2, "d", now
	extras := []out.RecordedExtra{{Extra: model.Extra{SourceRecordID: "src-1", Type: model.ExtraAdditionalMaterial, Quantity: "1", Description: "x", ClinicalReason: "y"}, ProcedureID: "p-t1"}}
	if err := m.CompleteProcedure(ctx, done, 1, extras, key("k-done", "p-t1"), []out.OutboxEvent{{EventID: "ev"}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetTreatment(ctx, "t1"); got.Procedures[0].Status != model.StatusCompleted {
		t.Fatal("the treatment view must show the completed procedure")
	}
	if err := m.CompleteProcedure(ctx, done, 1, nil, key("k-again", "p-t1"), nil); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("stale completion: got %v", err)
	}
	if err := m.CompleteProcedure(ctx, done, 2, extras, key("k-src", "p-t1"), nil); !errors.Is(err, out.ErrDuplicateSource) {
		t.Fatalf("reused source: got %v", err)
	}
	if _, ok, _ := m.FindIdempotency(ctx, "d", "op", "k-src"); ok || len(m.Outbox()) != 1 {
		t.Fatal("a rejected completion leaves nothing behind")
	}
	if _, err := m.GetProcedure(ctx, "missing"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
