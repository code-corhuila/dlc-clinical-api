package config

import (
	"testing"
	"time"
)

func TestLoadConsumerDeclaresEveryLimit(t *testing.T) {
	cfg, err := LoadConsumer(relayEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Consumer{MongoURI: "mongodb://db:27017/?replicaSet=rs0", MongoDatabase: "clinical", MongoMaxPoolSize: 10, MongoTimeout: 5 * time.Second,
		BrokerURL: "amqp://broker:5672/", Queue: "clinical.closure-outcomes", DeliveryLimit: 10, Prefetch: 10,
		HandlerTimeout: 10 * time.Second, RedeliveryDelay: 2 * time.Second, ShutdownTimeout: 20 * time.Second}
	if cfg != want {
		t.Fatalf("got %+v\nwant %+v", cfg, want)
	}
}

func TestLoadConsumerRejectsUnboundedValues(t *testing.T) {
	for name, values := range map[string]map[string]string{
		"no broker":        {"RABBITMQ_URL": ""},
		"zero limit":       {"INBOX_DELIVERY_LIMIT": "0"},
		"huge prefetch":    {"INBOX_PREFETCH": "5000"},
		"no handler bound": {"INBOX_HANDLER_TIMEOUT": "0s"},
		"empty queue":      {"INBOX_QUEUE": ""},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadConsumer(relayEnv(values)); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}
