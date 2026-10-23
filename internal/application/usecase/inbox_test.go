package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

type fakeInbox struct {
	closures map[string]model.CareClosure
	received map[string]out.InboxRecord
}

func (f *fakeInbox) Received(_ context.Context, id string) (bool, error) {
	_, ok := f.received[id]
	return ok, nil
}

func (f *fakeInbox) Apply(_ context.Context, r out.InboxRecord, updates []out.ClosureUpdate) error {
	if _, ok := f.received[r.EventID]; ok {
		return out.ErrDuplicateEvent
	}
	for _, u := range updates {
		if f.closures[u.Closure.ID].Version != u.FromVersion {
			return out.ErrVersionConflict
		}
	}
	for _, u := range updates {
		f.closures[u.Closure.ID] = u.Closure
	}
	f.received[r.EventID] = r
	return nil
}

func (f *fakeInbox) FindClosuresByAppointment(_ context.Context, apt string) ([]model.CareClosure, error) {
	var found []model.CareClosure
	for _, c := range f.closures {
		if c.AppointmentID == apt {
			found = append(found, c)
		}
	}
	return found, nil
}

func (f *fakeInbox) FindClosureByEncounter(_ context.Context, rec, cons string) (model.CareClosure, bool, error) {
	for _, c := range f.closures {
		if c.RecordID == rec && c.ConsultationID == cons {
			return c, true, nil
		}
	}
	return model.CareClosure{}, false, nil
}

func newInbox(t *testing.T) (*fakeInbox, *InboxService, model.CareClosure) {
	c, err := model.DeclareCareCompletion(model.DeclarationInput{ClosureID: "cc-1", RecordID: "rec-1", ConsultationID: "cons-1", AppointmentID: "apt-1",
		PatientID: "pat-1", DentistID: "dentist-1", ProcedureIDs: []string{"p-2", "p-1"}, ManualChargeIDs: []string{"m-1"}, ClinicalVersion: 2, At: t0})
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeInbox{closures: map[string]model.CareClosure{c.ID: c}, received: map[string]out.InboxRecord{}}
	return f, &InboxService{Inbox: f, Encounters: f, Clock: &fixedClock{t: t0}}, c
}

func appointmentCompleted(id, apt, patient string) in.IncomingEvent {
	return in.IncomingEvent{EventID: id, EventType: "AppointmentCompleted", EventVersion: 1, AggregateID: apt, CorrelationID: "corr", OccurredAt: t0,
		Payload: map[string]any{"appointmentId": apt, "patientId": patient, "dentistId": "dentist-1", "completedAt": t0.Format(time.RFC3339)}}
}

func chargesCompleted(id string, procs, charges []any) in.IncomingEvent {
	return in.IncomingEvent{EventID: id, EventType: "EncounterChargesCompleted", EventVersion: 1, AggregateID: "acc-1", CorrelationID: "corr", OccurredAt: t0,
		Payload: map[string]any{"encounterAccountId": "acc-1", "clinicalRecordId": "rec-1", "consultationId": "cons-1", "appointmentId": "apt-1",
			"patientId": "pat-1", "procedureIds": procs, "manualChargeIds": charges, "billableItemIds": []any{"b-1"}, "total": "10.00", "currency": "COP"}}
}

func TestInboxCompletesTheClosureWithBothOutcomesInAnyOrder(t *testing.T) {
	f, svc, c := newInbox(t)
	if err := svc.Consume(context.Background(), chargesCompleted("ev-1", []any{"p-1", "p-2"}, []any{"m-1"})); err != nil {
		t.Fatal(err)
	}
	if got := f.closures[c.ID]; got.BillingOutcome != model.OutcomeCompleted || got.Status != model.ClosureAwaitingFinalization {
		t.Fatalf("after billing %+v", got)
	}
	if err := svc.Consume(context.Background(), appointmentCompleted("ev-2", "apt-1", "pat-1")); err != nil {
		t.Fatal(err)
	}
	if got := f.closures[c.ID]; got.Status != model.ClosureCompleted || got.Version != 3 {
		t.Fatalf("after appointment %+v", got)
	}
	if f.received["ev-2"].Status != "PROCESSED" {
		t.Fatal("the receipt is stored with the effect")
	}
}

func TestInboxIsIdempotentPerEvent(t *testing.T) {
	f, svc, c := newInbox(t)
	ev := appointmentCompleted("ev-1", "apt-1", "pat-1")
	_ = svc.Consume(context.Background(), ev)
	if err := svc.Consume(context.Background(), ev); err != nil {
		t.Fatalf("a redelivered event is acknowledged, got %v", err)
	}
	if f.closures[c.ID].Version != 2 {
		t.Fatal("a redelivered event never applies twice")
	}
	if err := svc.Consume(context.Background(), appointmentCompleted("ev-3", "apt-1", "pat-1")); err != nil || f.closures[c.ID].Version != 2 {
		t.Fatal("a second fact with the same outcome is a no-op on the closure")
	}
}

func TestInboxRejectsMismatchedFactsWithoutEffect(t *testing.T) {
	cases := map[string]in.IncomingEvent{
		"other patient":        appointmentCompleted("ev-x", "apt-1", "pat-9"),
		"different procedures": chargesCompleted("ev-y", []any{"p-1"}, []any{"m-1"}),
		"different charges":    chargesCompleted("ev-z", []any{"p-1", "p-2"}, []any{}),
		"unsupported version":  {EventID: "ev-v", EventType: "AppointmentCompleted", EventVersion: 2, Payload: map[string]any{}},
		"malformed payload":    {EventID: "ev-m", EventType: "AppointmentCompleted", EventVersion: 1, Payload: map[string]any{"appointmentId": 7}},
	}
	for name, ev := range cases {
		t.Run(name, func(t *testing.T) {
			f, svc, c := newInbox(t)
			if err := svc.Consume(context.Background(), ev); err != nil {
				t.Fatalf("a mismatched fact is recorded and acknowledged, got %v", err)
			}
			if f.received[ev.EventID].Status != "FAILED" || f.received[ev.EventID].FailureReason == "" || f.closures[c.ID].Version != 1 {
				t.Fatalf("receipt %+v closure %+v", f.received[ev.EventID], f.closures[c.ID])
			}
		})
	}
}

func TestInboxAsksToRedeliverFactsThatArriveTooEarly(t *testing.T) {
	f, svc, _ := newInbox(t)
	if err := svc.Consume(context.Background(), appointmentCompleted("ev-1", "apt-unknown", "pat-1")); !errors.Is(err, out.ErrNotReady) {
		t.Fatalf("got %v", err)
	}
	if _, ok := f.received["ev-1"]; ok {
		t.Fatal("an early fact is not recorded, so its redelivery can still apply")
	}
}

func TestInboxIgnoresEventsItDoesNotSubscribeTo(t *testing.T) {
	f, svc, _ := newInbox(t)
	if err := svc.Consume(context.Background(), in.IncomingEvent{EventID: "ev-o", EventType: "InvoicePaid", EventVersion: 1, Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if f.received["ev-o"].Status != "PROCESSED" {
		t.Fatal("an unrelated fact is acknowledged and recorded without effect")
	}
}
