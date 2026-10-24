// Package messaging adapts the broker (RabbitMQ) to the outbound ports.
package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

var _ out.Publisher = (*Publisher)(nil)

// Publisher publishes envelopes to a durable topic exchange with the event
// type as routing key (events.md), waiting for the broker confirmation.
type Publisher struct {
	mu       sync.Mutex
	url      string
	conn     *amqp.Connection
	ch       *amqp.Channel
	returns  chan amqp.Return
	exchange string
	timeout  time.Duration
}

// NewPublisher connects, declares the exchange idempotently and enables
// publisher confirms; timeout bounds each publication.
func NewPublisher(url, exchange string, timeout time.Duration) (*Publisher, error) {
	p := &Publisher{url: url, exchange: exchange, timeout: timeout}
	if err := p.connect(); err != nil {
		return nil, err
	}
	return p, nil
}

// connect dials, opens a confirming channel and declares the exchange.
func (p *Publisher) connect() error {
	conn, err := amqp.DialConfig(p.url, amqp.Config{Dial: amqp.DefaultDial(p.timeout)})
	if err != nil {
		return fmt.Errorf("broker dial: %w", err)
	}
	ch, err := conn.Channel()
	if err == nil {
		err = ch.ExchangeDeclare(p.exchange, "topic", true, false, false, false, nil)
	}
	if err == nil {
		err = ch.Confirm(false)
	}
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("broker channel: %w", err)
	}
	p.conn, p.ch, p.returns = conn, ch, ch.NotifyReturn(make(chan amqp.Return, 1))
	return nil
}

// Close releases the channel and the connection.
func (p *Publisher) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.conn.Close()
}

// Publish sends a persistent, mandatory message and returns once the broker
// confirms it. An unroutable message (usually a consumer not deployed yet), a
// nack, a closed channel or a timeout are transient and retried by the relay.
func (p *Publisher) Publish(ctx context.Context, e out.Envelope) error {
	body, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("encode envelope: %v: %w", err, out.ErrPermanentPublish)
	}
	ctx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()
	p.mu.Lock()
	defer p.mu.Unlock()
	// A broker restart or missed heartbeats close the connection: reconnect
	// instead of failing every event until the relay gives up on it.
	if p.conn.IsClosed() || p.ch.IsClosed() {
		_ = p.conn.Close()
		if err := p.connect(); err != nil {
			return fmt.Errorf("reconnect: %w", err)
		}
	}
	confirm, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, p.exchange, e.EventType, true, false, amqp.Publishing{
		ContentType: "application/json", DeliveryMode: amqp.Persistent, MessageId: e.EventID, CorrelationId: e.Metadata.CorrelationID,
		Type: e.EventType, AppId: e.Producer, Timestamp: time.Now().UTC(), Body: body,
	})
	if err != nil {
		return fmt.Errorf("publish: %w", err)
	}
	acked, err := confirm.WaitContext(ctx)
	if err != nil {
		return fmt.Errorf("confirm: %w", err)
	}
	// A mandatory message without a route is returned before its confirmation.
	select {
	case r := <-p.returns:
		if r.MessageId == e.EventID {
			return fmt.Errorf("no route for %s (%s)", e.EventType, r.ReplyText)
		}
	default:
	}
	if !acked {
		return errors.New("broker rejected the message")
	}
	return nil
}
