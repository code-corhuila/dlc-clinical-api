package persistence

import (
	"errors"
	"slices"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func TestMongoDeclareCareCompletion(t *testing.T) {
	m := newTestMongo(t)
	cleanTreatments(t, m)
	for _, c := range []string{"care_closures", "procedure_extras"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	if err := m.CreateRecord(ctx, rec("r-p-1", "p-1", at), entryKey("key-rec-01", "r-p-1", at)); err != nil {
		t.Fatal(err)
	}
	plannedTreatment(t, m, at)
	p, _ := m.GetProcedure(ctx, "p-t-1")
	done := p
	done.Status, done.Version, done.CompletedBy, done.CompletedAt = model.StatusCompleted, 2, "d", at
	if err := m.CompleteProcedure(ctx, done, 1, nil, entryKey("key-done-01", "p-t-1", at), nil); err != nil {
		t.Fatal(err)
	}

	ids, err := m.CompletedProcedureIDs(ctx, "r-p-1", "a")
	if err != nil || !slices.Equal(ids, []string{"p-t-1"}) {
		t.Fatalf("completed = %v %v", ids, err)
	}
	if ids, _ := m.CompletedProcedureIDs(ctx, "r-p-1", "other"); len(ids) != 0 {
		t.Fatal("procedures of another encounter are excluded")
	}

	c := closure("cc-1", "r-p-1", "c-1", 2, at)
	c.ProcedureIDs, c.ManualChargeIDs, c.CorrelationID = []string{"p-t-1"}, []string{"m-1"}, "corr"
	ev := out.OutboxEvent{EventID: "ev-cc", EventType: "ClinicalCareCompleted", EventVersion: 1, AggregateID: "r-p-1", AggregateType: "ClinicalRecord",
		ActorID: "d", Payload: map[string]any{"clinicalRecordId": "r-p-1"}, OccurredAt: at}
	if err := m.DeclareCareCompletion(ctx, c, 1, entryKey("key-decl-01", "cc-1", at), []out.OutboxEvent{ev}); err != nil {
		t.Fatalf("rejected by the real validators: %v", err)
	}
	got, err := m.GetClosure(ctx, "cc-1")
	if err != nil || got.Status != model.ClosureAwaitingFinalization || got.PatientID != "p1" || !slices.Equal(got.ProcedureIDs, c.ProcedureIDs) ||
		!slices.Equal(got.ManualChargeIDs, c.ManualChargeIDs) || got.ClinicalVersion != 2 || !got.CompletedAt.Equal(at) || got.Version != 1 {
		t.Fatalf("got %+v %v", got, err)
	}
	if byEncounter, ok, _ := m.FindClosureByEncounter(ctx, "r-p-1", "c-1"); !ok || byEncounter.ID != "cc-1" {
		t.Fatal("closure by encounter")
	}
	if r, _ := m.GetRecord(ctx, "r-p-1"); r.Version != 2 {
		t.Fatalf("record version = %d", r.Version)
	}

	second := closure("cc-2", "r-p-1", "c-1", 3, at)
	if err := m.DeclareCareCompletion(ctx, second, 2, entryKey("key-decl-02", "cc-2", at), nil); !errors.Is(err, out.ErrClosureExists) {
		t.Fatalf("second declaration: got %v", err)
	}
	stale := closure("cc-3", "r-p-1", "c-3", 2, at)
	if err := m.DeclareCareCompletion(ctx, stale, 1, entryKey("key-decl-03", "cc-3", at), []out.OutboxEvent{{EventID: "ev-x", EventType: "ClinicalCareCompleted", EventVersion: 1, AggregateID: "r", AggregateType: "ClinicalRecord", Payload: map[string]any{}, OccurredAt: at}}); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("stale record: got %v", err)
	}
	if _, err := m.GetClosure(ctx, "cc-3"); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("a rejected declaration must roll back its closure")
	}
	if n, _ := m.db.Collection("outbox_events").CountDocuments(ctx, bson.D{{Key: "_id", Value: "ev-x"}}); n != 0 {
		t.Fatal("a rejected declaration must roll back its event")
	}
}

func TestMongoRetryClosureRetransmitsTheOriginalEvent(t *testing.T) {
	m := newTestMongo(t)
	for _, c := range []string{"care_closures", "outbox_events"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 23, 10, 0, 0, 0, time.UTC)
	_ = m.CreateRecord(ctx, rec("r-1", "p1", at), entryKey("key-rec-01", "r-1", at))
	c := closure("cc-1", "r-1", "c-1", 2, at)
	ev := out.OutboxEvent{EventID: "ev-decl", EventType: "ClinicalCareCompleted", EventVersion: 1, AggregateID: "r-1", AggregateType: "ClinicalRecord",
		ActorID: "d", Payload: map[string]any{"consultationId": "c-1"}, OccurredAt: at}
	if err := m.DeclareCareCompletion(ctx, c, 1, entryKey("key-decl-01", "cc-1", at), []out.OutboxEvent{ev}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.Collection("outbox_events").UpdateOne(ctx, bson.D{{Key: "_id", Value: "ev-decl"}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "FAILED"}, {Key: "attempts", Value: 5}, {Key: "failureReason", Value: "broker"}}}}); err != nil {
		t.Fatal(err)
	}
	failed, _ := m.GetClosure(ctx, "cc-1")
	failed.MarkFailed("broker retries exhausted", at)
	resumed := failed
	if err := resumed.Retry(2, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := m.RetryClosure(ctx, resumed, 1, entryKey("key-retry-01", "cc-1", at)); err != nil {
		t.Fatalf("rejected by the real validators: %v", err)
	}
	got, _ := m.GetClosure(ctx, "cc-1")
	if got.Version != 3 || got.Status != model.ClosureAwaitingFinalization || got.FailureReason != "" {
		t.Fatalf("closure = %+v", got)
	}
	var row bson.M
	_ = m.db.Collection("outbox_events").FindOne(ctx, bson.D{{Key: "_id", Value: "ev-decl"}}).Decode(&row)
	if row["status"] != "PENDING" || row["attempts"] != int32(0) || row["failureReason"] != nil {
		t.Fatalf("the original event must be pending again with its id: %v", row)
	}
	if n, _ := m.db.Collection("outbox_events").CountDocuments(ctx, bson.D{}); n != 1 {
		t.Fatalf("a retry never adds events, got %d", n)
	}
	if err := m.RetryClosure(ctx, resumed, 1, entryKey("key-retry-02", "cc-1", at)); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("stale retry: got %v", err)
	}
}
