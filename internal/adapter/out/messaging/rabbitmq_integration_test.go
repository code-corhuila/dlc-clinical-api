package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// newTestPublisher connects to a disposable broker. Without TEST_RABBITMQ_URL
// the integration tests are skipped, never failed (Anexo C).
func newTestPublisher(t *testing.T) (*Publisher, *amqp.Channel) {
	t.Helper()
	url := os.Getenv("TEST_RABBITMQ_URL")
	if url == "" {
		t.Skip("TEST_RABBITMQ_URL not set; integration test skipped")
	}
	p, err := NewPublisher(url, "clinical.events.test", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	conn, err := amqp.Dial(url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	ch, err := conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	return p, ch
}

func envelope(id, eventType string) out.Envelope {
	return out.Envelope{EventID: id, EventType: eventType, AggregateID: "rec-1", AggregateType: "ClinicalRecord", OccurredAt: "2026-10-23T12:00:00Z",
		Version: 1, Producer: "clinical-service", Payload: map[string]any{"consultationId": "c-1"},
		Metadata: out.Metadata{CorrelationID: "corr-1", CausationID: "corr-1", ActorUserID: "dentist-1"}}
}

func TestPublisherDeliversAConfirmedPersistentEnvelope(t *testing.T) {
	p, ch := newTestPublisher(t)
	q, err := ch.QueueDeclare("", false, true, true, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.QueueBind(q.Name, "ConsultationRecorded", "clinical.events.test", false, nil); err != nil {
		t.Fatal(err)
	}
	if err := p.Publish(context.Background(), envelope("ev-1", "ConsultationRecorded")); err != nil {
		t.Fatalf("confirmed publish failed: %v", err)
	}
	msg, ok, err := ch.Get(q.Name, true)
	if err != nil || !ok {
		t.Fatalf("message not delivered: %v", err)
	}
	if msg.MessageId != "ev-1" || msg.CorrelationId != "corr-1" || msg.Type != "ConsultationRecorded" || msg.DeliveryMode != amqp.Persistent || msg.ContentType != "application/json" {
		t.Fatalf("message properties = %+v", msg)
	}
	var got out.Envelope
	if err := json.Unmarshal(msg.Body, &got); err != nil || got.EventID != "ev-1" || got.Metadata.ActorUserID != "dentist-1" || got.Payload["consultationId"] != "c-1" {
		t.Fatalf("body = %s %v", msg.Body, err)
	}
}

func TestPublisherReportsUnroutableAsPermanent(t *testing.T) {
	p, _ := newTestPublisher(t)
	err := p.Publish(context.Background(), envelope("ev-2", "NobodyListensToThis"))
	if !errors.Is(err, out.ErrPermanentPublish) {
		t.Fatalf("an unroutable event must be permanent, got %v", err)
	}
}
