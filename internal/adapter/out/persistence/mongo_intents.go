package persistence

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

var (
	_ out.IntentStore    = (*Mongo)(nil)
	_ out.AdmissionStore = (*Mongo)(nil)
)

// Admission entry states of encounter_admissions (clinical-016).
const (
	admitted  = "ADMITTED"
	committed = "COMMITTED"
	aborted   = "ABORTED"
)

type intentDoc struct {
	ID               string     `bson:"_id"`
	OperationID      string     `bson:"operationId"`
	Kind             string     `bson:"kind"`
	PatientID        string     `bson:"patientId"`
	ClinicalRecordID string     `bson:"clinicalRecordId,omitempty"`
	ConsultationID   string     `bson:"consultationId,omitempty"`
	GuardID          string     `bson:"guardId,omitempty"`
	CommittedAt      *time.Time `bson:"committedAt,omitempty"`
	FencedAt         *time.Time `bson:"fencedAt,omitempty"`
	State            string     `bson:"state"`
	CreatedAt        time.Time  `bson:"createdAt"`
	UpdatedAt        time.Time  `bson:"updatedAt"`
}

func (d intentDoc) model() out.WriteIntent {
	return out.WriteIntent{OperationID: d.OperationID, Kind: d.Kind, PatientID: d.PatientID,
		Encounter: out.EncounterKey{RecordID: d.ClinicalRecordID, ConsultationID: d.ConsultationID},
		GuardID:   d.GuardID, Committed: d.CommittedAt != nil, Fenced: d.FencedAt != nil, State: d.State, CreatedAt: d.CreatedAt, UpdatedAt: d.UpdatedAt}
}

type admissionEntry struct {
	OperationID string `bson:"operationId"`
	State       string `bson:"state"`
}

type admissionDoc struct {
	ID               string           `bson:"_id"`
	ClinicalRecordID string           `bson:"clinicalRecordId"`
	ConsultationID   string           `bson:"consultationId"`
	AdmissionState   string           `bson:"admissionState"`
	FrozenBy         string           `bson:"frozenBy,omitempty"`
	Admissions       []admissionEntry `bson:"admissions"`
	CreatedAt        time.Time        `bson:"createdAt"`
	UpdatedAt        time.Time        `bson:"updatedAt"`
	Version          int              `bson:"version"`
}

func admissionID(k out.EncounterKey) string { return k.RecordID + "|" + k.ConsultationID }

func now() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

func (m *Mongo) intents() *mongo.Collection    { return m.db.Collection("write_intents") }
func (m *Mongo) admissions() *mongo.Collection { return m.db.Collection("encounter_admissions") }

func (m *Mongo) SaveIntent(ctx context.Context, w out.WriteIntent) error {
	return m.insertIntent(ctx, w)
}

func (m *Mongo) insertIntent(ctx context.Context, w out.WriteIntent) error {
	_, err := m.intents().InsertOne(ctx, intentDoc{ID: w.OperationID, OperationID: w.OperationID, Kind: w.Kind, PatientID: w.PatientID,
		ClinicalRecordID: w.Encounter.RecordID, ConsultationID: w.Encounter.ConsultationID, GuardID: w.GuardID,
		State: w.State, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt})
	if mongo.IsDuplicateKeyError(err) {
		return out.ErrDuplicateIdempotency
	}
	return err
}

func (m *Mongo) AttachGuard(ctx context.Context, operationID, guardID string) error {
	res, err := m.intents().UpdateOne(ctx, bson.D{{Key: "_id", Value: operationID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "guardId", Value: guardID}, {Key: "updatedAt", Value: now()}}}})
	if err == nil && res.MatchedCount == 0 {
		return out.ErrNotFound
	}
	return err
}

func (m *Mongo) FinishIntent(ctx context.Context, operationID, state string) error {
	res, err := m.intents().UpdateOne(ctx, bson.D{{Key: "_id", Value: operationID}, {Key: "state", Value: out.IntentPending}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "state", Value: state}, {Key: "updatedAt", Value: now()}}}})
	if err != nil || res.MatchedCount == 1 {
		return err
	}
	w, err := m.GetIntent(ctx, operationID)
	switch {
	case err != nil:
		return err
	case w.State == state:
		return nil
	}
	return out.ErrIntentSettled
}

func (m *Mongo) FenceIntent(ctx context.Context, operationID string) (bool, error) {
	_, err := m.intents().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: operationID}, {Key: "committedAt", Value: bson.D{{Key: "$exists", Value: false}}}, {Key: "fencedAt", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "fencedAt", Value: now()}, {Key: "updatedAt", Value: now()}}}})
	if err != nil {
		return false, err
	}
	w, err := m.GetIntent(ctx, operationID)
	return w.Committed, err
}

