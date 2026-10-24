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

type fakeProjections struct {
	facts     map[string]model.AppointmentFact
	processed map[string]bool
	activity  map[string]int   // day|dentist -> count
	revenue   map[string]int64 // month -> cents
	conflict  bool
}

func newFakeProjections() *fakeProjections {
	return &fakeProjections{facts: map[string]model.AppointmentFact{}, processed: map[string]bool{}, activity: map[string]int{}, revenue: map[string]int64{}}
}

func (f *fakeProjections) FindAppointmentFact(_ context.Context, id string) (model.AppointmentFact, bool, error) {
	fact, ok := f.facts[id]
	return fact, ok, nil
}

func (f *fakeProjections) Project(_ context.Context, p out.Projection) error {
	if f.processed[p.Event.EventID] {
		return out.ErrDuplicateEvent
	}
	if f.conflict {
		return out.ErrVersionConflict
	}
	f.processed[p.Event.EventID] = true
	if p.Fact != nil {
		f.facts[p.Fact.AppointmentID] = *p.Fact
	}
	if p.Activity != nil {
		f.activity[p.Activity.Day+"|"+p.Activity.DentistID]++
	}
	if p.Revenue != nil {
		f.revenue[p.Revenue.Month] += p.Revenue.Cents
	}
	return nil
}

const (
	aptA = "aaaaaaaa-0000-4000-8000-0000000000a1"
	denA = "dddddddd-0000-4000-8000-0000000000d1"
)

func projector() (*AnalyticsProjector, *fakeProjections) {
	store := newFakeProjections()
	loc, _ := time.LoadLocation("America/Bogota")
	return &AnalyticsProjector{Projections: store, Location: loc}, store
}

func event(id, kind string, at time.Time, payload map[string]any) in.IncomingEvent {
	return in.IncomingEvent{EventID: id, EventType: kind, EventVersion: 1, OccurredAt: at, Payload: payload}
}

var at10 = time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)

func scheduled(id string, at time.Time) in.IncomingEvent {
	return event(id, "AppointmentScheduled", at, map[string]any{"appointmentId": aptA, "patientId": "p", "dentistId": denA,
		"startAt": "2026-10-23T15:00:00Z", "endAt": "2026-10-23T16:00:00Z", "reminderDueAt": "2026-10-22T15:00:00Z", "scheduledBy": "s"})
}

func TestProjectorFollowsTheAppointmentLifecycle(t *testing.T) {
	p, store := projector()
	ctx := context.Background()
	if err := p.Consume(ctx, scheduled("ev-1", at10)); err != nil {
		t.Fatal(err)
	}
	moved := event("ev-2", "AppointmentRescheduled", at10.Add(time.Hour), map[string]any{"appointmentId": aptA, "previousStartAt": "2026-10-23T15:00:00Z",
		"newStartAt": "2026-10-24T14:00:00Z", "newEndAt": "2026-10-24T15:00:00Z", "newReminderDueAt": "2026-10-23T14:00:00Z", "appointmentVersion": float64(2), "rescheduledBy": "s"})
	if err := p.Consume(ctx, moved); err != nil {
		t.Fatal(err)
	}
	f := store.facts[aptA]
	if f.DentistID != denA || !f.StartAt.Equal(time.Date(2026, 10, 24, 14, 0, 0, 0, time.UTC)) || f.Status != model.AppointmentScheduled || f.AppointmentVersion != 2 {
		t.Fatalf("rescheduled fact = %+v", f)
	}
	cancelled := event("ev-3", "AppointmentCancelled", at10.Add(2*time.Hour), map[string]any{"appointmentId": aptA, "patientId": "p", "dentistId": denA,
		"cancelledAt": "2026-10-23T17:00:00Z", "cancelledBy": "s", "reason": "r", "override": false})
	if err := p.Consume(ctx, cancelled); err != nil || store.facts[aptA].Status != model.AppointmentCancelled {
		t.Fatalf("cancelled: %v %+v", err, store.facts[aptA])
	}
	// A late copy of the original scheduling never resurrects the appointment.
	if err := p.Consume(ctx, scheduled("ev-4", at10.Add(-time.Hour))); err != nil || store.facts[aptA].Status != model.AppointmentCancelled {
		t.Fatalf("out of order: %v %+v", err, store.facts[aptA])
	}
	if !store.processed["ev-4"] {
		t.Fatal("an older event is still recorded as processed")
	}
}

func TestProjectorWaitsForTheSchedulingFact(t *testing.T) {
	p, store := projector()
	early := event("ev-1", "AppointmentCompleted", at10, map[string]any{"appointmentId": aptA, "patientId": "p", "dentistId": denA, "completedAt": "2026-10-23T16:00:00Z"})
	if err := p.Consume(context.Background(), early); !errors.Is(err, out.ErrNotReady) || len(store.processed) != 0 {
		t.Fatalf("a completion before its scheduling is redelivered later: %v", err)
	}
}

func TestProjectorCountsDeclaredEncountersAndCollectedRevenue(t *testing.T) {
	p, store := projector()
	ctx := context.Background()
	declared := event("ev-c", "ClinicalCareCompleted", at10, map[string]any{"clinicalRecordId": "r", "consultationId": "c", "appointmentId": aptA,
		"patientId": "p", "dentistId": denA, "procedureIds": []any{}, "manualChargeIds": []any{}, "completedAt": "2026-10-24T03:00:00Z", "clinicalVersion": float64(2)})
	if err := p.Consume(ctx, declared); err != nil || store.activity["2026-10-23|"+denA] != 1 {
		t.Fatalf("a declaration at 03:00 UTC counts on the clinic day before: %v %v", err, store.activity)
	}
	payment := func(id, amount, currency string) in.IncomingEvent {
		return event(id, "PaymentRecorded", at10, map[string]any{"paymentId": id, "invoiceId": "i", "patientId": "p", "amount": amount,
			"currency": currency, "remainingBalance": "0.00", "method": "CASH", "recordedAt": "2026-11-01T02:00:00Z", "recordedBy": "s"})
	}
	if err := p.Consume(ctx, payment("ev-p1", "150000.50", "COP")); err != nil || store.revenue["2026-10"] != 15000050 {
		t.Fatalf("revenue: %v %v", err, store.revenue)
	}
	if err := p.Consume(ctx, payment("ev-p1", "150000.50", "COP")); err != nil || store.revenue["2026-10"] != 15000050 {
		t.Fatalf("a duplicate delivery is acknowledged without counting twice: %v %v", err, store.revenue)
	}
	for name, e := range map[string]in.IncomingEvent{
		"binary amount":   payment("ev-p2", "1.5", "COP"),
		"other currency":  payment("ev-p3", "10.00", "USD"),
		"missing dentist": event("ev-x", "ClinicalCareCompleted", at10, map[string]any{"completedAt": "2026-10-23T15:00:00Z"}),
	} {
		if err := p.Consume(ctx, e); err == nil || errors.Is(err, out.ErrNotReady) {
			t.Fatalf("%s must be rejected as malformed: %v", name, err)
		}
	}
}

func TestProjectorIgnoresOtherEventsAndRetriesConflicts(t *testing.T) {
	p, store := projector()
	if err := p.Consume(context.Background(), event("ev-o", "AppointmentConfirmed", at10, map[string]any{})); err != nil || len(store.processed) != 0 {
		t.Fatalf("events outside HU-CLN-003 are acknowledged untouched: %v", err)
	}
	store.conflict = true
	if err := p.Consume(context.Background(), scheduled("ev-1", at10)); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("a concurrent change is retried: %v", err)
	}
}
