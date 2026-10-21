package in

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// CompleteProcedureCommand records the individual completion of a procedure
// with its clinically justified materials and needs, never prices.
type CompleteProcedureCommand struct {
	Actor                  Actor
	ProcedureID            string
	ExpectedVersion        int
	MaterialsUsed          []model.ExtraInput
	AdditionalRequirements []model.ExtraInput
	IdempotencyKey         string
	RequestHash            string
	CorrelationID          string
}

// ProcedureCompletionResult tells whether the completion happened now or was replayed.
type ProcedureCompletionResult struct {
	Procedure model.Procedure
	Replayed  bool
}

// ProcedureCompletions is the HU-CLN-002 operation (ADR-014: progress only, no closure).
type ProcedureCompletions interface {
	Complete(ctx context.Context, cmd CompleteProcedureCommand) (ProcedureCompletionResult, error)
}
