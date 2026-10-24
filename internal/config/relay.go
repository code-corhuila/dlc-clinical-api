package config

import (
	"fmt"
	"strconv"
	"time"
)

// Relay holds the outbox relay settings; every bound is explicit (Norma
// 5.3.10 and Anexo D: bounded batches, run time and retries).
type Relay struct {
	MongoURI         string
	MongoDatabase    string
	MongoMaxPoolSize int
	MongoTimeout     time.Duration
	BrokerURL        string
	Exchange         string
	PublishTimeout   time.Duration
	BatchSize        int
	Interval         time.Duration
	RunTimeout       time.Duration
	MaxAttempts      int
	BaseDelay        time.Duration
	MaxDelay         time.Duration
	ShutdownTimeout  time.Duration
}

// LoadRelay builds the relay configuration from lookup (os.LookupEnv).
func LoadRelay(lookup func(string) (string, bool)) (Relay, error) {
	var db Config
	if err := loadMongo(lookup, &db); err != nil {
		return Relay{}, err
	}
	r := Relay{MongoURI: db.MongoURI, MongoDatabase: db.MongoDatabase, MongoMaxPoolSize: db.MongoMaxPoolSize, MongoTimeout: db.MongoTimeout,
		Exchange: "clinical.events"}
	r.BrokerURL, _ = lookup("RABBITMQ_URL")
	if r.BrokerURL == "" {
		return Relay{}, fmt.Errorf("RABBITMQ_URL is required")
	}
	if v, ok := lookup("RELAY_EXCHANGE"); ok {
		if v == "" {
			return Relay{}, fmt.Errorf("RELAY_EXCHANGE must not be empty")
		}
		r.Exchange = v
	}
	for _, d := range []struct {
		key    string
		target *time.Duration
		value  time.Duration
	}{
		{"RELAY_PUBLISH_TIMEOUT", &r.PublishTimeout, 5 * time.Second},
		{"RELAY_INTERVAL", &r.Interval, 5 * time.Second},
		{"RELAY_RUN_TIMEOUT", &r.RunTimeout, 30 * time.Second},
		{"RELAY_BASE_DELAY", &r.BaseDelay, time.Second},
		{"RELAY_MAX_DELAY", &r.MaxDelay, 5 * time.Minute},
		{"SHUTDOWN_TIMEOUT", &r.ShutdownTimeout, 20 * time.Second},
	} {
		v, err := positiveDuration(lookup, d.key, d.value)
		if err != nil {
			return Relay{}, err
		}
		*d.target = v
	}
	var err error
	if r.BatchSize, err = boundedInt(lookup, "RELAY_BATCH_SIZE", 100, 1, 1000); err != nil {
		return Relay{}, err
	}
	if r.MaxAttempts, err = boundedInt(lookup, "RELAY_MAX_ATTEMPTS", 20, 1, 50); err != nil {
		return Relay{}, err
	}
	if r.BaseDelay > r.MaxDelay {
		return Relay{}, fmt.Errorf("RELAY_BASE_DELAY must not exceed RELAY_MAX_DELAY")
	}
	return r, nil
}

func positiveDuration(lookup func(string) (string, bool), key string, def time.Duration) (time.Duration, error) {
	v, ok := lookup(key)
	if !ok {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration; zero means unlimited", key)
	}
	return d, nil
}

func boundedInt(lookup func(string) (string, bool), key string, def, lo, hi int) (int, error) {
	v, ok := lookup(key)
	if !ok {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		return 0, fmt.Errorf("%s must be an integer from %d to %d", key, lo, hi)
	}
	return n, nil
}
