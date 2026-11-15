package config

import (
	"fmt"
	"time"

	// The image carries no system zoneinfo guarantee; embed it for CLINIC_TIMEZONE.
	_ "time/tzdata"
)

// AnalyticsConsumer is the HU-CLN-003 projection consumer: the inbox consumer
// limits on its own queue, so analytics never delays closure outcomes.
type AnalyticsConsumer struct {
	Consumer
	ClinicTimezone *time.Location
}

// LoadAnalyticsConsumer builds the analytics consumer configuration from lookup.
func LoadAnalyticsConsumer(lookup func(string) (string, bool)) (AnalyticsConsumer, error) {
	base, err := LoadConsumer(lookup)
	if err != nil {
		return AnalyticsConsumer{}, err
	}
	base.Queue = "clinical.analytics"
	if v, ok := lookup("ANALYTICS_QUEUE"); ok {
		if v == "" {
			return AnalyticsConsumer{}, fmt.Errorf("ANALYTICS_QUEUE must not be empty")
		}
		base.Queue = v
	}
	loc, err := loadClinicTimezone(lookup)
	if err != nil {
		return AnalyticsConsumer{}, err
	}
	return AnalyticsConsumer{Consumer: base, ClinicTimezone: loc}, nil
}

// loadClinicTimezone reads CLINIC_TIMEZONE (design-decisions.md: initial
// deployment default America/Bogota); days and months are counted in it.
func loadClinicTimezone(lookup func(string) (string, bool)) (*time.Location, error) {
	name := "America/Bogota"
	if v, ok := lookup("CLINIC_TIMEZONE"); ok {
		name = v
	}
	loc, err := time.LoadLocation(name)
	if err != nil || name == "" {
		return nil, fmt.Errorf("CLINIC_TIMEZONE %q is not a known IANA timezone", name)
	}
	return loc, nil
}
