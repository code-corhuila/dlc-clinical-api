package out

import (
	"context"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// OutboxEvent is a committed fact to publish later (Norma 5.3.11). It is
// written in the same transaction as the change that produced it.
type OutboxEvent struct {
	EventID       string
	EventType     string
	EventVersion  int
	AggregateID   string
	AggregateType string
	CorrelationID string
	CausationID   string
	ActorID       string
	Payload       map[string]any
	OccurredAt    time.Time
}

// EntryRepository persists clinical entries. CreateEntry writes the entry,
// its idempotency record and its outbox events in one transaction.
type EntryRepository interface {
	FindIdempotency(ctx context.Context, actorID, operation, key string) (IdempotencyRecord, bool, error)
	CreateEntry(ctx context.Context, e model.Entry, idem IdempotencyRecord, events []OutboxEvent) error
	GetEntry(ctx context.Context, id string) (model.Entry, error)
	// ListEntries returns a record's entries newest first and their total.
	ListEntries(ctx context.Context, recordID string, offset, limit int) ([]model.Entry, int, error)
}
