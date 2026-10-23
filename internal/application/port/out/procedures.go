package out

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// ErrDuplicateSource means an extra's sourceRecordId was already recorded.
var ErrDuplicateSource = errors.New("extra source already recorded")

// RecordedExtra is an extra traced to its procedure, appointment and author (CLN-008).
type RecordedExtra struct {
	model.Extra
	ProcedureID   string
	AppointmentID string
	AuthorID      string
	CreatedAt     time.Time
}

// ProcedureRepository persists procedure progress.
type ProcedureRepository interface {
	FindIdempotency(ctx context.Context, actorID, operation, key string) (IdempotencyRecord, bool, error)
	GetProcedure(ctx context.Context, id string) (model.Procedure, error)
	// CompleteProcedure updates the procedure only while its stored version is
	// fromVersion, and writes its extras, key and events in the same transaction.
	CompleteProcedure(ctx context.Context, p model.Procedure, fromVersion int, extras []RecordedExtra, idem IdempotencyRecord, events []OutboxEvent) error
}
