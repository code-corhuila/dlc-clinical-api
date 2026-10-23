package persistence

import (
	"context"
	"errors"
	"slices"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var _ out.ClosureRepository = (*Mongo)(nil)

// closureDoc mirrors the care_closures validator (ADR-014).
type closureDoc struct {
	ID                 string    `bson:"_id"`
	ClosureID          string    `bson:"closureId"`
	RecordID           string    `bson:"clinicalRecordId"`
	ConsultationID     string    `bson:"consultationId"`
	AppointmentID      string    `bson:"appointmentId"`
	PatientID          string    `bson:"patientId"`
	DentistID          string    `bson:"dentistId"`
	ProcedureIDs       []string  `bson:"procedureIds"`
	ManualChargeIDs    []string  `bson:"manualChargeIds"`
	Status             string    `bson:"status"`
	AppointmentOutcome string    `bson:"appointmentOutcome"`
	BillingOutcome     string    `bson:"billingOutcome"`
	FailureReason      string    `bson:"failureReason,omitempty"`
	CorrelationID      string    `bson:"correlationId"`
	CausationID        string    `bson:"causationId"`
	CompletedAt        time.Time `bson:"completedAt"`
	ClinicalVersion    int       `bson:"clinicalVersion"`
	CreatedAt          time.Time `bson:"createdAt"`
	UpdatedAt          time.Time `bson:"updatedAt"`
	Version            int       `bson:"version"`
}

func toClosureDoc(c model.CareClosure) closureDoc {
	correlation := c.CorrelationID
	if correlation == "" {
		correlation = c.ID
	}
	return closureDoc{ID: c.ID, ClosureID: c.ID, RecordID: c.RecordID, ConsultationID: c.ConsultationID, AppointmentID: c.AppointmentID,
		PatientID: c.PatientID, DentistID: c.DentistID, ProcedureIDs: nonNilIDs(c.ProcedureIDs), ManualChargeIDs: nonNilIDs(c.ManualChargeIDs),
		Status: string(c.Status), AppointmentOutcome: string(c.AppointmentOutcome), BillingOutcome: string(c.BillingOutcome),
		FailureReason: c.FailureReason, CorrelationID: correlation, CausationID: correlation, CompletedAt: c.CompletedAt,
		ClinicalVersion: c.ClinicalVersion, CreatedAt: c.CreatedAt, UpdatedAt: c.UpdatedAt, Version: c.Version}
}

func (d closureDoc) model() model.CareClosure {
	return model.CareClosure{ID: d.ID, RecordID: d.RecordID, ConsultationID: d.ConsultationID, AppointmentID: d.AppointmentID,
		PatientID: d.PatientID, DentistID: d.DentistID, ProcedureIDs: nonNilIDs(d.ProcedureIDs), ManualChargeIDs: nonNilIDs(d.ManualChargeIDs),
		Status: model.ClosureStatus(d.Status), AppointmentOutcome: model.Outcome(d.AppointmentOutcome), BillingOutcome: model.Outcome(d.BillingOutcome),
		FailureReason: d.FailureReason, CorrelationID: d.CorrelationID, CompletedAt: d.CompletedAt.UTC(), ClinicalVersion: d.ClinicalVersion,
		CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), Version: d.Version}
}

func nonNilIDs(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

func (m *Mongo) findClosure(ctx context.Context, filter bson.D) (model.CareClosure, bool, error) {
	var d closureDoc
	err := m.db.Collection("care_closures").FindOne(ctx, filter).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return model.CareClosure{}, false, nil
	}
	if err != nil {
		return model.CareClosure{}, false, err
	}
	return d.model(), true, nil
}

func (m *Mongo) GetClosure(ctx context.Context, id string) (model.CareClosure, error) {
	c, ok, err := m.findClosure(ctx, bson.D{{Key: "_id", Value: id}})
	if err == nil && !ok {
		return model.CareClosure{}, out.ErrNotFound
	}
	return c, err
}

func (m *Mongo) FindClosureByEncounter(ctx context.Context, recordID, consultationID string) (model.CareClosure, bool, error) {
	return m.findClosure(ctx, bson.D{{Key: "clinicalRecordId", Value: recordID}, {Key: "consultationId", Value: consultationID}})
}

// CompletedProcedureIDs reads Clinical's own procedures of the encounter.
func (m *Mongo) CompletedProcedureIDs(ctx context.Context, recordID, appointmentID string) ([]string, error) {
	cur, err := m.db.Collection("procedures").Find(ctx,
		bson.D{{Key: "clinicalRecordId", Value: recordID}, {Key: "appointmentId", Value: appointmentID}, {Key: "status", Value: string(model.StatusCompleted)}},
		options.Find().SetProjection(bson.D{{Key: "_id", Value: 1}}).SetLimit(maxDeclaredProcedures+1))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	return ids, nil
}

// maxDeclaredProcedures bounds the read; the domain rejects more than 100.
const maxDeclaredProcedures = 100

// DeclareCareCompletion inserts the encounter closure (unique per encounter),
// raises the record version only from recordFromVersion, and stores the key
// and ClinicalCareCompleted in one majority transaction.
func (m *Mongo) DeclareCareCompletion(ctx context.Context, c model.CareClosure, recordFromVersion int, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	return m.transact(ctx, func(tx context.Context) error {
		if err := m.recordWrite(tx, idem); err != nil {
			return err
		}
		if _, err := m.db.Collection("care_closures").InsertOne(tx, toClosureDoc(c)); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return out.ErrClosureExists
			}
			return err
		}
		res, err := m.db.Collection("clinical_records").UpdateOne(tx,
			bson.D{{Key: "_id", Value: c.RecordID}, {Key: "version", Value: recordFromVersion}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "version", Value: c.ClinicalVersion}, {Key: "updatedAt", Value: c.CompletedAt}}}})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return out.ErrVersionConflict
		}
		for _, ev := range events {
			if _, err := m.db.Collection("outbox_events").InsertOne(tx, outboxDocument(ev, c.CompletedAt)); err != nil {
				return err
			}
		}
		return nil
	})
}

// RetryClosure replaces the closure only from fromVersion and puts the
// encounter's original ClinicalCareCompleted back to PENDING with its eventId,
// in one majority transaction; no event is ever added by a retry.
func (m *Mongo) RetryClosure(ctx context.Context, c model.CareClosure, fromVersion int, idem out.IdempotencyRecord) error {
	return m.transact(ctx, func(tx context.Context) error {
		if err := m.insertIdempotency(tx, idem); err != nil {
			return err
		}
		res, err := m.db.Collection("care_closures").ReplaceOne(tx, bson.D{{Key: "_id", Value: c.ID}, {Key: "version", Value: fromVersion}}, toClosureDoc(c))
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return out.ErrVersionConflict
		}
		_, err = m.db.Collection("outbox_events").UpdateMany(tx,
			bson.D{{Key: "eventType", Value: "ClinicalCareCompleted"}, {Key: "aggregateId", Value: c.RecordID}, {Key: "payload.consultationId", Value: c.ConsultationID}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: "PENDING"}, {Key: "attempts", Value: 0}}},
				{Key: "$unset", Value: bson.D{{Key: "nextAttemptAt", Value: ""}, {Key: "failureReason", Value: ""}, {Key: "publishedAt", Value: ""}}}})
		return err
	})
}
