package usecase

import (
	"context"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// fakeEncounters marks appointments whose encounter was already declared.
type fakeEncounters struct{ closed map[string]bool }

func (f *fakeEncounters) FindClosuresByAppointment(_ context.Context, apt string) ([]model.CareClosure, error) {
	if f.closed[apt] {
		return []model.CareClosure{{ID: "cc-" + apt, AppointmentID: apt}}, nil
	}
	return nil, nil
}

func wantClosed(t *testing.T, err error) {
	t.Helper()
	if kindOf(t, err) != model.KindConflict || err.(*model.Error).Code != "CARE_STATE_CONFLICT" {
		t.Fatalf("a write to a clinically closed encounter must be rejected, got %v", err)
	}
}

func TestClosedEncounterRejectsLateEntriesAndAmendments(t *testing.T) {
	ef := setupEntries(t)
	open, err := ef.record(dentist, ef.rec1, model.KindConsultation, "k-open-01")
	if err != nil {
		t.Fatal(err)
	}
	ef.encounters.closed["apt-1"] = true
	_, err = ef.record(dentist, ef.rec1, model.KindEvolution, "k-late-01")
	wantClosed(t, err)
	_, err = ef.svc.Amend(context.Background(), in.AmendEntryCommand{Actor: dentist, EntryID: open.Entry.ID, Text: "x", Reason: "r", ExpectedVersion: 1, IdempotencyKey: "k-late-02", RequestHash: "h"})
	wantClosed(t, err)
	if _, err := ef.record(dentist, ef.rec1, model.KindAllergy, "k-free-01", func(c *in.RecordEntryCommand) { c.AppointmentID = "" }); err != nil {
		t.Fatalf("a longitudinal entry outside any encounter stays allowed: %v", err)
	}
	if _, err := ef.record(dentist, ef.rec1, model.KindEvolution, "k-open-02", func(c *in.RecordEntryCommand) { c.AppointmentID = "apt-2" }); err != nil {
		t.Fatalf("another, open encounter stays writable: %v", err)
	}
}

func TestClosedEncounterRejectsLatePlansAndCompletions(t *testing.T) {
	pf := setupProcedures(t, true)
	pf.encounters.closed["apt-1"] = true
	_, err := pf.complete(dentist, "k-late-03")
	wantClosed(t, err)
	_, err = pf.plan(dentist, "pat-1", "k-late-04")
	wantClosed(t, err)
}
