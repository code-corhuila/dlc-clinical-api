package out

import (
	"context"
	"errors"
)

// ErrOwnerConflict is an owner's 409: the requested coordination step is not
// allowed in the owner's current state.
var ErrOwnerConflict = errors.New("owner rejected the operation in its current state")

// Appointment states of APT-001 that matter to Clinical.
const AppointmentInAttention = "EN_ATENCION"

// EncounterStatus is Appointments' authoritative view of one appointment.
type EncounterStatus struct {
	AppointmentID string
	PatientID     string
	DentistID     string
	Status        string
	Version       int
}

// AppointmentAuthority reads the current state of an appointment from
// Appointments (ErrNotFound when unknown). A stale projection never replaces it.
type AppointmentAuthority interface {
	EncounterStatus(ctx context.Context, appointmentID string) (EncounterStatus, error)
}

// ManualChargeSettlement is the frozen manual-charge source set of an
// encounter: identifiers only, never amounts.
type ManualChargeSettlement struct {
	Settled       bool
	PendingWrites int
	ChargeIDs     []string
	Version       int
}

// BillingAuthority freezes and releases an encounter's manual charges for the
// care-completions declaration (ADR-017 §6). Both calls are idempotent by the
// declaration's operationId; Release answers ErrOwnerConflict once Billing has
// recorded the declaration as committed.
type BillingAuthority interface {
	FreezeManualCharges(ctx context.Context, recordID, consultationID, operationID string) (ManualChargeSettlement, error)
	ReleaseManualChargeFreeze(ctx context.Context, recordID, consultationID, operationID string) error
}

// Guard outcomes and states (ADR-017 §1).
const (
	GuardOpen      = "OPEN"
	GuardFinalized = "FINALIZED"
	GuardAborted   = "ABORTED"
)

// Guard is an activity guard on a patient gate. Token is a proof for
// Appointments only: it is never stored in clear text or logged.
type Guard struct {
	ID    string
	State string
	Token string
}

// PatientGate serializes Clinical writes against patient deactivation
// (ADR-017 §2–3). Acquiring again with the same operationId returns the same
// guard with a rotated token; ErrOwnerConflict means the gate is blocked.
type PatientGate interface {
	AcquireActivityGuard(ctx context.Context, patientID, operationID string) (Guard, error)
	ResolveGuard(ctx context.Context, guardID, token, outcome string) error
}
