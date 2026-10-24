package out

import (
	"context"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// ProcessedEvent is the receipt of one analytics source event.
type ProcessedEvent struct {
	EventID    string
	EventType  string
	OccurredAt time.Time
}

// ActivityIncrement adds one declared encounter to a dentist's clinic day.
type ActivityIncrement struct {
	Day       string // YYYY-MM-DD, clinic timezone
	DentistID string
}

// RevenueIncrement adds one recorded payment to a clinic month.
type RevenueIncrement struct {
	Month string // YYYY-MM, clinic timezone
	Cents int64
}

// Projection is everything one event changes, applied atomically with its
// receipt. Fact replaces the stored appointment fact only while it still has
// ExpectedSourceEventID ("" when none was stored), so a concurrent change is
// ErrVersionConflict and is retried.
type Projection struct {
	Event                 ProcessedEvent
	Fact                  *model.AppointmentFact
	ExpectedSourceEventID string
	Activity              *ActivityIncrement
	Revenue               *RevenueIncrement
}

// AnalyticsProjections stores the HU-CLN-003 read models (ADR-006). They are
// rebuildable from source events and have no operational authority.
type AnalyticsProjections interface {
	FindAppointmentFact(ctx context.Context, appointmentID string) (model.AppointmentFact, bool, error)
	// Project stores the receipt and the changes together; a repeated eventId
	// is ErrDuplicateEvent and changes nothing.
	Project(ctx context.Context, p Projection) error
}

// AnalyticsReader answers the dashboard from the projections. dentistID ""
// means the whole clinic.
type AnalyticsReader interface {
	// CountAppointments counts live appointments starting in [from, to).
	CountAppointments(ctx context.Context, from, to time.Time, dentistID string) (int, error)
	// ActivityBetween returns declared encounters per day, fromDay..toDay inclusive.
	ActivityBetween(ctx context.Context, fromDay, toDay, dentistID string) (map[string]int, error)
	// CollectedRevenue returns the collected cents of a month, if any were recorded.
	CollectedRevenue(ctx context.Context, month string) (int64, bool, error)
	// LatestEvent is the newest processed source event time, or false when no
	// projection has been built yet.
	LatestEvent(ctx context.Context) (time.Time, bool, error)
}
