// Package messaging is the inbound broker adapter: it turns RabbitMQ
// deliveries into in.IncomingEvent and acknowledges only applied facts.
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
)

// Binding subscribes the queue to one producer event (exact PascalCase type).
type Binding struct{ Exchange, RoutingKey string }

// Options declare the consumer's queue and its explicit limits.
type Options struct {
	URL             string
	Queue           string
	Bindings        []Binding
	DeliveryLimit   int           // redeliveries before the broker dead-letters
	Prefetch        int           // unacknowledged messages in flight
	Timeout         time.Duration // dial and per-message handling bound
	RedeliveryDelay time.Duration // pause before requeueing a failed message
}

// Subscriber consumes a quorum queue owned by Clinical. Failed deliveries are
// requeued until DeliveryLimit, then dead-lettered to <queue>.dlq.
type Subscriber struct {
	conn *amqp.Connection
	ch   *amqp.Channel
	opts Options
}

// NewSubscriber declares the DLQ, the quorum queue and its bindings idempotently.
func NewSubscriber(o Options) (*Subscriber, error) {
	conn, err := amqp.DialConfig(o.URL, amqp.Config{Dial: amqp.DefaultDial(o.Timeout)})
	if err != nil {
		return nil, fmt.Errorf("broker dial: %w", err)
	}
	ch, err := conn.Channel()
	if err == nil {
		err = declare(ch, o)
	}
	if err == nil {
		err = ch.Qos(o.Prefetch, 0, false)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("broker topology: %w", err)
	}
	return &Subscriber{conn: conn, ch: ch, opts: o}, nil
}

func declare(ch *amqp.Channel, o Options) error {
	if _, err := ch.QueueDeclare(o.Queue+".dlq", true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); err != nil {
		return err
	}
	args := amqp.Table{"x-queue-type": "quorum", "x-delivery-limit": o.DeliveryLimit,
		"x-dead-letter-exchange": "", "x-dead-letter-routing-key": o.Queue + ".dlq"}
	if _, err := ch.QueueDeclare(o.Queue, true, false, false, false, args); err != nil {
		return err
	}
	for _, b := range o.Bindings {
		if err := ch.ExchangeDeclare(b.Exchange, "topic", true, false, false, false, nil); err != nil {
			return err
		}
		if err := ch.QueueBind(o.Queue, b.RoutingKey, b.Exchange, false, nil); err != nil {
			return err
		}
	}
	return nil
}

// Close releases the channel and the connection.
func (s *Subscriber) Close() error { return s.conn.Close() }

// envelope is the received v1 envelope (07-api/contracts/events).
type envelope struct {
	EventID     string         `json:"eventId"`
	EventType   string         `json:"eventType"`
	AggregateID string         `json:"aggregateId"`
	OccurredAt  time.Time      `json:"occurredAt"`
	Version     int            `json:"version"`
	Payload     map[string]any `json:"payload"`
	Metadata    struct {
		CorrelationID string `json:"correlationId"`
		CausationID   string `json:"causationId"`
	} `json:"metadata"`
}

// Run delivers each message to handle until ctx ends. Malformed messages are
// rejected to the DLQ without business mutation; an error requeues the message.
func (s *Subscriber) Run(ctx context.Context, handle func(context.Context, in.IncomingEvent) error) error {
	deliveries, err := s.ch.ConsumeWithContext(ctx, s.opts.Queue, "", false, false, false, false, nil)
	if err != nil {
		return err
	}
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return errors.New("delivery channel closed")
			}
			s.process(ctx, d, handle)
		}
	}
}

func (s *Subscriber) process(ctx context.Context, d amqp.Delivery, handle func(context.Context, in.IncomingEvent) error) {
	var e envelope
	if err := json.Unmarshal(d.Body, &e); err != nil || e.EventID == "" || e.EventType == "" {
		_ = d.Reject(false)
		return
	}
	msgCtx, cancel := context.WithTimeout(ctx, s.opts.Timeout)
	defer cancel()
	err := handle(msgCtx, in.IncomingEvent{EventID: e.EventID, EventType: e.EventType, EventVersion: e.Version, AggregateID: e.AggregateID,
		CorrelationID: e.Metadata.CorrelationID, CausationID: e.Metadata.CausationID, Payload: e.Payload, OccurredAt: e.OccurredAt.UTC()})
	if err == nil {
		_ = d.Ack(false)
		return
	}
	select {
	case <-time.After(s.opts.RedeliveryDelay):
	case <-ctx.Done():
	}
	_ = d.Nack(false, true)
}
