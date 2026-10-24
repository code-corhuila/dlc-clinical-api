package config

import (
	"fmt"
	"time"
)

// Reconciler holds the write-intent reconciler settings (ADR-017); every
// bound is explicit. It needs the Appointments gate and the Billing freeze.
type Reconciler struct {
	MongoURI         string
	MongoDatabase    string
	MongoMaxPoolSize int
	MongoTimeout     time.Duration
	AppointmentsURL  string
	BillingURL       string
	ServiceToken     string
	ServiceTokenFile string
	// Owner credentials (authentication.md); each falls back to the shared one.
	AppointmentsTokenFile string
	BillingTokenFile      string
	InternalTimeout       time.Duration
	Interval              time.Duration
	RunTimeout            time.Duration
	MinAge                time.Duration // keeps runs away from requests still in flight
	BatchSize             int
	ShutdownTimeout       time.Duration
}

// LoadReconciler builds the reconciler configuration from lookup (os.LookupEnv).
func LoadReconciler(lookup func(string) (string, bool)) (Reconciler, error) {
	var base Config
	if err := loadMongo(lookup, &base); err != nil {
		return Reconciler{}, err
	}
	if err := loadOwners(lookup, &base); err != nil {
		return Reconciler{}, err
	}
	if base.AppointmentsURL == "" || base.BillingURL == "" {
		return Reconciler{}, fmt.Errorf("APPOINTMENTS_INTERNAL_URL and BILLING_INTERNAL_URL are required: the reconciler resolves guards and freezes")
	}
	r := Reconciler{MongoURI: base.MongoURI, MongoDatabase: base.MongoDatabase, MongoMaxPoolSize: base.MongoMaxPoolSize, MongoTimeout: base.MongoTimeout,
		AppointmentsURL: base.AppointmentsURL, BillingURL: base.BillingURL, ServiceToken: base.ServiceToken, ServiceTokenFile: base.ServiceTokenFile,
		AppointmentsTokenFile: base.AppointmentsTokenFile, BillingTokenFile: base.BillingTokenFile,
		InternalTimeout: base.InternalTimeout}
	for _, d := range []struct {
		key    string
		target *time.Duration
		value  time.Duration
	}{
		{"RECONCILE_INTERVAL", &r.Interval, 30 * time.Second},
		{"RECONCILE_RUN_TIMEOUT", &r.RunTimeout, 30 * time.Second},
		{"RECONCILE_MIN_AGE", &r.MinAge, 2 * time.Minute},
		{"SHUTDOWN_TIMEOUT", &r.ShutdownTimeout, 20 * time.Second},
	} {
		v, err := positiveDuration(lookup, d.key, d.value)
		if err != nil {
			return Reconciler{}, err
		}
		*d.target = v
	}
	var err error
	if r.BatchSize, err = boundedInt(lookup, "RECONCILE_BATCH_SIZE", 50, 1, 500); err != nil {
		return Reconciler{}, err
	}
	return r, nil
}
