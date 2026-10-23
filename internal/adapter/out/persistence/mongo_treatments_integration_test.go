package persistence

import (
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func cleanTreatments(t *testing.T, m *Mongo) {
	t.Helper()
	for _, c := range []string{"treatments", "procedures", "outbox_events", "idempotency_keys"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMongoTreatmentLifecycle(t *testing.T) {
	m := newTestMongo(t)
	cleanTreatments(t, m)
	at := time.Date(2026, 10, 21, 11, 0, 0, 0, time.UTC)
	tr := treatment("t-1", "p-1", at)
	tr.DiagnosisID = "d-1"
	ev := out.OutboxEvent{EventID: "ev-t1", EventType: "TreatmentPlanned", EventVersion: 1, AggregateID: "t-1", AggregateType: "Treatment",
		CorrelationID: "c", ActorID: "d", Payload: map[string]any{"treatmentId": "t-1", "procedureCount": 1}, OccurredAt: at}
	if err := m.CreateTreatment(ctx, tr, entryKey("key-tr-01", "t-1", at), []out.OutboxEvent{ev}); err != nil {
		t.Fatalf("rejected by the real validators: %v", err)
	}
	got, err := m.GetTreatment(ctx, "t-1")
	if err != nil || got.ID != tr.ID || got.DiagnosisID != "d-1" || got.CreatedBy != "d" || len(got.Procedures) != 1 || got.Procedures[0] != tr.Procedures[0] || !got.CreatedAt.Equal(at) {
		t.Fatalf("got %+v %v", got, err)
	}
	var proc bson.M
	if err := m.db.Collection("procedures").FindOne(ctx, bson.D{{Key: "_id", Value: "p-t-1"}}).Decode(&proc); err != nil || proc["treatmentId"] != "t-1" || proc["patientId"] != "p-1" || proc["status"] != "PLANNED" {
		t.Fatalf("planned procedure document = %v %v", proc, err)
	}

	started := got
	started.Status, started.Version, started.UpdatedAt = model.StatusInProgress, 2, at.Add(time.Minute)
	if err := m.UpdateTreatment(ctx, started, 1, entryKey("key-tr-02", "t-1", at)); err != nil {
		t.Fatal(err)
	}
	if now, _ := m.GetTreatment(ctx, "t-1"); now.Status != model.StatusInProgress || now.Version != 2 {
		t.Fatalf("after start %+v", now)
	}
	if err := m.UpdateTreatment(ctx, started, 1, entryKey("key-tr-03", "t-1", at)); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("stale update: got %v", err)
	}
	if _, ok, _ := m.FindIdempotency(ctx, "d", "op", "key-tr-03"); ok {
		t.Fatal("a conflicting update must roll back its key")
	}
	if _, err := m.GetTreatment(ctx, "missing"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMongoTreatmentCreateIsAtomic(t *testing.T) {
	m := newTestMongo(t)
	cleanTreatments(t, m)
	at := time.Now().UTC().Truncate(time.Second)
	_ = m.CreateTreatment(ctx, treatment("t-1", "p-1", at), entryKey("key-tr-01", "t-1", at), nil)
	err := m.CreateTreatment(ctx, treatment("t-2", "p-1", at), entryKey("key-tr-01", "t-2", at), []out.OutboxEvent{{EventID: "ev-x", EventType: "TreatmentPlanned", EventVersion: 1, AggregateID: "t-2", AggregateType: "Treatment", Payload: map[string]any{}, OccurredAt: at}})
	if !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("got %v", err)
	}
	for c, id := range map[string]string{"treatments": "t-2", "procedures": "p-t-2", "outbox_events": "ev-x"} {
		if n, _ := m.db.Collection(c).CountDocuments(ctx, bson.D{{Key: "_id", Value: id}}); n != 0 {
			t.Fatalf("%s kept %s after rollback", c, id)
		}
	}
}

func TestMongoListTreatmentsScopedOldestFirst(t *testing.T) {
	m := newTestMongo(t)
	cleanTreatments(t, m)
	base := time.Now().UTC().Truncate(time.Second)
	for i, p := range []string{"p1", "p2", "p1"} {
		id := "t" + string(rune('a'+i))
		if err := m.CreateTreatment(ctx, treatment(id, p, base.Add(time.Duration(i)*time.Minute)), entryKey("key-l-"+id, id, base), nil); err != nil {
			t.Fatal(err)
		}
	}
	items, total, err := m.ListTreatments(ctx, out.RecordFilter{PatientIDs: []string{"p1"}, Limit: 5})
	if err != nil || total != 2 || len(items) != 2 || items[0].ID != "ta" || items[1].ID != "tc" || len(items[0].Procedures) != 1 {
		t.Fatalf("got %d %+v %v", total, items, err)
	}
}
