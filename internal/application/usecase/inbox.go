package usecase

import (
	"context"
	"errors"
	"slices"
	"strconv"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// InboxService implements in.EventConsumer for the closure outcomes
// (ADR-014): AppointmentCompleted and EncounterChargesCompleted.
type InboxService struct {
	Inbox      out.InboxRepository
	Encounters out.EncounterFinder
	Clock      out.Clock
}

var _ in.EventConsumer = (*InboxService)(nil)

// rejected is a fact that will never apply: recorded FAILED and acknowledged.
type rejected string

// Consume applies an event exactly once: duplicates are acknowledged, facts
// that arrive before their closure are redelivered, mismatched facts are
// recorded as failed without effect, and other events are only recorded.
func (s *InboxService) Consume(ctx context.Context, e in.IncomingEvent) error {
	seen, err := s.Inbox.Received(ctx, e.EventID)
	if err != nil || seen {
		return err
	}
	now := s.Clock.Now()
	var updates []out.ClosureUpdate
	var reason rejected
	switch e.EventType {
	case "AppointmentCompleted":
		updates, reason, err = s.appointmentCompleted(ctx, e)
	case "EncounterChargesCompleted":
		updates, reason, err = s.chargesCompleted(ctx, e)
	}
	if err != nil {
		return err
	}
	receipt := out.InboxRecord{EventID: e.EventID, EventType: e.EventType, EventVersion: strconv.Itoa(e.EventVersion), AggregateID: e.AggregateID,
		CorrelationID: e.CorrelationID, CausationID: e.CausationID, Payload: e.Payload, Status: "PROCESSED", ReceivedAt: now}
	if reason != "" {
		receipt.Status, receipt.FailureReason, updates = "FAILED", string(reason), nil
	}
	for i := range updates {
		updates[i].Closure.UpdatedAt = now
	}
	if err := s.Inbox.Apply(ctx, receipt, updates); err != nil && !errors.Is(err, out.ErrDuplicateEvent) {
		return err
	}
	return nil
}

func (s *InboxService) appointmentCompleted(ctx context.Context, e in.IncomingEvent) ([]out.ClosureUpdate, rejected, error) {
	if e.EventVersion != 1 {
		return nil, "unsupported event version", nil
	}
	apt, ok1 := text(e.Payload, "appointmentId")
	patient, ok2 := text(e.Payload, "patientId")
	if !ok1 || !ok2 {
		return nil, "malformed payload", nil
	}
	closures, err := s.Inbox.FindClosuresByAppointment(ctx, apt)
	if err != nil {
		return nil, "", err
	}
	if len(closures) == 0 {
		return nil, "", out.ErrNotReady
	}
	var updates []out.ClosureUpdate
	for _, c := range closures {
		if c.PatientID != patient {
			return nil, "patient does not match the declared encounter", nil
		}
		from := c.Version
		if c.RecordAppointmentCompleted(s.Clock.Now()); c.Version != from {
			updates = append(updates, out.ClosureUpdate{Closure: c, FromVersion: from})
		}
	}
	return updates, "", nil
}

func (s *InboxService) chargesCompleted(ctx context.Context, e in.IncomingEvent) ([]out.ClosureUpdate, rejected, error) {
	if e.EventVersion != 1 {
		return nil, "unsupported event version", nil
	}
	rec, ok1 := text(e.Payload, "clinicalRecordId")
	cons, ok2 := text(e.Payload, "consultationId")
	apt, ok3 := text(e.Payload, "appointmentId")
	patient, ok4 := text(e.Payload, "patientId")
	procs, ok5 := textSet(e.Payload, "procedureIds")
	charges, ok6 := textSet(e.Payload, "manualChargeIds")
	if !ok1 || !ok2 || !ok3 || !ok4 || !ok5 || !ok6 {
		return nil, "malformed payload", nil
	}
	c, found, err := s.Encounters.FindClosureByEncounter(ctx, rec, cons)
	if err != nil {
		return nil, "", err
	}
	if !found {
		return nil, "", out.ErrNotReady
	}
	// Billing must confirm exactly the declared source set (ADR-015).
	if c.AppointmentID != apt || c.PatientID != patient || !slices.Equal(procs, c.ProcedureIDs) || !slices.Equal(charges, c.ManualChargeIDs) {
		return nil, "charges do not match the declared encounter", nil
	}
	from := c.Version
	if c.RecordBillingCompleted(s.Clock.Now()); c.Version == from {
		return nil, "", nil
	}
	return []out.ClosureUpdate{{Closure: c, FromVersion: from}}, "", nil
}

func text(p map[string]any, key string) (string, bool) {
	v, ok := p[key].(string)
	return v, ok && v != ""
}

// textSet reads a string array and returns it sorted, as the closure stores it.
func textSet(p map[string]any, key string) ([]string, bool) {
	raw, ok := p[key].([]any)
	if !ok {
		return nil, false
	}
	set := make([]string, 0, len(raw))
	for _, v := range raw {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		set = append(set, s)
	}
	slices.Sort(set)
	return set, true
}
