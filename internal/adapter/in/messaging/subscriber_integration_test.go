package messaging

import (
	"context"
	"errors"
	"os"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
)

const appointmentEvent = `{"eventId":"ev-1","eventType":"AppointmentCompleted","aggregateId":"apt-1","aggregateType":"Appointment","occurredAt":"2026-10-23T12:00:00Z","version":1,"producer":"appointments-service","payload":{"appointmentId":"apt-1","patientId":"pat-1"},"metadata":{"correlationId":"corr-1","causationId":"cause-1"}}`

type harness struct {
	url, queue string
	ch         *amqp.Channel
}

func newHarness(t *testing.T) harness {
	t.Helper()
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("TEST_RABBITMQ_URL not set; integration test skipped")
	}
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	return harness{url: url, queue: "clinical.test." + t.Name(), ch: ch}
}

func (h harness) subscriber(t *testing.T) *Subscriber {
	t.Helper()
	s, err := NewSubscriber(Options{URL: h.url, Queue: h.queue, DeliveryLimit: 2, Prefetch: 1, Timeout: 5 * time.Second,
		Bindings: []Binding{{Exchange: "test.appointments.events", RoutingKey: "AppointmentCompleted"}, {Exchange: "test.billing.events", RoutingKey: "EncounterChargesCompleted"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = s.Close()
		_, _ = h.ch.QueueDelete(h.queue, false, false, false)
		_, _ = h.ch.QueueDelete(h.queue+".dlq", false, false, false)
	})
	return s
}

func (h harness) publish(t *testing.T, exchange, key, body string) {
	t.Helper()
	if err := h.ch.PublishWithContext(context.Background(), exchange, key, false, false, amqp.Publishing{ContentType: "application/json", Body: []byte(body)}); err != nil {
		t.Fatal(err)
	}
}

func (h harness) dlqDepth(t *testing.T) int {
	t.Helper()
	q, err := h.ch.QueueDeclarePassive(h.queue+".dlq", true, false, false, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	return q.Messages
}

func runFor(s *Subscriber, d time.Duration, handle func(context.Context, in.IncomingEvent) error) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	_ = s.Run(ctx, handle)
}

func TestSubscriberDecodesTheEnvelopeAndAcknowledges(t *testing.T) {
	h := newHarness(t)
	s := h.subscriber(t)
	h.publish(t, "test.appointments.events", "AppointmentCompleted", appointmentEvent)
	var got in.IncomingEvent
	runFor(s, 2*time.Second, func(_ context.Context, e in.IncomingEvent) error { got = e; return nil })
	if got.EventID != "ev-1" || got.EventType != "AppointmentCompleted" || got.EventVersion != 1 || got.CorrelationID != "corr-1" ||
		got.CausationID != "cause-1" || got.Payload["appointmentId"] != "apt-1" || !got.OccurredAt.Equal(time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)) {
		t.Fatalf("event = %+v", got)
	}
	if q, _ := h.ch.QueueDeclarePassive(h.queue, true, false, false, false, amqp.Table{"x-queue-type": "quorum"}); q.Messages != 0 {
		t.Fatal("an applied event is acknowledged")
	}
}

func TestSubscriberQuarantinesMalformedMessages(t *testing.T) {
	h := newHarness(t)
	s := h.subscriber(t)
	h.publish(t, "test.billing.events", "EncounterChargesCompleted", `{not json`)
	var calls atomic.Int32
	runFor(s, 2*time.Second, func(context.Context, in.IncomingEvent) error { calls.Add(1); return nil })
	if calls.Load() != 0 || h.dlqDepth(t) != 1 {
		t.Fatalf("a malformed message goes to the DLQ without business mutation (calls %d)", calls.Load())
	}
}

func TestSubscriberRetriesThenDeadLetters(t *testing.T) {
	h := newHarness(t)
	s := h.subscriber(t)
	h.publish(t, "test.appointments.events", "AppointmentCompleted", appointmentEvent)
	var calls atomic.Int32
	runFor(s, 4*time.Second, func(context.Context, in.IncomingEvent) error {
		calls.Add(1)
		return errors.New("closure not declared yet")
	})
	if calls.Load() < 2 || h.dlqDepth(t) != 1 {
		t.Fatalf("a failing event is redelivered a bounded number of times, then dead-lettered (calls %d)", calls.Load())
	}
}
