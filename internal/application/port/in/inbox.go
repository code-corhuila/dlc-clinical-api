package in

import (
	"context"
	"time"
)

// IncomingEvent is a committed fact received from another owner through the
// broker (envelope of 07-api/contracts/events). It is data, never authority.
type IncomingEvent struct {
	EventID       string
	EventType     string
	EventVersion  int
	AggregateID   string
	CorrelationID string
	CausationID   string
	Payload       map[string]any
	OccurredAt    time.Time
}

// EventConsumer applies an incoming fact exactly once. A nil error means the
// message may be acknowledged; out.ErrNotReady asks the broker to redeliver.
type EventConsumer interface {
	Consume(ctx context.Context, e IncomingEvent) error
}
