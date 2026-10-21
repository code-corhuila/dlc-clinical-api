package model

import (
	"slices"
	"time"
)

// ClosureStatus is the encounter closure state (ADR-014).
type ClosureStatus string

const (
	// ClosureAwaitingFinalization is the normal wait for the administrative
	// finalization of the appointment; it is never a failure.
	ClosureAwaitingFinalization ClosureStatus = "AWAITING_ADMINISTRATIVE_FINALIZATION"
	ClosurePending              ClosureStatus = "CLOSURE_PENDING"
	ClosureCompleted            ClosureStatus = "CLOSURE_COMPLETED"
	ClosureFailed               ClosureStatus = "CLOSURE_FAILED"
)

// Outcome is the state of one downstream owner's result.
type Outcome string

const (
	OutcomePending   Outcome = "PENDING"
	OutcomeCompleted Outcome = "COMPLETED"
)

// maxDeclaredSources bounds each declared set, matching the dlc-clinical-db validator.
const maxDeclaredSources = 100

// CareClosure is the explicit Dentist declaration of one encounter (CLN-009)
// and the process that observes its Appointments and Billing outcomes.
type CareClosure struct {
	ID                 string
	RecordID           string
	ConsultationID     string
	AppointmentID      string
	PatientID          string
	DentistID          string
	ProcedureIDs       []string
	ManualChargeIDs    []string
	Status             ClosureStatus
	AppointmentOutcome Outcome
	BillingOutcome     Outcome
	FailureReason      string
	CorrelationID      string
	CompletedAt        time.Time
	ClinicalVersion    int
	CreatedAt          time.Time
	UpdatedAt          time.Time
	Version            int
}

// DeclarationInput is the server-derived content of a declaration; the
// source set is never taken from the client.
type DeclarationInput struct {
	ClosureID, RecordID, ConsultationID, AppointmentID string
	PatientID, DentistID                               string
	ProcedureIDs, ManualChargeIDs                      []string
	ClinicalVersion                                    int
	CorrelationID                                      string
	At                                                 time.Time
}

func declaredSet(field string, ids []string) ([]string, error) {
	if len(ids) > maxDeclaredSources {
		return nil, invalid(field, "must contain at most 100 items")
	}
	set := slices.Clone(ids)
	slices.Sort(set)
	if len(slices.Compact(slices.Clone(set))) != len(set) {
		return nil, invalid(field, "must not repeat items")
	}
	if set == nil {
		set = []string{}
	}
	return set, nil
}

// DeclareCareCompletion fixes the encounter's source set and opens its closure.
func DeclareCareCompletion(in DeclarationInput) (CareClosure, error) {
	switch {
	case in.ConsultationID == "":
		return CareClosure{}, invalid("consultationId", "required")
	case in.AppointmentID == "":
		return CareClosure{}, invalid("appointmentId", "required")
	case in.DentistID == "":
		return CareClosure{}, invalid("dentistId", "required")
	}
	procs, err := declaredSet("procedureIds", in.ProcedureIDs)
	if err != nil {
		return CareClosure{}, err
	}
	charges, err := declaredSet("manualChargeIds", in.ManualChargeIDs)
	if err != nil {
		return CareClosure{}, err
	}
	return CareClosure{
		ID: in.ClosureID, RecordID: in.RecordID, ConsultationID: in.ConsultationID, AppointmentID: in.AppointmentID,
		PatientID: in.PatientID, DentistID: in.DentistID, ProcedureIDs: procs, ManualChargeIDs: charges,
		Status: ClosureAwaitingFinalization, AppointmentOutcome: OutcomePending, BillingOutcome: OutcomePending,
		CorrelationID: in.CorrelationID, CompletedAt: in.At, ClinicalVersion: in.ClinicalVersion,
		CreatedAt: in.At, UpdatedAt: in.At, Version: 1,
	}, nil
}

// RecordAppointmentCompleted observes AppointmentCompleted; repeats are ignored.
func (c *CareClosure) RecordAppointmentCompleted(at time.Time) {
	if c.AppointmentOutcome != OutcomeCompleted {
		c.AppointmentOutcome = OutcomeCompleted
		c.advance(at)
	}
}

// RecordBillingCompleted observes EncounterChargesCompleted; repeats are ignored.
func (c *CareClosure) RecordBillingCompleted(at time.Time) {
	if c.BillingOutcome != OutcomeCompleted {
		c.BillingOutcome = OutcomeCompleted
		c.advance(at)
	}
}

// advance derives the status from both outcomes, in either order.
func (c *CareClosure) advance(at time.Time) {
	switch {
	case c.AppointmentOutcome == OutcomeCompleted && c.BillingOutcome == OutcomeCompleted:
		c.Status = ClosureCompleted
	case c.AppointmentOutcome == OutcomeCompleted:
		c.Status = ClosurePending
	default:
		c.Status = ClosureAwaitingFinalization
	}
	c.UpdatedAt = at
	c.Version++
}
