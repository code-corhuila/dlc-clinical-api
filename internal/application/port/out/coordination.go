package out

import (
	"context"
	"errors"
)

// ErrOwnerConflict is an owner's 409: the requested coordination step is not
// allowed in the owner's current state.
var ErrOwnerConflict = errors.New("owner rejected the operation in its current state")

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
