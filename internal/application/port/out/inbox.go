package out

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var (
	// ErrDuplicateEvent means the eventId was already received (at-least-once delivery).
	ErrDuplicateEvent = errors.New("event already received")
	// ErrNotReady means the fact cannot be applied yet (out-of-order delivery); redeliver later.
	ErrNotReady = errors.New("event not applicable yet")
)

// InboxRecord is the durable receipt of an incoming event.
type InboxRecord struct {
	EventID, EventType, EventVersion, AggregateID string
	CorrelationID, CausationID                    string
	Payload                                       map[string]any
	Status                                        string // PROCESSED or FAILED
	FailureReason                                 string
	ReceivedAt                                    time.Time
}

// ClosureUpdate is a closure to store only while its stored version is FromVersion.
type ClosureUpdate struct {
	Closure     model.CareClosure
	FromVersion int
}

// InboxRepository deduplicates incoming events and applies their effect in
// the same transaction as the receipt.
type InboxRepository interface {
	Received(ctx context.Context, eventID string) (bool, error)
	// Apply stores the receipt and every closure update atomically; a repeated
	// eventId is ErrDuplicateEvent and a moved closure is ErrVersionConflict.
	Apply(ctx context.Context, r InboxRecord, updates []ClosureUpdate) error
	// FindClosuresByAppointment returns the encounter closures of an appointment.
	FindClosuresByAppointment(ctx context.Context, appointmentID string) ([]model.CareClosure, error)
}

// EncounterFinder finds the closure of an encounter (satisfied by ClosureRepository).
type EncounterFinder interface {
	FindClosureByEncounter(ctx context.Context, recordID, consultationID string) (model.CareClosure, bool, error)
}
