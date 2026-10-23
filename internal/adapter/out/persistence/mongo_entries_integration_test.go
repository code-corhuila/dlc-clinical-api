package persistence

import (
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

const testHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func cleanEntries(t *testing.T, m *Mongo) {
	t.Helper()
	for _, c := range []string{"consultations", "diagnoses", "clinical_entries", "outbox_events"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
}

func entryKey(k, id string, at time.Time) out.IdempotencyRecord {
	i := key(k, id)
	i.RequestHash, i.CreatedAt, i.ExpiresAt = testHash, at, at.Add(time.Hour)
	return i
}

func kindEntry(id string, kind model.EntryKind, at time.Time) model.Entry {
	e := model.Entry{ID: id, RecordID: "rec-1", Kind: kind, Text: "nota " + id, Code: "C-1", AppointmentID: "apt-1",
		AuthorID: "dentist-1", CreatedAt: at, UpdatedAt: at, Version: 1}
	if kind == model.KindDiagnosis {
		e.ConsultationID = "e-cons"
	}
	return e
}

func TestMongoEntriesRoundTripEveryKind(t *testing.T) {
	m := newTestMongo(t)
	cleanEntries(t, m)
	base := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	kinds := []model.EntryKind{model.KindConsultation, model.KindDiagnosis, model.KindEvolution, model.KindAntecedent, model.KindAllergy}
	want := map[string]model.Entry{}
	for i, k := range kinds {
		e := kindEntry("e-"+string(k)[:4], k, base.Add(time.Duration(i)*time.Minute))
		if k == model.KindConsultation {
			e.ID = "e-cons"
		}
		want[e.ID] = e
		if err := m.CreateEntry(ctx, e, entryKey("key-entry-"+e.ID, e.ID, base), nil); err != nil {
			t.Fatalf("%s rejected by the real validator: %v", k, err)
		}
	}
	for id, w := range want {
		got, err := m.GetEntry(ctx, id)
		if err != nil || got != w {
			t.Fatalf("%s: got %+v %v, want %+v", id, got, err, w)
		}
	}
	items, total, err := m.ListEntries(ctx, "rec-1", 1, 2)
	if err != nil || total != 5 || len(items) != 2 || items[0].Kind != model.KindDiagnosis || items[1].Kind != model.KindEvolution {
		t.Fatalf("unified list = %d %+v %v", total, items, err)
	}
	if items, total, _ := m.ListEntries(ctx, "other", 0, 5); total != 0 || len(items) != 0 {
		t.Fatal("other records are not mixed in")
	}
	if _, err := m.GetEntry(ctx, "missing"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMongoEntryOutboxIsAtomic(t *testing.T) {
	m := newTestMongo(t)
	cleanEntries(t, m)
	at := time.Now().UTC().Truncate(time.Second)
	ev := out.OutboxEvent{EventID: "ev-1", EventType: "ConsultationRecorded", EventVersion: 1, AggregateID: "rec-1", AggregateType: "ClinicalRecord",
		CorrelationID: "corr-1", CausationID: "corr-1", ActorID: "dentist-1", Payload: map[string]any{"consultationId": "c-1", "patientId": "p-1"}, OccurredAt: at}
	if err := m.CreateEntry(ctx, kindEntry("c-1", model.KindConsultation, at), entryKey("key-out-01", "c-1", at), []out.OutboxEvent{ev}); err != nil {
		t.Fatal(err)
	}
	var doc bson.M
	if err := m.db.Collection("outbox_events").FindOne(ctx, bson.D{{Key: "_id", Value: "ev-1"}}).Decode(&doc); err != nil {
		t.Fatal(err)
	}
	if doc["status"] != "PENDING" || doc["eventVersion"] != "1" || doc["aggregateType"] != "ClinicalRecord" || doc["actorUserId"] != "dentist-1" || doc["correlationId"] != "corr-1" {
		t.Fatalf("outbox row = %v", doc)
	}

	ev.EventID = "ev-2"
	err := m.CreateEntry(ctx, kindEntry("c-2", model.KindConsultation, at), entryKey("key-out-01", "c-2", at), []out.OutboxEvent{ev})
	if !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("got %v", err)
	}
	if _, err := m.GetEntry(ctx, "c-2"); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("the entry of a rejected key must roll back")
	}
	if n, _ := m.db.Collection("outbox_events").CountDocuments(ctx, bson.D{{Key: "_id", Value: "ev-2"}}); n != 0 {
		t.Fatal("the event of a rejected write must roll back")
	}
}
