package persistence

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var _ out.ProcedureRepository = (*Mongo)(nil)

type procedureDoc struct {
	ID            string    `bson:"_id"`
	TreatmentID   string    `bson:"treatmentId"`
	RecordID      string    `bson:"clinicalRecordId"`
	PatientID     string    `bson:"patientId"`
	AppointmentID string    `bson:"appointmentId"`
	Code          string    `bson:"procedureCode"`
	Status        string    `bson:"status"`
	CompletedBy   string    `bson:"completedBy"`
	CompletedAt   time.Time `bson:"completedAt"`
	Version       int       `bson:"version"`
}

func (d procedureDoc) model() model.Procedure {
	p := model.Procedure{ID: d.ID, TreatmentID: d.TreatmentID, RecordID: d.RecordID, PatientID: d.PatientID, Code: d.Code,
		AppointmentID: d.AppointmentID, Status: model.Status(d.Status), Version: d.Version, CompletedBy: d.CompletedBy}
	if !d.CompletedAt.IsZero() {
		p.CompletedAt = d.CompletedAt.UTC()
	}
	return p
}

func (m *Mongo) GetProcedure(ctx context.Context, id string) (model.Procedure, error) {
	var d procedureDoc
	err := m.db.Collection("procedures").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return model.Procedure{}, out.ErrNotFound
	}
	if err != nil {
		return model.Procedure{}, err
	}
	return d.model(), nil
}

// extraDocument stores the quantity as decimal128 (exact, never double) and
// no money at all (CLN-006); sourceRecordId is unique by index.
func extraDocument(e out.RecordedExtra) (bson.D, error) {
	q, err := bson.ParseDecimal128(e.Quantity)
	if err != nil {
		return nil, fmt.Errorf("quantity %q: %w", e.Quantity, err)
	}
	d := bson.D{{Key: "_id", Value: e.SourceRecordID}, {Key: "sourceRecordId", Value: e.SourceRecordID}, {Key: "procedureId", Value: e.ProcedureID},
		{Key: "appointmentId", Value: e.AppointmentID}, {Key: "type", Value: string(e.Type)}}
	if e.Code != "" {
		d = append(d, bson.E{Key: "code", Value: e.Code})
	}
	return append(d, bson.E{Key: "quantity", Value: q}, bson.E{Key: "description", Value: e.Description},
		bson.E{Key: "clinicalReason", Value: e.ClinicalReason}, bson.E{Key: "authorId", Value: e.AuthorID}, bson.E{Key: "createdAt", Value: e.CreatedAt}), nil
}

// CompleteProcedure writes key, procedure progress, the treatment view, the
// extras and ProcedureCompleted in one majority transaction; the procedure is
// updated only while its stored version is fromVersion.
func (m *Mongo) CompleteProcedure(ctx context.Context, p model.Procedure, fromVersion int, extras []out.RecordedExtra, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	docs := make([]any, 0, len(extras))
	for _, e := range extras {
		d, err := extraDocument(e)
		if err != nil {
			return err
		}
		docs = append(docs, d)
	}
	return m.transact(ctx, func(tx context.Context) error {
		if err := m.insertIdempotency(tx, idem); err != nil {
			return err
		}
		res, err := m.db.Collection("procedures").UpdateOne(tx,
			bson.D{{Key: "_id", Value: p.ID}, {Key: "version", Value: fromVersion}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: string(p.Status)}, {Key: "completedBy", Value: p.CompletedBy},
				{Key: "completedAt", Value: p.CompletedAt}, {Key: "updatedAt", Value: p.CompletedAt}, {Key: "version", Value: p.Version}}}})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return out.ErrVersionConflict
		}
		if _, err := m.db.Collection("treatments").UpdateOne(tx,
			bson.D{{Key: "_id", Value: p.TreatmentID}, {Key: "procedures.procedureId", Value: p.ID}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "procedures.$.status", Value: string(p.Status)}, {Key: "updatedAt", Value: p.CompletedAt}}},
				{Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}}}); err != nil {
			return err
		}
		if len(docs) > 0 {
			if _, err := m.db.Collection("procedure_extras").InsertMany(tx, docs); err != nil {
				if mongo.IsDuplicateKeyError(err) {
					return out.ErrDuplicateSource
				}
				return err
			}
		}
		for _, ev := range events {
			if _, err := m.db.Collection("outbox_events").InsertOne(tx, outboxDocument(ev, p.CompletedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}
