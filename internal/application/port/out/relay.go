package out

import (
	"context"
	"errors"
	"time"
)

// ErrPermanentPublish marks a publication a retry cannot fix (for example an
// unroutable message); the event goes straight to FAILED.
var ErrPermanentPublish = errors.New("permanent publish failure")

// Envelope is the published form of an event (07-api/contracts/events).
type Envelope struct {
	EventID       string         `json:"eventId"`
	EventType     string         `json:"eventType"`
	AggregateID   string         `json:"aggregateId"`
	AggregateType string         `json:"aggregateType"`
	OccurredAt    string         `json:"occurredAt"`
	Version       int            `json:"version"`
	Producer      string         `json:"producer"`
	Payload       map[string]any `json:"payload"`
	Metadata      Metadata       `json:"metadata"`
}

// Metadata carries the trace of an event through every service.
type Metadata struct {
	CorrelationID string `json:"correlationId"`
	CausationID   string `json:"causationId"`
	ActorUserID   string `json:"actorUserId,omitempty"`
}

// PendingEvent is an outbox row due for publication.
type PendingEvent struct {
	Event    OutboxEvent
	Attempts int
}

// OutboxStore reads due events and records each publication outcome.
type OutboxStore interface {
	// Due returns at most limit PENDING events whose next attempt is due, oldest first.
	Due(ctx context.Context, now time.Time, limit int) ([]PendingEvent, error)
	MarkPublished(ctx context.Context, eventID string, at time.Time) error
	Reschedule(ctx context.Context, eventID string, attempts int, next time.Time, reason string) error
	MarkFailed(ctx context.Context, eventID string, attempts int, reason string) error
}

// Publisher hands an envelope to the broker and returns once it is confirmed.
type Publisher interface {
	Publish(ctx context.Context, e Envelope) error
}
