package persistence

import (
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func closure(id, record, consultation string, version int, at time.Time) model.CareClosure {
	return model.CareClosure{ID: id, RecordID: record, ConsultationID: consultation, AppointmentID: "a", PatientID: "p1", DentistID: "d",
		ProcedureIDs: []string{}, ManualChargeIDs: []string{}, Status: model.ClosureAwaitingFinalization,
		AppointmentOutcome: model.OutcomePending, BillingOutcome: model.OutcomePending, CompletedAt: at, ClinicalVersion: version,
		CreatedAt: at, UpdatedAt: at, Version: 1}
}

func TestMemoryClosuresDeclareOncePerEncounter(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	_ = m.CreateRecord(ctx, rec("r1", "p1", now), key("k-rec", "r1"))
	tr := treatment("t1", "p1", now)
	tr.Procedures[0].RecordID, tr.Procedures[0].Status = "r1", model.StatusCompleted
	_ = m.CreateTreatment(ctx, tr, key("k-plan", "t1"), nil)

	ids, err := m.CompletedProcedureIDs(ctx, "r1", "a")
	if err != nil || len(ids) != 1 || ids[0] != "p-t1" {
		t.Fatalf("completed procedures = %v %v", ids, err)
	}
	if ids, _ := m.CompletedProcedureIDs(ctx, "r1", "other"); len(ids) != 0 {
		t.Fatal("procedures of another encounter are excluded")
	}

	c := closure("cc1", "r1", "c1", 2, now)
	if err := m.DeclareCareCompletion(ctx, c, 1, key("k-decl", "cc1"), []out.OutboxEvent{{EventID: "ev"}}); err != nil {
		t.Fatal(err)
	}
	if r, _ := m.GetRecord(ctx, "r1"); r.Version != 2 {
		t.Fatalf("record version = %d", r.Version)
	}
	if got, ok, _ := m.FindClosureByEncounter(ctx, "r1", "c1"); !ok || got.ID != "cc1" {
		t.Fatal("closure by encounter")
	}
	if err := m.DeclareCareCompletion(ctx, closure("cc2", "r1", "c1", 3, now), 2, key("k-decl-2", "cc2"), nil); !errors.Is(err, out.ErrClosureExists) {
		t.Fatalf("second declaration: got %v", err)
	}
	if err := m.DeclareCareCompletion(ctx, closure("cc3", "r1", "c3", 2, now), 1, key("k-decl-3", "cc3"), nil); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("stale record: got %v", err)
	}
	if _, err := m.GetClosure(ctx, "cc3"); !errors.Is(err, out.ErrNotFound) || len(m.Outbox()) != 1 {
		t.Fatal("a rejected declaration leaves nothing behind")
	}
}

func TestMemoryRetryClosureOnlyFromItsVersion(t *testing.T) {
	m := NewMemory()
	now := time.Now()
	_ = m.CreateRecord(ctx, rec("r1", "p1", now), key("k-rec", "r1"))
	c := closure("cc1", "r1", "c1", 2, now)
	_ = m.DeclareCareCompletion(ctx, c, 1, key("k-decl", "cc1"), nil)
	retried := c
	retried.Version = 2
	if err := m.RetryClosure(ctx, retried, 1, key("k-retry", "cc1")); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetClosure(ctx, "cc1"); got.Version != 2 {
		t.Fatalf("closure = %+v", got)
	}
	if err := m.RetryClosure(ctx, retried, 1, key("k-retry-2", "cc1")); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("a stale retry is rejected: %v", err)
	}
	if err := m.RetryClosure(ctx, retried, 2, key("k-retry", "cc1")); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("a reused key is a duplicate: %v", err)
	}
}
