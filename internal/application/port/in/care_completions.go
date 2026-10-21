package in

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// DeclareCareCompletionCommand is the Dentist's explicit declaration that all
// clinical work of an encounter is saved (CLN-009). The source set is derived
// by the server, never sent by the client.
type DeclareCareCompletionCommand struct {
	Actor           Actor
	RecordID        string
	ConsultationID  string
	AppointmentID   string
	ExpectedVersion int
	IdempotencyKey  string
	RequestHash     string
	CorrelationID   string
}

// CareClosureResult tells whether the declaration happened now or was replayed.
type CareClosureResult struct {
	Closure  model.CareClosure
	Replayed bool
}

// CareCompletions are the HU-XCT-001 operations owned by Clinical (ADR-014).
type CareCompletions interface {
	Declare(ctx context.Context, cmd DeclareCareCompletionCommand) (CareClosureResult, error)
	GetClosure(ctx context.Context, actor Actor, id string) (model.CareClosure, error)
}
