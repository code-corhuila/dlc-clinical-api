package persistence

import (
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func receipt(id, status string, at time.Time) out.InboxRecord {
	return out.InboxRecord{EventID: id, EventType: "AppointmentCompleted", EventVersion: "1", AggregateID: "a", CorrelationID: "corr",
		Payload: map[string]any{"appointmentId": "a"}, Status: status, ReceivedAt: at}
}

func TestMongoInboxAppliesEachEventOnce(t *testing.T) {
	m := newTestMongo(t)
	for _, c := range []string{"care_closures", "outbox_events", "inbox_events"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
	at := time.Date(2026, 10, 23, 11, 0, 0, 0, time.UTC)
	_ = m.CreateRecord(ctx, rec("r-1", "p1", at), entryKey("key-rec-01", "r-1", at))
	if err := m.DeclareCareCompletion(ctx, closure("cc-1", "r-1", "c-1", 2, at), 1, entryKey("key-decl-01", "cc-1", at), nil); err != nil {
		t.Fatal(err)
	}
	found, err := m.FindClosuresByAppointment(ctx, "a")
	if err != nil || len(found) != 1 || found[0].ID != "cc-1" {
		t.Fatalf("closures by appointment = %+v %v", found, err)
	}
	updated := found[0]
	updated.RecordAppointmentCompleted(at)
	if err := m.Apply(ctx, receipt("ev-1", "PROCESSED", at), []out.ClosureUpdate{{Closure: updated, FromVersion: 1}}); err != nil {
		t.Fatalf("rejected by the real validators: %v", err)
	}
	if got, _ := m.GetClosure(ctx, "cc-1"); got.Status != model.ClosurePending || got.Version != 2 {
		t.Fatalf("closure = %+v", got)
	}
	if seen, _ := m.Received(ctx, "ev-1"); !seen {
		t.Fatal("the receipt must be stored")
	}
	if err := m.Apply(ctx, receipt("ev-1", "PROCESSED", at), nil); !errors.Is(err, out.ErrDuplicateEvent) {
		t.Fatalf("redelivery: got %v", err)
	}
	if err := m.Apply(ctx, receipt("ev-2", "PROCESSED", at), []out.ClosureUpdate{{Closure: updated, FromVersion: 1}}); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("moved closure: got %v", err)
	}
	if seen, _ := m.Received(ctx, "ev-2"); seen {
		t.Fatal("a conflicting receipt must roll back so its redelivery can apply")
	}
	failed := receipt("ev-3", "FAILED", at)
	failed.FailureReason = "patient does not match the declared encounter"
	if err := m.Apply(ctx, failed, nil); err != nil {
		t.Fatal(err)
	}
	var row bson.M
	_ = m.db.Collection("inbox_events").FindOne(ctx, bson.D{{Key: "_id", Value: "ev-3"}}).Decode(&row)
	if row["status"] != "FAILED" || row["failureReason"] == nil || row["processedAt"] == nil {
		t.Fatalf("failed receipt = %v", row)
	}
}
