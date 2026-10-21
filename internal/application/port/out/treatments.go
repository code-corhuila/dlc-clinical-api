package out

import (
	"context"
	"errors"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// ErrVersionConflict means another write changed the resource after it was read.
var ErrVersionConflict = errors.New("version conflict")

// TreatmentRepository persists treatments and their planned procedures.
type TreatmentRepository interface {
	FindIdempotency(ctx context.Context, actorID, operation, key string) (IdempotencyRecord, bool, error)
	// CreateTreatment writes treatment, procedures, key and events in one transaction.
	CreateTreatment(ctx context.Context, t model.Treatment, idem IdempotencyRecord, events []OutboxEvent) error
	// UpdateTreatment replaces t only if the stored version is still fromVersion,
	// and stores the key in the same transaction; otherwise ErrVersionConflict.
	UpdateTreatment(ctx context.Context, t model.Treatment, fromVersion int, idem IdempotencyRecord) error
	GetTreatment(ctx context.Context, id string) (model.Treatment, error)
	// ListTreatments returns newest first and the total matching the filter.
	ListTreatments(ctx context.Context, f RecordFilter) ([]model.Treatment, int, error)
}
