package persistence

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var (
	_ out.AnalyticsProjections = (*Mongo)(nil)
	_ out.AnalyticsReader      = (*Mongo)(nil)
)

type factDoc struct {
	ID                 string    `bson:"_id"`
	AppointmentID      string    `bson:"appointmentId"`
	DentistID          string    `bson:"dentistId"`
	StartAt            time.Time `bson:"startAt"`
	Status             string    `bson:"status"`
	AppointmentVersion int       `bson:"appointmentVersion,omitempty"`
	SourceEventID      string    `bson:"sourceEventId"`
	SourceEventType    string    `bson:"sourceEventType"`
	SourceOccurredAt   time.Time `bson:"sourceOccurredAt"`
	UpdatedAt          time.Time `bson:"updatedAt"`
}

func (d factDoc) model() model.AppointmentFact {
	return model.AppointmentFact{AppointmentID: d.AppointmentID, DentistID: d.DentistID, StartAt: d.StartAt, Status: model.AppointmentStatus(d.Status),
		AppointmentVersion: d.AppointmentVersion, SourceEventID: d.SourceEventID, SourceEventType: d.SourceEventType, SourceOccurredAt: d.SourceOccurredAt}
}

var countedStatuses = bson.A{string(model.AppointmentScheduled), string(model.AppointmentFinalized)}

func (m *Mongo) FindAppointmentFact(ctx context.Context, id string) (model.AppointmentFact, bool, error) {
	var d factDoc
	err := m.db.Collection("analytics_appointment_facts").FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return model.AppointmentFact{}, false, nil
	}
	return d.model(), err == nil, err
}

// Project stores the receipt and every change in one majority transaction.
func (m *Mongo) Project(ctx context.Context, p out.Projection) error {
	return m.transact(ctx, func(tx context.Context) error {
		at := now()
		if _, err := m.db.Collection("analytics_processed_events").InsertOne(tx, bson.D{{Key: "_id", Value: p.Event.EventID},
			{Key: "eventId", Value: p.Event.EventID}, {Key: "eventType", Value: p.Event.EventType},
			{Key: "occurredAt", Value: p.Event.OccurredAt}, {Key: "processedAt", Value: at}}); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return out.ErrDuplicateEvent
			}
			return err
		}
		if p.Fact != nil {
			if err := m.replaceFact(tx, *p.Fact, p.ExpectedSourceEventID, at); err != nil {
				return err
			}
		}
		if a := p.Activity; a != nil {
			if _, err := m.db.Collection("analytics_weekly_activity").UpdateOne(tx, bson.D{{Key: "_id", Value: a.Day + "|" + a.DentistID}},
				bson.D{{Key: "$inc", Value: bson.D{{Key: "count", Value: int32(1)}}},
					{Key: "$set", Value: bson.D{{Key: "lastEventId", Value: p.Event.EventID}, {Key: "updatedAt", Value: at}}},
					{Key: "$setOnInsert", Value: bson.D{{Key: "day", Value: a.Day}, {Key: "dentistId", Value: a.DentistID}}}},
				options.UpdateOne().SetUpsert(true)); err != nil {
				return err
			}
		}
		if r := p.Revenue; r != nil {
			amount, err := bson.ParseDecimal128(model.FormatCOP(r.Cents))
			if err != nil {
				return err
			}
			if _, err := m.db.Collection("analytics_monthly_revenue").UpdateOne(tx, bson.D{{Key: "_id", Value: r.Month}},
				bson.D{{Key: "$inc", Value: bson.D{{Key: "collected", Value: amount}, {Key: "payments", Value: int32(1)}}},
					{Key: "$set", Value: bson.D{{Key: "lastEventId", Value: p.Event.EventID}, {Key: "updatedAt", Value: at}}},
					{Key: "$setOnInsert", Value: bson.D{{Key: "month", Value: r.Month}}}},
				options.UpdateOne().SetUpsert(true)); err != nil {
				return err
			}
		}
		return nil
	})
}

