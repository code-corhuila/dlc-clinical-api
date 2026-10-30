package persistence

import (
	"context"
	"errors"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/mongo/writeconcern"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var _ out.EntryRepository = (*Mongo)(nil)

// Entries live in the collections of data-model.md: consultations,
// diagnoses and clinical_entries (evolution, antecedent, allergy).
var entryCollections = []struct {
	name string
	kind model.EntryKind // empty: the document carries its own kind
}{
	{"consultations", model.KindConsultation},
	{"diagnoses", model.KindDiagnosis},
	{"clinical_entries", ""},
}

func collectionFor(k model.EntryKind) string {
	switch k {
	case model.KindConsultation:
		return "consultations"
	case model.KindDiagnosis:
		return "diagnoses"
	}
	return "clinical_entries"
}

// entryDocument maps an entry to the exact fields its collection validator declares.
func entryDocument(e model.Entry) bson.D {
	d := bson.D{{Key: "_id", Value: e.ID}}
	switch e.Kind {
	case model.KindConsultation:
		d = append(d, bson.E{Key: "consultationId", Value: e.ID}, bson.E{Key: "dentistId", Value: e.AuthorID})
	case model.KindDiagnosis:
		d = append(d, bson.E{Key: "diagnosisId", Value: e.ID}, bson.E{Key: "dentistId", Value: e.AuthorID}, bson.E{Key: "consultationId", Value: e.ConsultationID})
	default:
		d = append(d, bson.E{Key: "entryId", Value: e.ID}, bson.E{Key: "kind", Value: string(e.Kind)}, bson.E{Key: "authorId", Value: e.AuthorID})
	}
	d = append(d, bson.E{Key: "clinicalRecordId", Value: e.RecordID}, bson.E{Key: "text", Value: e.Text})
	for _, opt := range []struct{ k, v string }{{"code", e.Code}, {"appointmentId", e.AppointmentID}, {"amendsEntryId", e.AmendsEntryID}, {"amendmentReason", e.AmendmentReason}} {
		if opt.v != "" {
			d = append(d, bson.E{Key: opt.k, Value: opt.v})
		}
	}
	return append(d, bson.E{Key: "createdAt", Value: e.CreatedAt}, bson.E{Key: "updatedAt", Value: e.UpdatedAt}, bson.E{Key: "version", Value: e.Version})
}

// storedEntry reads any of the three collections; Kind is injected for the
// collections that do not store it.
type storedEntry struct {
	ID              string    `bson:"_id"`
	RecordID        string    `bson:"clinicalRecordId"`
	Kind            string    `bson:"kind"`
	Text            string    `bson:"text"`
	Code            string    `bson:"code"`
	AppointmentID   string    `bson:"appointmentId"`
	ConsultationID  string    `bson:"consultationId"`
	AuthorID        string    `bson:"authorId"`
	DentistID       string    `bson:"dentistId"`
	AmendsEntryID   string    `bson:"amendsEntryId"`
	AmendmentReason string    `bson:"amendmentReason"`
	CreatedAt       time.Time `bson:"createdAt"`
	UpdatedAt       time.Time `bson:"updatedAt"`
	Version         int       `bson:"version"`
}

func (s storedEntry) model() model.Entry {
	e := model.Entry{ID: s.ID, RecordID: s.RecordID, Kind: model.EntryKind(s.Kind), Text: s.Text, Code: s.Code,
		AppointmentID: s.AppointmentID, AuthorID: s.AuthorID, AmendsEntryID: s.AmendsEntryID, AmendmentReason: s.AmendmentReason,
		CreatedAt: s.CreatedAt.UTC(), UpdatedAt: s.UpdatedAt.UTC(), Version: s.Version}
	if e.AuthorID == "" {
		e.AuthorID = s.DentistID
	}
	if e.Kind == model.KindDiagnosis {
		e.ConsultationID = s.ConsultationID // a consultation's own consultationId is its id, not a reference
	}
	return e
}

func outboxDocument(ev out.OutboxEvent, now time.Time) bson.D {
	causation := ev.CausationID
	if causation == "" {
		causation = ev.EventID
	}
	correlation := ev.CorrelationID
	if correlation == "" {
		correlation = ev.EventID
	}
	return bson.D{
		{Key: "_id", Value: ev.EventID}, {Key: "eventId", Value: ev.EventID}, {Key: "eventType", Value: ev.EventType},
		{Key: "eventVersion", Value: strconv.Itoa(ev.EventVersion)}, {Key: "aggregateId", Value: ev.AggregateID},
		{Key: "aggregateType", Value: ev.AggregateType}, {Key: "actorUserId", Value: ev.ActorID},
		{Key: "correlationId", Value: correlation}, {Key: "causationId", Value: causation}, {Key: "payload", Value: ev.Payload},
		{Key: "occurredAt", Value: ev.OccurredAt}, {Key: "status", Value: "PENDING"}, {Key: "attempts", Value: 0}, {Key: "createdAt", Value: now},
	}
}

// CreateEntry writes key, entry and outbox events in one majority transaction.
func (m *Mongo) CreateEntry(ctx context.Context, e model.Entry, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	session, err := m.client.StartSession()
	if err != nil {
		return err
	}
	defer session.EndSession(ctx)
	_, err = session.WithTransaction(ctx, func(tx context.Context) (any, error) {
		if err := m.recordWrite(tx, idem); err != nil {
			return nil, err
		}
		if _, err := m.db.Collection(collectionFor(e.Kind)).InsertOne(tx, entryDocument(e)); err != nil {
			return nil, err
		}
		for _, ev := range events {
			if _, err := m.db.Collection("outbox_events").InsertOne(tx, outboxDocument(ev, e.CreatedAt)); err != nil {
				return nil, err
			}
		}
		return nil, nil
	}, options.Transaction().SetWriteConcern(writeconcern.Majority()))
	return err
}

func (m *Mongo) GetEntry(ctx context.Context, id string) (model.Entry, error) {
	for _, c := range entryCollections {
		var s storedEntry
		err := m.db.Collection(c.name).FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&s)
		if errors.Is(err, mongo.ErrNoDocuments) {
			continue
		}
		if err != nil {
			return model.Entry{}, err
		}
		if c.kind != "" {
			s.Kind = string(c.kind)
		}
		return s.model(), nil
	}
	return model.Entry{}, out.ErrNotFound
}

