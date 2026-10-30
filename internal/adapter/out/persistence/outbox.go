package persistence

import (
	"context"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var _ out.OutboxStore = (*Mongo)(nil)

type outboxRow struct {
	ID            string    `bson:"_id"`
	EventType     string    `bson:"eventType"`
	EventVersion  string    `bson:"eventVersion"`
	AggregateID   string    `bson:"aggregateId"`
	AggregateType string    `bson:"aggregateType"`
	ActorUserID   string    `bson:"actorUserId"`
	CorrelationID string    `bson:"correlationId"`
	CausationID   string    `bson:"causationId"`
	Payload       bson.M    `bson:"payload"`
	OccurredAt    time.Time `bson:"occurredAt"`
	Attempts      int       `bson:"attempts"`
}

// Due reads PENDING events whose next attempt is due, oldest first, bounded
// by limit; it uses idx_outbox_events_publish_work.
func (m *Mongo) Due(ctx context.Context, now time.Time, limit int) ([]out.PendingEvent, error) {
	filter := bson.D{{Key: "status", Value: "PENDING"}, {Key: "$or", Value: bson.A{
		bson.D{{Key: "nextAttemptAt", Value: bson.D{{Key: "$exists", Value: false}}}},
		bson.D{{Key: "nextAttemptAt", Value: bson.D{{Key: "$lte", Value: now}}}},
	}}}
	cur, err := m.db.Collection("outbox_events").Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "occurredAt", Value: 1}, {Key: "_id", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	var rows []outboxRow
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	due := make([]out.PendingEvent, 0, len(rows))
	for _, r := range rows {
		version, _ := strconv.Atoi(r.EventVersion)
		due = append(due, out.PendingEvent{Attempts: r.Attempts, Event: out.OutboxEvent{EventID: r.ID, EventType: r.EventType, EventVersion: version,
			AggregateID: r.AggregateID, AggregateType: r.AggregateType, CorrelationID: r.CorrelationID, CausationID: r.CausationID,
			ActorID: r.ActorUserID, Payload: map[string]any(r.Payload), OccurredAt: r.OccurredAt.UTC()}})
	}
	return due, nil
}

func (m *Mongo) setOutbox(ctx context.Context, id string, set, unset bson.D) error {
	update := bson.D{{Key: "$set", Value: set}}
	if len(unset) > 0 {
		update = append(update, bson.E{Key: "$unset", Value: unset})
	}
	_, err := m.db.Collection("outbox_events").UpdateOne(ctx, bson.D{{Key: "_id", Value: id}}, update)
	return err
}

func (m *Mongo) MarkPublished(ctx context.Context, eventID string, at time.Time) error {
	return m.setOutbox(ctx, eventID, bson.D{{Key: "status", Value: "PUBLISHED"}, {Key: "publishedAt", Value: at}},
		bson.D{{Key: "nextAttemptAt", Value: ""}, {Key: "failureReason", Value: ""}})
}

func (m *Mongo) Reschedule(ctx context.Context, eventID string, attempts int, next time.Time, reason string) error {
	return m.setOutbox(ctx, eventID, bson.D{{Key: "attempts", Value: attempts}, {Key: "nextAttemptAt", Value: next}, {Key: "failureReason", Value: reason}}, nil)
}

func (m *Mongo) MarkFailed(ctx context.Context, eventID string, attempts int, reason string) error {
	return m.setOutbox(ctx, eventID, bson.D{{Key: "status", Value: "FAILED"}, {Key: "attempts", Value: attempts}, {Key: "failureReason", Value: reason}},
		bson.D{{Key: "nextAttemptAt", Value: ""}})
}

// MarkDeclarationFailed fails the event and its encounter closure in one
// transaction; a completed closure is left as it is.
func (m *Mongo) MarkDeclarationFailed(ctx context.Context, eventID string, attempts int, reason, recordID, consultationID string, at time.Time) error {
	closureReason := []rune("ClinicalCareCompleted could not be delivered: " + reason)
	if len(closureReason) > 500 { // care_closures validator
		closureReason = closureReason[:500]
	}
	return m.transact(ctx, func(tx context.Context) error {
		if err := m.MarkFailed(tx, eventID, attempts, reason); err != nil {
			return err
		}
		_, err := m.db.Collection("care_closures").UpdateOne(tx,
			bson.D{{Key: "clinicalRecordId", Value: recordID}, {Key: "consultationId", Value: consultationID},
				{Key: "status", Value: bson.D{{Key: "$ne", Value: string(model.ClosureCompleted)}}}},
			bson.D{{Key: "$set", Value: bson.D{{Key: "status", Value: string(model.ClosureFailed)},
				{Key: "failureReason", Value: string(closureReason)}, {Key: "updatedAt", Value: at}}},
				{Key: "$inc", Value: bson.D{{Key: "version", Value: 1}}}})
		return err
	})
}