// replaceFact inserts a first fact, or replaces the stored one only while it
// still comes from expected; anything else is a concurrent change.
func (m *Mongo) replaceFact(tx context.Context, f model.AppointmentFact, expected string, at time.Time) error {
	doc := factDoc{ID: f.AppointmentID, AppointmentID: f.AppointmentID, DentistID: f.DentistID, StartAt: f.StartAt, Status: string(f.Status),
		AppointmentVersion: f.AppointmentVersion, SourceEventID: f.SourceEventID, SourceEventType: f.SourceEventType,
		SourceOccurredAt: f.SourceOccurredAt, UpdatedAt: at}
	facts := m.db.Collection("analytics_appointment_facts")
	if expected == "" {
		if _, err := facts.InsertOne(tx, doc); err != nil {
			if mongo.IsDuplicateKeyError(err) {
				return out.ErrVersionConflict
			}
			return err
		}
		return nil
	}
	res, err := facts.ReplaceOne(tx, bson.D{{Key: "_id", Value: f.AppointmentID}, {Key: "sourceEventId", Value: expected}}, doc)
	if err == nil && res.MatchedCount == 0 {
		return out.ErrVersionConflict
	}
	return err
}

func scoped(filter bson.D, dentistID string) bson.D {
	if dentistID != "" {
		filter = append(filter, bson.E{Key: "dentistId", Value: dentistID})
	}
	return filter
}

func (m *Mongo) CountAppointments(ctx context.Context, from, to time.Time, dentistID string) (int, error) {
	n, err := m.db.Collection("analytics_appointment_facts").CountDocuments(ctx, scoped(bson.D{
		{Key: "startAt", Value: bson.D{{Key: "$gte", Value: from}, {Key: "$lt", Value: to}}},
		{Key: "status", Value: bson.D{{Key: "$in", Value: countedStatuses}}}}, dentistID))
	return int(n), err
}

func (m *Mongo) ActivityBetween(ctx context.Context, fromDay, toDay, dentistID string) (map[string]int, error) {
	cur, err := m.db.Collection("analytics_weekly_activity").Find(ctx,
		scoped(bson.D{{Key: "day", Value: bson.D{{Key: "$gte", Value: fromDay}, {Key: "$lte", Value: toDay}}}}, dentistID))
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Day   string `bson:"day"`
		Count int    `bson:"count"`
	}
	if err := cur.All(ctx, &rows); err != nil {
		return nil, err
	}
	counts := map[string]int{}
	for _, r := range rows {
		counts[r.Day] += r.Count
	}
	return counts, nil
}

func (m *Mongo) CollectedRevenue(ctx context.Context, month string) (int64, bool, error) {
	var d struct {
		Collected bson.Decimal128 `bson:"collected"`
	}
	err := m.db.Collection("analytics_monthly_revenue").FindOne(ctx, bson.D{{Key: "_id", Value: month}}).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	cents, err := decimalCents(d.Collected)
	return cents, err == nil, err
}

// decimalCents converts an exact Decimal128 to cents without floating point.
func decimalCents(d bson.Decimal128) (int64, error) {
	significand, exp, err := d.BigInt()
	if err != nil {
		return 0, err
	}
	scale := exp + 2
	if scale >= 0 {
		significand.Mul(significand, new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(scale)), nil))
	} else {
		divisor := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(-scale)), nil)
		if new(big.Int).Rem(significand, divisor).Sign() != 0 {
			return 0, fmt.Errorf("collected %s is finer than a cent", d.String())
		}
		significand.Quo(significand, divisor)
	}
	if !significand.IsInt64() {
		return 0, fmt.Errorf("collected %s overflows", d.String())
	}
	return significand.Int64(), nil
}

func (m *Mongo) LatestEvent(ctx context.Context) (time.Time, bool, error) {
	var d struct {
		OccurredAt time.Time `bson:"occurredAt"`
	}
	err := m.db.Collection("analytics_processed_events").FindOne(ctx, bson.D{},
		options.FindOne().SetSort(bson.D{{Key: "occurredAt", Value: -1}})).Decode(&d)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return time.Time{}, false, nil
	}
	return d.OccurredAt.UTC(), err == nil, err
}