// ListEntries unifies the three collections of a record in one stable order
// (createdAt asc, _id asc) and counts them in the same aggregation.
func (m *Mongo) ListEntries(ctx context.Context, recordID string, offset, limit int) ([]model.Entry, int, error) {
	match := bson.D{{Key: "$match", Value: bson.D{{Key: "clinicalRecordId", Value: recordID}}}}
	union := func(coll string, kind model.EntryKind) bson.D {
		return bson.D{{Key: "$unionWith", Value: bson.D{{Key: "coll", Value: coll}, {Key: "pipeline", Value: bson.A{
			match, bson.D{{Key: "$addFields", Value: bson.D{{Key: "kind", Value: string(kind)}}}},
		}}}}}
	}
	pipeline := mongo.Pipeline{
		match, union("consultations", model.KindConsultation), union("diagnoses", model.KindDiagnosis),
		{{Key: "$sort", Value: bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}}},
		{{Key: "$facet", Value: bson.D{
			{Key: "items", Value: bson.A{bson.D{{Key: "$skip", Value: offset}}, bson.D{{Key: "$limit", Value: limit}}}},
			{Key: "total", Value: bson.A{bson.D{{Key: "$count", Value: "n"}}}},
		}}},
	}
	cur, err := m.db.Collection("clinical_entries").Aggregate(ctx, pipeline)
	if err != nil {
		return nil, 0, err
	}
	var result []struct {
		Items []storedEntry `bson:"items"`
		Total []struct {
			N int `bson:"n"`
		} `bson:"total"`
	}
	if err := cur.All(ctx, &result); err != nil {
		return nil, 0, err
	}
	items := []model.Entry{}
	total := 0
	if len(result) == 1 {
		for _, s := range result[0].Items {
			items = append(items, s.model())
		}
		if len(result[0].Total) == 1 {
			total = result[0].Total[0].N
		}
	}
	return items, total, nil
}

func (m *Mongo) ConsultationForAppointment(ctx context.Context, recordID, appointmentID string) (string, bool, error) {
	var d struct {
		ID string `bson:"_id"`
	}
	err := m.db.Collection("consultations").FindOne(ctx,
		bson.D{{Key: "clinicalRecordId", Value: recordID}, {Key: "appointmentId", Value: appointmentID}},
		options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}}).SetProjection(bson.D{{Key: "_id", Value: 1}})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return "", false, nil
	}
	return d.ID, err == nil, err
}
