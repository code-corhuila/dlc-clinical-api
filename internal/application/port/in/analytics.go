package in

import (
	"context"
	"time"
)

// DayCount is one day of the weekly activity (YYYY-MM-DD, clinic timezone).
type DayCount struct {
	Day   string
	Count int
}

// MonthlyRevenue is collected money of the current month, Administrator only.
type MonthlyRevenue struct {
	AmountCents   int64
	ChangePercent *float64 // nil without a previous-month reference
}

// ClinicalDashboard is the HU-CLN-003 snapshot (clinical-service.yaml ClinicalDashboard).
type ClinicalDashboard struct {
	AsOf                    time.Time
	TodayAppointments       int
	AppointmentsDeltaVsPrev int
	WeeklyActivity          []DayCount
	MonthlyRevenue          *MonthlyRevenue
}

// ClinicalAnalytics reads the role-scoped dashboard.
type ClinicalAnalytics interface {
	Dashboard(ctx context.Context, actor Actor) (ClinicalDashboard, error)
}
