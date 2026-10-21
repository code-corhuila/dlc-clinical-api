package persistence

import (
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func plannedTreatment(t *testing.T, m *Mongo, at time.Time) model.Treatment {
	t.Helper()
	tr := treatment("t-1", "p-1", at)
	tr.Procedures[0].RecordID, tr.Procedures[0].PatientID, tr.Procedures[0].Version = tr.RecordID, tr.PatientID, 1
	if err := m.CreateTreatment(ctx, tr, entryKey("key-plan-01", "t-1", at), nil); err != nil {
		t.Fatal(err)
	}
	return tr
}

func recordedExtra(src string, at time.Time) out.RecordedExtra {
	return out.RecordedExtra{Extra: model.Extra{SourceRecordID: src, Type: model.ExtraAdditionalMaterial, Code: "RES-1", Quantity: "1.2500",
		Description: "Resina", ClinicalReason: "Fractura"}, ProcedureID: "p-t-1", AppointmentID: "a", AuthorID: "d", CreatedAt: at}
}

func TestMongoCompleteProcedureWritesEverythingOnce(t *testing.T) {
	m := newTestMongo(t)
	cleanTreatments(t, m)
	if _, err := m.db.Collection("procedure_extras").DeleteMany(ctx, bson.D{}); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 10, 21, 13, 0, 0, 0, time.UTC)
	plannedTreatment(t, m, at)

	p, err := m.GetProcedure(ctx, "p-t-1")
	if err != nil || p.TreatmentID != "t-1" || p.RecordID != "r-p-1" || p.PatientID != "p-1" || p.Version != 1 || p.Status != model.StatusPlanned {
		t.Fatalf("got %+v %v", p, err)
	}
	done := p
	done.Status, done.Version, done.CompletedBy, done.CompletedAt = model.StatusCompleted, 2, "d", at.Add(time.Minute)
	ev := out.OutboxEvent{EventID: "ev-pc", EventType: "ProcedureCompleted", EventVersion: 1, AggregateID: "t-1", AggregateType: "Treatment",
		ActorID: "d", Payload: map[string]any{"procedureId": "p-t-1"}, OccurredAt: at}
	if err := m.CompleteProcedure(ctx, done, 1, []out.RecordedExtra{recordedExtra("src-1", at)}, entryKey("key-done-01", "p-t-1", at), []out.OutboxEvent{ev}); err != nil {
		t.Fatalf("rejected by the real validators: %v", err)
	}
	if got, _ := m.GetProcedure(ctx, "p-t-1"); got.Status != model.StatusCompleted || got.Version != 2 || got.CompletedBy != "d" || !got.CompletedAt.Equal(done.CompletedAt) {
		t.Fatalf("after completion %+v", got)
	}
	if tr, _ := m.GetTreatment(ctx, "t-1"); tr.Procedures[0].Status != model.StatusCompleted || tr.Version != 2 {
		t.Fatalf("treatment view %+v", tr)
	}
	var extra bson.M
	if err := m.db.Collection("procedure_extras").FindOne(ctx, bson.D{{Key: "_id", Value: "src-1"}}).Decode(&extra); err != nil {
		t.Fatal(err)
	}
	if q, ok := extra["quantity"].(bson.Decimal128); !ok || q.String() != "1.2500" || extra["procedureId"] != "p-t-1" || extra["authorId"] != "d" {
		t.Fatalf("extra = %v (quantity %T)", extra, extra["quantity"])
	}

	if err := m.CompleteProcedure(ctx, done, 1, nil, entryKey("key-done-02", "p-t-1", at), nil); !errors.Is(err, out.ErrVersionConflict) {
		t.Fatalf("stale completion: got %v", err)
	}
	again := done
	again.Version = 3
	err = m.CompleteProcedure(ctx, again, 2, []out.RecordedExtra{recordedExtra("src-1", at)}, entryKey("key-done-03", "p-t-1", at), []out.OutboxEvent{{EventID: "ev-x", EventType: "ProcedureCompleted", EventVersion: 1, AggregateID: "t-1", AggregateType: "Treatment", Payload: map[string]any{}, OccurredAt: at}})
	if !errors.Is(err, out.ErrDuplicateSource) {
		t.Fatalf("reused source: got %v", err)
	}
	if got, _ := m.GetProcedure(ctx, "p-t-1"); got.Version != 2 {
		t.Fatal("a rejected completion must roll back the procedure update")
	}
	if n, _ := m.db.Collection("outbox_events").CountDocuments(ctx, bson.D{{Key: "_id", Value: "ev-x"}}); n != 0 {
		t.Fatal("a rejected completion must roll back its event")
	}
	if _, err := m.GetProcedure(ctx, "missing"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