func (m *Mongo) GetIntent(ctx context.Context, operationID string) (out.WriteIntent, error) {
	var d intentDoc
	err := m.intents().FindOne(ctx, bson.D{{Key: "_id", Value: operationID}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return out.WriteIntent{}, out.ErrNotFound
	}
	return d.model(), err
}

func (m *Mongo) PendingIntents(ctx context.Context, limit int) ([]out.WriteIntent, error) {
	cur, err := m.intents().Find(ctx, bson.D{{Key: "state", Value: out.IntentPending}},
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var docs []intentDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	pending := make([]out.WriteIntent, 0, len(docs))
	for _, d := range docs {
		pending = append(pending, d.model())
	}
	return pending, nil
}

// findAdmission reads an encounter's admission document, or a new OPEN one.
func (m *Mongo) findAdmission(ctx context.Context, k out.EncounterKey) (admissionDoc, bool, error) {
	var d admissionDoc
	err := m.admissions().FindOne(ctx, bson.D{{Key: "_id", Value: admissionID(k)}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		at := now()
		return admissionDoc{ID: admissionID(k), ClinicalRecordID: k.RecordID, ConsultationID: k.ConsultationID, AdmissionState: "OPEN",
			Admissions: []admissionEntry{}, CreatedAt: at, UpdatedAt: at, Version: 1}, false, nil
	}
	return d, err == nil, err
}

// saveAdmission inserts a new document or replaces it from its read version,
// so a concurrent change is a write conflict that the transaction retries.
func (m *Mongo) saveAdmission(ctx context.Context, d admissionDoc, exists bool) error {
	if !exists {
		_, err := m.admissions().InsertOne(ctx, d)
		return err
	}
	from := d.Version
	d.Version, d.UpdatedAt = from+1, now()
	res, err := m.admissions().ReplaceOne(ctx, bson.D{{Key: "_id", Value: d.ID}, {Key: "version", Value: from}}, d)
	if err == nil && res.MatchedCount == 0 {
		return out.ErrVersionConflict
	}
	return err
}

func (m *Mongo) Admit(ctx context.Context, k out.EncounterKey, operationID string) error {
	return m.transact(ctx, func(tx context.Context) error {
		d, exists, err := m.findAdmission(tx, k)
		if err != nil {
			return err
		}
		if d.AdmissionState == "FROZEN" {
			return out.ErrEncounterFrozen
		}
		for _, a := range d.Admissions {
			if a.OperationID == operationID {
				return nil
			}
		}
		d.Admissions = append(d.Admissions, admissionEntry{OperationID: operationID, State: admitted})
		return m.saveAdmission(tx, d, exists)
	})
}

func (m *Mongo) AbortAdmission(ctx context.Context, k out.EncounterKey, operationID string) error {
	_, err := m.admissions().UpdateOne(ctx,
		bson.D{{Key: "_id", Value: admissionID(k)}, {Key: "admissions", Value: bson.D{{Key: "$elemMatch", Value: bson.D{{Key: "operationId", Value: operationID}, {Key: "state", Value: admitted}}}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "admissions.$.state", Value: aborted}, {Key: "updatedAt", Value: now()}}}, {Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}}})
	return err
}

func (m *Mongo) BeginDeclaration(ctx context.Context, w out.WriteIntent) (int, error) {
	pending := 0
	err := m.transact(ctx, func(tx context.Context) error {
		pending = 0
		d, exists, err := m.findAdmission(tx, w.Encounter)
		if err != nil {
			return err
		}
		if d.AdmissionState == "FROZEN" && d.FrozenBy != w.OperationID {
			return out.ErrEncounterFrozen
		}
		if err := m.insertIntent(tx, w); err != nil {
			return err
		}
		d.AdmissionState, d.FrozenBy = "FROZEN", w.OperationID
		for _, a := range d.Admissions {
			if a.State == admitted {
				pending++
			}
		}
		return m.saveAdmission(tx, d, exists)
	})
	return pending, err
}

func (m *Mongo) Unfreeze(ctx context.Context, k out.EncounterKey, operationID string) error {
	_, err := m.admissions().UpdateOne(ctx, bson.D{{Key: "_id", Value: admissionID(k)}, {Key: "frozenBy", Value: operationID}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "admissionState", Value: "OPEN"}, {Key: "updatedAt", Value: now()}}},
			{Key: "$unset", Value: bson.D{{Key: "frozenBy", Value: ""}}}, {Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}}})
	return err
}

// recordWrite stores the idempotency key and settles the write's tag inside
// the write's transaction (ADR-017 §3.4): the intent is marked committed and
// its admission COMMITTED, or the whole write is rejected.
func (m *Mongo) recordWrite(tx context.Context, idem out.IdempotencyRecord) error {
	if err := m.insertIdempotency(tx, idem); err != nil {
		return err
	}
	tag, ok := out.WriteTagFrom(tx)
	if !ok {
		return nil
	}
	set := bson.D{{Key: "committedAt", Value: now()}, {Key: "updatedAt", Value: now()}}
	if tag.Complete {
		set = append(set, bson.E{Key: "state", Value: out.IntentCompleted})
	}
	res, err := m.intents().UpdateOne(tx,
		bson.D{{Key: "_id", Value: tag.OperationID}, {Key: "state", Value: out.IntentPending}, {Key: "committedAt", Value: bson.D{{Key: "$exists", Value: false}}},
			{Key: "fencedAt", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "$set", Value: set}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return out.ErrIntentSettled
	}
	if tag.Encounter == nil {
		return nil
	}
	res, err = m.admissions().UpdateOne(tx,
		bson.D{{Key: "_id", Value: admissionID(*tag.Encounter)}, {Key: "admissions", Value: bson.D{{Key: "$elemMatch", Value: bson.D{{Key: "operationId", Value: tag.OperationID}, {Key: "state", Value: admitted}}}}}},
		bson.D{{Key: "$set", Value: bson.D{{Key: "admissions.$.state", Value: committed}, {Key: "updatedAt", Value: now()}}}, {Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return out.ErrIntentSettled
	}
	return nil
}
