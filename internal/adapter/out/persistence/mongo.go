package persistence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// MongoOptions are the explicit limits of the Clinical database connection.
type MongoOptions struct {
	URI         string
	Database    string
	MaxPoolSize int
	Timeout     time.Duration // connect, pool wait and per-operation deadline
}

// Mongo is the production RecordRepository over the schema owned by
// dlc-clinical-db. It never creates collections, validators or indexes.
type Mongo struct {
	client *mongo.Client
	db     *mongo.Database
}

var _ out.RecordRepository = (*Mongo)(nil)

// OpenMongo connects with a bounded pool and deadlines, and verifies the primary.
func OpenMongo(ctx context.Context, o MongoOptions) (*Mongo, error) {
	client, err := mongo.Connect(options.Client().ApplyURI(o.URI).
		SetMaxPoolSize(uint64(o.MaxPoolSize)).
		SetConnectTimeout(o.Timeout).
		SetServerSelectionTimeout(o.Timeout).
		SetTimeout(o.Timeout))
	if err != nil {
		return nil, fmt.Errorf("mongo connect: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, o.Timeout)
	defer cancel()
	if err := client.Ping(pingCtx, readpref.Primary()); err != nil {
		_ = client.Disconnect(ctx)
		return nil, fmt.Errorf("mongo ping: %w", err)
	}
	return &Mongo{client: client, db: client.Database(o.Database)}, nil
}

// Close releases the pool during graceful shutdown.
func (m *Mongo) Close(ctx context.Context) error { return m.client.Disconnect(ctx) }

type recordDoc struct {
	ID               string    `bson:"_id"`
	ClinicalRecordID string    `bson:"clinicalRecordId"`
	PatientID        string    `bson:"patientId"`
	Version          int       `bson:"version"`
	CreatedAt        time.Time `bson:"createdAt"`
	UpdatedAt        time.Time `bson:"updatedAt"`
	CreatedBy        string    `bson:"createdBy"`
}

func (d recordDoc) model() model.ClinicalRecord {
	return model.ClinicalRecord{ID: d.ID, PatientID: d.PatientID, CreatedBy: d.CreatedBy, CreatedAt: d.CreatedAt.UTC(), UpdatedAt: d.UpdatedAt.UTC(), Version: d.Version}
}

type idempotencyDoc struct {
	ID             string    `bson:"_id"`
	ActorID        string    `bson:"actorId"`
	Operation      string    `bson:"operation"`
	Key            string    `bson:"idempotencyKey"`
	RequestHash    string    `bson:"requestHash"`
	ResourceID     string    `bson:"resourceId"`
	ResponseStatus int       `bson:"responseStatus"`
	CreatedAt      time.Time `bson:"createdAt"`
	ExpiresAt      time.Time `bson:"expiresAt"`
}

func idempotencyID(actor, operation, key string) string { return actor + "|" + operation + "|" + key }

func (m *Mongo) FindIdempotency(ctx context.Context, actor, operation, key string) (out.IdempotencyRecord, bool, error) {
	var d idempotencyDoc
	err := m.db.Collection("idempotency_keys").FindOne(ctx, bson.D{{Key: "_id", Value: idempotencyID(actor, operation, key)}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return out.IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return out.IdempotencyRecord{}, false, err
	}
	return out.IdempotencyRecord{
		ActorID: d.ActorID, Operation: d.Operation, Key: d.Key, RequestHash: d.RequestHash, ResourceID: d.ResourceID,
		ResponseStatus: d.ResponseStatus, CreatedAt: d.CreatedAt.UTC(), ExpiresAt: d.ExpiresAt.UTC(),
	}, true, nil
}

// CreateRecord inserts the key and the record in one majority transaction,
// so a crash or a duplicate can never leave one without the other.
func (m *Mongo) CreateRecord(ctx context.Context, rec model.ClinicalRecord, idem out.IdempotencyRecord) error {
	session, err := m.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		if err := m.insertIdempotency(tx, idem); err != nil {
			return nil, err
		}
		if _, err := m.db.Collection("clinical_records").InsertOne(tx, recordDoc{
			ID: rec.ID, ClinicalRecordID: rec.ID, PatientID: rec.PatientID, Version: rec.Version,
			CreatedAt: rec.CreatedAt, UpdatedAt: rec.UpdatedAt, CreatedBy: rec.CreatedBy,
		}); err != nil {
			if mongo.IsDuplicateKeyError(err) && strings.Contains(err.Error(), "uq_clinical_records_patient_id") {
				return nil, out.ErrRecordExists
			}
			return nil, err
		}
		return nil, nil
	}, options.Transaction().SetWriteConcern(writeconcern.Majority()))
	return err
}

func (m *Mongo) GetRecord(ctx context.Context, id string) (model.ClinicalRecord, error) {
	var d recordDoc
	err := m.db.Collection("clinical_records").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return model.ClinicalRecord{}, out.ErrNotFound
	}
	if err != nil {
		return model.ClinicalRecord{}, err
	}
	return d.model(), nil
}

// ListRecords counts and pages within the same filter, newest first, ties by id.
func (m *Mongo) ListRecords(ctx context.Context, f out.RecordFilter) ([]model.ClinicalRecord, int, error) {
	filter := bson.D{}
	if f.PatientIDs != nil {
		filter = bson.D{{Key: "patientId", Value: bson.D{{Key: "$in", Value: f.PatientIDs}}}}
	}
	coll := m.db.Collection("clinical_records")
	total, err := coll.CountDocuments(ctx, filter)
	if err != nil {
		return nil, 0, err
	}
	cur, err := coll.Find(ctx, filter, options.Find().
		SetSort(bson.D{{Key: "createdAt", Value: -1}, {Key: "_id", Value: -1}}).
		SetSkip(int64(f.Offset)).SetLimit(int64(f.Limit)))
	if err != nil {
		return nil, 0, err
	}
	var docs []recordDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, 0, err
	}
	items := make([]model.ClinicalRecord, 0, len(docs))
	for _, d := range docs {
		items = append(items, d.model())
	}
	return items, int(total), nil
}

// insertIdempotency stores the retry record inside the caller's transaction;
// a duplicate scope means another request with the same key won.
func (m *Mongo) insertIdempotency(tx context.Context, idem out.IdempotencyRecord) error {
	_, err := m.db.Collection("idempotency_keys").InsertOne(tx, idempotencyDoc{
		ID: idempotencyID(idem.ActorID, idem.Operation, idem.Key), ActorID: idem.ActorID, Operation: idem.Operation,
		Key: idem.Key, RequestHash: idem.RequestHash, ResourceID: idem.ResourceID, ResponseStatus: idem.ResponseStatus,
		CreatedAt: idem.CreatedAt, ExpiresAt: idem.ExpiresAt,
	})
	if mongo.IsDuplicateKeyError(err) {
		return out.ErrDuplicateIdempotency
	}
	return err
}
