package persistence

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var (
	_ out.InboxRepository = (*Memory)(nil)
	_ out.InboxRepository = (*Mongo)(nil)
)

// Received reports whether an eventId was already recorded.
func (m *Memory) Received(_ context.Context, eventID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.inbox[eventID]
	return ok, nil
}

// Apply stores the receipt and the closure updates together or none.
func (m *Memory) Apply(_ context.Context, r out.InboxRecord, updates []out.ClosureUpdate) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.inbox[r.EventID]; ok {
		return out.ErrDuplicateEvent
	}
	for _, u := range updates {
		if stored, ok := m.closure[u.Closure.ID]; !ok || stored.Version != u.FromVersion {
			return out.ErrVersionConflict
		}
	}
	for _, u := range updates {
		m.closure[u.Closure.ID] = cloneClosure(u.Closure)
	}
	m.inbox[r.EventID] = r
	return nil
}

func (m *Memory) FindClosuresByAppointment(_ context.Context, appointmentID string) ([]model.CareClosure, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found []model.CareClosure
	for _, c := range m.closure {
		if c.AppointmentID == appointmentID {
			found = append(found, cloneClosure(c))
		}
	}
	return found, nil
}

func (m *Mongo) Received(ctx context.Context, eventID string) (bool, error) {
	n, err := m.db.Collection("inbox_events").CountDocuments(ctx, bson.D{{Key: "_id", Value: eventID}})
	return n > 0, err
}

// Apply inserts the receipt (unique eventId) and replaces each closure only
// from its expected version, in one majority transaction.
func (m *Mongo) Apply(ctx context.Context, r out.InboxRecord, updates []out.ClosureUpdate) error {
	correlation, causation := r.CorrelationID, r.CausationID
	if correlation == "" {
		correlation = r.EventID
	}
	if causation == "" {
		causation = r.EventID
	}
	doc := bson.D{
		{Key: "_id", Value: r.EventID}, {Key: "eventId", Value: r.EventID}, {Key: "eventType", Value: r.EventType},
		{Key: "eventVersion", Value: r.EventVersion}, {Key: "aggregateId", Value: r.AggregateID},
		{Key: "correlationId", Value: correlation}, {Key: "causationId", Value: causation}, {Key: "payload", Value: r.Payload},
		{Key: "receivedAt", Value: r.ReceivedAt}, {Key: "processedAt", Value: r.ReceivedAt}, {Key: "status", Value: r.Status}, {Key: "attempts", Value: 1},
	}
	if r.FailureReason != "" {
		doc = append(doc, bson.E{Key: "failureReason", Value: r.FailureReason})
	}
	return m.transact(ctx, func(tx context.Context) error {
		if _, err := m.db.Collection("inbox_events").InsertOne(tx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return out.ErrDuplicateEvent
			}
			return err
		}
		for _, u := range updates {
			res, err := m.db.Collection("care_closures").ReplaceOne(tx, bson.D{{Key: "_id", Value: u.Closure.ID}, {Key: "version", Value: u.FromVersion}}, toClosureDoc(u.Closure))
			if err != nil {
				return err
			}
			if res.MatchedCount == 0 {
				return out.ErrVersionConflict
			}
		}
		return nil
	})
}

func (m *Mongo) FindClosuresByAppointment(ctx context.Context, appointmentID string) ([]model.CareClosure, error) {
	cur, err := m.db.Collection("care_closures").Find(ctx, bson.D{{Key: "appointmentId", Value: appointmentID}})
	if err != nil {
		return nil, err
	}
	var docs []closureDoc
	if err := cur.All(ctx, &docs); err != nil {
		return nil, err
	}
	found := make([]model.CareClosure, 0, len(docs))
	for _, d := range docs {
		found = append(found, d.model())
	}
	return found, nil
}
