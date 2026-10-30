package usecase

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

type fakeOutbox struct {
	due         []out.PendingEvent
	asked       int
	published   []string
	rescheduled map[string]time.Time
	failed      map[string]string
	closures    map[string]string // eventID -> record/consultation of the failed closure
}

func (f *fakeOutbox) Due(_ context.Context, _ time.Time, limit int) ([]out.PendingEvent, error) {
	f.asked = limit
	return f.due, nil
}
func (f *fakeOutbox) MarkPublished(_ context.Context, id string, _ time.Time) error {
	f.published = append(f.published, id)
	return nil
}
func (f *fakeOutbox) Reschedule(_ context.Context, id string, _ int, next time.Time, _ string) error {
	f.rescheduled[id] = next
	return nil
}
func (f *fakeOutbox) MarkFailed(_ context.Context, id string, _ int, reason string) error {
	f.failed[id] = reason
	return nil
}
func (f *fakeOutbox) MarkDeclarationFailed(_ context.Context, id string, _ int, reason, recordID, consultationID string, _ time.Time) error {
	f.failed[id] = reason
	f.closures[id] = recordID + "/" + consultationID
	return nil
}

type fakePublisher struct {
	failures map[string]error
	sent     []out.Envelope
	onSend   func()
}

func (p *fakePublisher) Publish(_ context.Context, e out.Envelope) error {
	if p.onSend != nil {
		p.onSend()
	}
	if err := p.failures[e.EventID]; err != nil {
		return err
	}
	p.sent = append(p.sent, e)
	return nil
}

func pending(id string, attempts int) out.PendingEvent {
	return out.PendingEvent{Attempts: attempts, Event: out.OutboxEvent{EventID: id, EventType: "ProcedureCompleted", EventVersion: 1, AggregateID: "t-1",
		AggregateType: "Treatment", CorrelationID: "corr-op", CausationID: "corr-op", ActorID: "dentist-1", Payload: map[string]any{"procedureId": "p-1"}, OccurredAt: t0}}
}

func newRelay(due []out.PendingEvent, failures map[string]error) (*fakeOutbox, *fakePublisher, *RelayService) {
	store := &fakeOutbox{due: due, rescheduled: map[string]time.Time{}, failed: map[string]string{}, closures: map[string]string{}}
	pub := &fakePublisher{failures: failures}
	return store, pub, &RelayService{Outbox: store, Publisher: pub, Clock: staticClock{t0}, Random: func() float64 { return 0.5 },
		BatchSize: 3, MaxAttempts: 5, BaseDelay: time.Second, MaxDelay: time.Minute}
}

func TestRelayPublishesTheEnvelopeOfEachDueEvent(t *testing.T) {
	store, pub, relay := newRelay([]out.PendingEvent{pending("ev-1", 0)}, nil)
	report, err := relay.RunOnce(context.Background())
	if err != nil || report.Published != 1 || store.asked != 3 || len(store.published) != 1 {
		t.Fatalf("report %+v err %v store %+v", report, err, store)
	}
	e := pub.sent[0]
	if e.EventID != "ev-1" || e.Version != 1 || e.Producer != "clinical-service" || e.AggregateType != "Treatment" || e.OccurredAt != t0.Format(time.RFC3339) ||
		e.Metadata.CorrelationID != "corr-op" || e.Metadata.ActorUserID != "dentist-1" || e.Payload["procedureId"] != "p-1" {
		t.Fatalf("envelope = %+v", e)
	}
}

func TestRelayFailureOfOneItemDoesNotStopTheBatch(t *testing.T) {
	store, _, relay := newRelay([]out.PendingEvent{pending("ev-1", 0), pending("ev-2", 0), pending("ev-3", 0)},
		map[string]error{"ev-2": errors.New("broker connection reset")})
	report, _ := relay.RunOnce(context.Background())
	if report.Published != 2 || report.Retried != 1 || len(store.published) != 2 || store.published[1] != "ev-3" {
		t.Fatalf("report %+v published %v", report, store.published)
	}
	next := store.rescheduled["ev-2"]
	if !next.After(t0) || next.After(t0.Add(time.Second)) {
		t.Fatalf("first retry waits a random part of the base delay, got %v", next.Sub(t0))
	}
}

