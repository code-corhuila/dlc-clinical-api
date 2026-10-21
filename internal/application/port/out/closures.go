package out

import (
	"context"
	"errors"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// ErrClosureExists means the encounter was already declared.
var ErrClosureExists = errors.New("encounter already declared")

// ClosureRepository persists encounter declarations and their closures.
type ClosureRepository interface {
	FindIdempotency(ctx context.Context, actorID, operation, key string) (IdempotencyRecord, bool, error)
	GetClosure(ctx context.Context, id string) (model.CareClosure, error)
	FindClosureByEncounter(ctx context.Context, recordID, consultationID string) (model.CareClosure, bool, error)
	// CompletedProcedureIDs derives the encounter's completed procedures from Clinical's own data.
	CompletedProcedureIDs(ctx context.Context, recordID, appointmentID string) ([]string, error)
	// DeclareCareCompletion inserts the closure, raises the record version only
	// while it is still recordFromVersion, and stores key and events atomically.
	DeclareCareCompletion(ctx context.Context, c model.CareClosure, recordFromVersion int, idem IdempotencyRecord, events []OutboxEvent) error
}

// BillingAuthority lists, from Billing, the manual charges of an encounter;
// Clinical never reads Billing's data directly.
type BillingAuthority interface {
	EncounterManualCharges(ctx context.Context, recordID, consultationID, appointmentID string) ([]string, error)
}

// EncounterGate stops admitting new Clinical and Billing writes to an encounter
// and confirms in-flight ones are settled. The cross-owner protocol is still a
// release gate (care-completion.md), so the only adapter today fails closed.
type EncounterGate interface {
	Quiesce(ctx context.Context, recordID, consultationID, appointmentID string) error
}
