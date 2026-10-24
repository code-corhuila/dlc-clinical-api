package config

import (
	"fmt"
	"time"
)

// Consumer holds the inbox consumer settings; every bound is explicit.
type Consumer struct {
	MongoURI         string
	MongoDatabase    string
	MongoMaxPoolSize int
	MongoTimeout     time.Duration
	BrokerURL        string
	Queue            string
	DeliveryLimit    int
	Prefetch         int
	HandlerTimeout   time.Duration
	RedeliveryDelay  time.Duration
	ShutdownTimeout  time.Duration
}

// LoadConsumer builds the inbox consumer configuration from lookup (os.LookupEnv).
func LoadConsumer(lookup func(string) (string, bool)) (Consumer, error) {
	var db Config
	if err := loadMongo(lookup, &db); err != nil {
		return Consumer{}, err
	}
	c := Consumer{MongoURI: db.MongoURI, MongoDatabase: db.MongoDatabase, MongoMaxPoolSize: db.MongoMaxPoolSize, MongoTimeout: db.MongoTimeout,
		Queue: "clinical.closure-outcomes"}
	c.BrokerURL, _ = lookup("RABBITMQ_URL")
	if c.BrokerURL == "" {
		return Consumer{}, fmt.Errorf("RABBITMQ_URL is required")
	}
	if v, ok := lookup("INBOX_QUEUE"); ok {
		if v == "" {
			return Consumer{}, fmt.Errorf("INBOX_QUEUE must not be empty")
		}
		c.Queue = v
	}
	var err error
	if c.DeliveryLimit, err = boundedInt(lookup, "INBOX_DELIVERY_LIMIT", 10, 1, 100); err != nil {
		return Consumer{}, err
	}
	if c.Prefetch, err = boundedInt(lookup, "INBOX_PREFETCH", 10, 1, 1000); err != nil {
		return Consumer{}, err
	}
	for _, d := range []struct {
		key    string
		target *time.Duration
		value  time.Duration
	}{
		{"INBOX_HANDLER_TIMEOUT", &c.HandlerTimeout, 10 * time.Second},
		{"INBOX_REDELIVERY_DELAY", &c.RedeliveryDelay, 2 * time.Second},
		{"SHUTDOWN_TIMEOUT", &c.ShutdownTimeout, 20 * time.Second},
	} {
		if *d.target, err = positiveDuration(lookup, d.key, d.value); err != nil {
			return Consumer{}, err
		}
	}
	return c, nil
}