func TestRelayBackoffIsExponentialWithFullJitterAndCapped(t *testing.T) {
	store, _, relay := newRelay([]out.PendingEvent{pending("ev-1", 3), pending("ev-2", 3)}, map[string]error{"ev-1": errors.New("timeout"), "ev-2": errors.New("timeout")})
	relay.MaxAttempts = 20
	draws := []float64{0.25, 1}
	relay.Random = func() float64 { d := draws[0]; draws = draws[1:]; return d }
	_, _ = relay.RunOnce(context.Background())
	if got := store.rescheduled["ev-1"].Sub(t0); got != 2*time.Second {
		t.Fatalf("attempt 4 waits rand*base*2^3 = 0.25*8s, got %v", got)
	}
	relay.BaseDelay, relay.MaxDelay = time.Second, 5*time.Second
	if got := store.rescheduled["ev-2"].Sub(t0); got != 8*time.Second {
		t.Fatalf("got %v", got)
	}
	relay.Random = func() float64 { return 1 }
	capped := relay.backoff(10)
	if capped > relay.MaxDelay {
		t.Fatalf("the delay is capped, got %v", capped)
	}
}

func TestRelayGivesUpAfterMaxAttemptsAndOnPermanentErrors(t *testing.T) {
	store, _, relay := newRelay([]out.PendingEvent{pending("ev-1", 4), pending("ev-2", 0)},
		map[string]error{"ev-1": errors.New("timeout"), "ev-2": fmt.Errorf("no route: %w", out.ErrPermanentPublish)})
	report, _ := relay.RunOnce(context.Background())
	if report.Failed != 2 || store.failed["ev-1"] == "" || store.failed["ev-2"] == "" || len(store.rescheduled) != 0 {
		t.Fatalf("report %+v store %+v", report, store)
	}
}

func TestRelayStopsWhenTheRunTimeIsOver(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, pub, relay := newRelay([]out.PendingEvent{pending("ev-1", 0), pending("ev-2", 0)}, nil)
	pub.onSend = cancel
	_, _ = relay.RunOnce(ctx)
	if len(store.published) != 1 {
		t.Fatalf("an expired run processes nothing more, published %v", store.published)
	}
}

// staticClock never advances, so retry delays are measured exactly.
type staticClock struct{ t time.Time }

func (c staticClock) Now() time.Time { return c.t }

// An undeliverable declaration is a persistent failure of its closure, which
// must become CLOSURE_FAILED with the reason so it is visible and can be
// retried (entities-and-rules.md, ADR-014 point 5); other events only fail.
func TestRelayFailsTheClosureOfAnUndeliverableDeclaration(t *testing.T) {
	decl := pending("ev-decl", 4)
	decl.Event.EventType, decl.Event.AggregateID, decl.Event.AggregateType = "ClinicalCareCompleted", "r-1", "ClinicalRecord"
	decl.Event.Payload = map[string]any{"clinicalRecordId": "r-1", "consultationId": "c-1"}
	store, _, relay := newRelay([]out.PendingEvent{decl, pending("ev-proc", 4)},
		map[string]error{"ev-decl": errors.New("no route"), "ev-proc": errors.New("no route")})
	report, err := relay.RunOnce(context.Background())
	if err != nil || report.Failed != 2 || store.closures["ev-decl"] != "r-1/c-1" || store.failed["ev-proc"] == "" {
		t.Fatalf("report %+v store %+v %v", report, store, err)
	}
	if _, closure := store.closures["ev-proc"]; closure {
		t.Fatal("only the declaration fails a closure")
	}
}
