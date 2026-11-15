package usecase

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// errMalformedEvent rejects a payload that breaks its event contract; the
// subscriber dead-letters it after the bounded retries.
var errMalformedEvent = errors.New("malformed analytics event")

// AnalyticsProjector keeps the HU-CLN-003 projections from Appointments,
// Billing and Clinical facts (ADR-006). Each event is applied once, never
// moves a projection backwards, and changes no operational state.
type AnalyticsProjector struct {
	Projections out.AnalyticsProjections
	Location    *time.Location // clinic timezone for days and months
}

var _ in.EventConsumer = (*AnalyticsProjector)(nil)

var appointmentOutcomes = map[string]model.AppointmentStatus{
	"AppointmentCancelled": model.AppointmentCancelled,
	"AppointmentNoShow":    model.AppointmentNoShow,
	"AppointmentCompleted": model.AppointmentFinalized,
}

// Consume applies one source event; events outside HU-CLN-003 are ignored.
func (p *AnalyticsProjector) Consume(ctx context.Context, e in.IncomingEvent) error {
	proj := out.Projection{Event: out.ProcessedEvent{EventID: e.EventID, EventType: e.EventType, OccurredAt: e.OccurredAt}}
	var err error
	switch e.EventType {
	case "AppointmentScheduled", "AppointmentRescheduled", "AppointmentCancelled", "AppointmentNoShow", "AppointmentCompleted":
		err = p.appointment(ctx, e, &proj)
	case "ClinicalCareCompleted":
		err = p.activity(e, &proj)
	case "PaymentRecorded":
		err = p.revenue(e, &proj)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	if err := p.Projections.Project(ctx, proj); err != nil && !errors.Is(err, out.ErrDuplicateEvent) {
		return err
	}
	return nil
}

func (p *AnalyticsProjector) appointment(ctx context.Context, e in.IncomingEvent, proj *out.Projection) error {
	id, err := payloadText(e.Payload, "appointmentId")
	if err != nil {
		return err
	}
	stored, found, err := p.Projections.FindAppointmentFact(ctx, id)
	if err != nil {
		return err
	}
	next := stored
	switch e.EventType {
	case "AppointmentScheduled":
		next = model.AppointmentFact{AppointmentID: id, Status: model.AppointmentScheduled}
		if next.DentistID, err = payloadText(e.Payload, "dentistId"); err != nil {
			return err
		}
		if next.StartAt, err = payloadInstant(e.Payload, "startAt"); err != nil {
			return err
		}
	case "AppointmentRescheduled":
		if !found {
			return fmt.Errorf("rescheduling %s before its scheduling: %w", id, out.ErrNotReady)
		}
		if next.StartAt, err = payloadInstant(e.Payload, "newStartAt"); err != nil {
			return err
		}
		version, ok := e.Payload["appointmentVersion"].(float64)
		if !ok || version < 1 {
			return fmt.Errorf("appointmentVersion: %w", errMalformedEvent)
		}
		next.AppointmentVersion = int(version)
	default:
		if !found {
			return fmt.Errorf("%s of %s before its scheduling: %w", e.EventType, id, out.ErrNotReady)
		}
		next.Status = appointmentOutcomes[e.EventType]
	}
	next.SourceEventID, next.SourceEventType, next.SourceOccurredAt = e.EventID, e.EventType, e.OccurredAt
	if found && !next.Supersedes(stored) {
		return nil // an older event: recorded as processed, projection unchanged
	}
	proj.Fact, proj.ExpectedSourceEventID = &next, stored.SourceEventID
	return nil
}

func (p *AnalyticsProjector) activity(e in.IncomingEvent, proj *out.Projection) error {
	dentist, err := payloadText(e.Payload, "dentistId")
	if err != nil {
		return err
	}
	completed, err := payloadInstant(e.Payload, "completedAt")
	if err != nil {
		return err
	}
	proj.Activity = &out.ActivityIncrement{Day: model.ClinicDay(completed, p.Location), DentistID: dentist}
	return nil
}

func (p *AnalyticsProjector) revenue(e in.IncomingEvent, proj *out.Projection) error {
	if currency, _ := e.Payload["currency"].(string); currency != "COP" {
		return fmt.Errorf("currency %q: %w", currency, errMalformedEvent)
	}
	amount, err := payloadText(e.Payload, "amount")
	if err != nil {
		return err
	}
	cents, err := model.ParseCOP(amount)
	if err != nil {
		return fmt.Errorf("amount: %v: %w", err, errMalformedEvent)
	}
	recorded, err := payloadInstant(e.Payload, "recordedAt")
	if err != nil {
		return err
	}
	proj.Revenue = &out.RevenueIncrement{Month: model.ClinicMonth(recorded, p.Location), Cents: cents}
	return nil
}

func payloadText(payload map[string]any, key string) (string, error) {
	v, ok := payload[key].(string)
	if !ok || v == "" {
		return "", fmt.Errorf("%s: %w", key, errMalformedEvent)
	}
	return v, nil
}

func payloadInstant(payload map[string]any, key string) (time.Time, error) {
	v, err := payloadText(payload, key)
	if err != nil {
		return time.Time{}, err
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", key, errMalformedEvent)
	}
	return t.UTC(), nil
}
