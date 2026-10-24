package persistence

import (
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func TestMongoOutboxStoreDrivesPublication(t *testing.T) {
	m := newTestMongo(t)
	for _, c := range []string{"outbox_events", "consultations", "clinical_entries", "diagnoses"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
	base := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	for i, id := range []string{"ev-a", "ev-b", "ev-c"} {
		ev := out.OutboxEvent{EventID: id, EventType: "ConsultationRecorded", EventVersion: 1, AggregateID: "rec-1", AggregateType: "ClinicalRecord",
			CorrelationID: "corr", CausationID: "corr", ActorID: "d", Payload: map[string]any{"consultationId": "c-" + id, "patientId": "p"}, OccurredAt: base.Add(time.Duration(i) * time.Minute)}
		e := kindEntry("c-"+id, model.KindConsultation, base)
		if err := m.CreateEntry(ctx, e, entryKey("key-ob-"+id, e.ID, base), []out.OutboxEvent{ev}); err != nil {
			t.Fatal(err)
		}
	}
	due, err := m.Due(ctx, base.Add(time.Hour), 2)
	if err != nil || len(due) != 2 || due[0].Event.EventID != "ev-a" || due[1].Event.EventID != "ev-b" {
		t.Fatalf("due = %+v %v", due, err)
	}
	got := due[0].Event
	if got.EventVersion != 1 || got.AggregateType != "ClinicalRecord" || got.ActorID != "d" || got.Payload["consultationId"] != "c-ev-a" || !got.OccurredAt.Equal(base) || due[0].Attempts != 0 {
		t.Fatalf("event = %+v", got)
	}

	now := base.Add(time.Hour)
	if err := m.MarkPublished(ctx, "ev-a", now); err != nil {
		t.Fatal(err)
	}
	if err := m.Reschedule(ctx, "ev-b", 1, now.Add(time.Minute), "broker connection reset"); err != nil {
		t.Fatal(err)
	}
	if err := m.MarkFailed(ctx, "ev-c", 5, "no route"); err != nil {
		t.Fatal(err)
	}
	if due, _ := m.Due(ctx, now, 10); len(due) != 0 {
		t.Fatalf("published, rescheduled and failed events are not due now: %+v", due)
	}
	later, _ := m.Due(ctx, now.Add(2*time.Minute), 10)
	if len(later) != 1 || later[0].Event.EventID != "ev-b" || later[0].Attempts != 1 {
		t.Fatalf("a rescheduled event becomes due again: %+v", later)
	}
	var row bson.M
	_ = m.db.Collection("outbox_events").FindOne(ctx, bson.D{{Key: "_id", Value: "ev-a"}}).Decode(&row)
	if row["status"] != "PUBLISHED" || row["publishedAt"] == nil {
		t.Fatalf("published row = %v", row)
	}
	_ = m.db.Collection("outbox_events").FindOne(ctx, bson.D{{Key: "_id", Value: "ev-c"}}).Decode(&row)
	if row["status"] != "FAILED" || row["failureReason"] != "no route" {
		t.Fatalf("failed row = %v", row)
	}
}
