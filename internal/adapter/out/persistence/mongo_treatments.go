package persistence

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var _ out.TreatmentRepository = (*Mongo)(nil)

type plannedProcedureDoc struct {
	ProcedureID   string `bson:"procedureId"`
	ProcedureCode string `bson:"procedureCode"`
	AppointmentID string `bson:"appointmentId"`
	Status        string `bson:"status"`
}

type treatmentDoc struct {
	ID             string                `bson:"_id"`
	TreatmentID    string                `bson:"treatmentId"`
	RecordID       string                `bson:"clinicalRecordId"`
	PatientID      string                `bson:"patientId"`
	DiagnosisID    string                `bson:"diagnosisId,omitempty"`
	ClinicalReason string                `bson:"clinicalReason,omitempty"`
	Status         string                `bson:"status"`
	Procedures     []plannedProcedureDoc `bson:"procedures"`
	CreatedBy      string                `bson:"createdBy"`
	CreatedAt      time.Time             `bson:"createdAt"`
	UpdatedAt      time.Time             `bson:"updatedAt"`
	Version        int                   `bson:"version"`
}

func toTreatmentDoc(t model.Treatment) treatmentDoc {
	procs := make([]plannedProcedureDoc, len(t.Procedures))
	for i, p := range t.Procedures {
		procs[i] = plannedProcedureDoc{ProcedureID: p.ID, ProcedureCode: p.Code, AppointmentID: p.AppointmentID, Status: string(p.Status)}
	}
	return treatmentDoc{ID: t.ID, TreatmentID: t.ID, RecordID: t.RecordID, PatientID: t.PatientID, DiagnosisID: t.DiagnosisID,
		ClinicalReason: t.ClinicalReason, Status: string(t.Status), Procedures: procs, CreatedBy: t.CreatedBy,
		CreatedAt: t.CreatedAt, UpdatedAt: t.UpdatedAt, Version: t.Version}
}

func (d treatmentDoc) model() model.Treatment {
	procs := make([]model.Procedure, len(d.Procedures))
	for i, p := range d.Procedures {
		procs[i] = model.Procedure{ID: p.ProcedureID, TreatmentID: d.ID, Code: p.ProcedureCode, AppointmentID: p.AppointmentID, Status: model.Status(p.Status)}
	}
	return model.Treatment{ID: d.ID, RecordID: d.RecordID, PatientID: d.PatientID, DiagnosisID: d.DiagnosisID, ClinicalReason: d.ClinicalReason,
		Status: model.Status(d.Status), Procedures: procs, CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), Version: d.Version}
}

// procedureDocuments are the per-procedure rows later completed one by one
// (ProcedureCompleted); money is never stored (CLN-006).
func procedureDocuments(t model.Treatment) []any {
	docs := make([]any, len(t.Procedures))
	for i, p := range t.Procedures {
		docs[i] = bson.D{
			{Key: "_id", Value: p.ID}, {Key: "procedureId", Value: p.ID}, {Key: "treatmentId", Value: t.ID},
			{Key: "clinicalRecordId", Value: t.RecordID}, {Key: "patientId", Value: t.PatientID},
			{Key: "appointmentId", Value: p.AppointmentID}, {Key: "procedureCode", Value: p.Code}, {Key: "status", Value: string(p.Status)},
			{Key: "createdAt", Value: t.CreatedAt}, {Key: "updatedAt", Value: t.UpdatedAt}, {Key: "version", Value: 1},
		}
	}
	return docs
}

// transact runs fn in one majority transaction.
func (m *Mongo) transact(ctx context.Context, fn func(tx context.Context) error) error {
	session, err := m.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) { return nil, fn(tx) },
		options.Transaction().SetWriteConcern(writeconcern.Majority()))
	return err
}

// CreateTreatment writes key, treatment, its procedures and events atomically.
func (m *Mongo) CreateTreatment(ctx context.Context, t model.Treatment, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	return m.transact(ctx, func(tx context.Context) error {
		if err := m.recordWrite(tx, idem); err != nil {
			return err
		}
		if _, err := m.db.Collection("treatments").InsertOne(tx, toTreatmentDoc(t)); err != nil {
			return err
		}
		if _, err := m.db.Collection("procedures").InsertMany(tx, procedureDocuments(t)); err != nil {
			return err
		}
		for _, ev := range events {
			if _, err := m.db.Collection("outbox_events").InsertOne(tx, outboxDocument(ev, t.CreatedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// UpdateTreatment replaces the treatment only while its stored version is fromVersion.
func (m *Mongo) UpdateTreatment(ctx context.Context, t model.Treatment, fromVersion int, idem out.IdempotencyRecord) error {
	return m.transact(ctx, func(tx context.Context) error {
		if err := m.insertIdempotency(tx, idem); err != nil {
			return err
		}
		res, err := m.db.Collection("treatments").ReplaceOne(tx, bson.D{{Key: "_id", Value: t.ID}, {Key: "version", Value: fromVersion}}, toTreatmentDoc(t))
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return out.ErrVersionConflict
		}
		return nil
	})
}

func (m *Mongo) GetTreatment(ctx context.Context, id string) (model.Treatment, error) {
	var d treatmentDoc
	err := m.db.Collection("treatments").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return model.Treatment{}, out.ErrNotFound
	}
	if err != nil {
		return model.Treatment{}, err
	}
	return d.model(), nil
}

// ListTreatments counts and pages within the same filter, oldest first, ties by id.
func (m *Mongo) ListTreatments(ctx context.Context, f out.RecordFilter) ([]model.Treatment, int, error) {
	filter := bson.D{}
	if f.PatientIDs != nil {
		filter = bson.D{{Key: "patientId", Value: bson.D{{Key: "$in", Value: f.PatientIDs}}}}
	}
	coll := m.db.Collection("treatments")
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	cur, err := coll.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).
		SetSkip(int64(f.Offset)).SetLimit(int64(f.Limit)))
	if err != nil {
		return nil, 0, err
	}
	var docs []treatmentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, 0, err
	}
	items := make([]model.Treatment, 0, len(docs))
	for _, d := range docs {
		items = append(items, d.model())
	}
	return items, int(total), nil
}
