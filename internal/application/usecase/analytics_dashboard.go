package usecase

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// DashboardService implements in.ClinicalAnalytics (HU-CLN-003). It reads
// projections only and never enables an operational action.
type DashboardService struct {
	Analytics out.AnalyticsReader
	Clock     out.Clock
	Location  *time.Location // clinic timezone
}

var _ in.ClinicalAnalytics = (*DashboardService)(nil)

// dashboardScope applies the approved visibility: Administrator and Assistant
// see the whole clinic, a Dentist only own work; revenue is Administrator only.
func dashboardScope(a in.Actor) (dentistID string, revenue bool, err error) {
	switch {
	case slices.Contains(a.Roles, RoleAdministrator):
		return "", true, nil
	case slices.Contains(a.Roles, RoleDentist):
		return a.ID, false, nil
	case slices.Contains(a.Roles, RoleAssistant):
		return "", false, nil
	}
	return "", false, forbidden()
}

func (s *DashboardService) Dashboard(ctx context.Context, actor in.Actor) (in.ClinicalDashboard, error) {
	dentistID, revenue, err := dashboardScope(actor)
	if err != nil {
		return in.ClinicalDashboard{}, err
	}
	asOf, built, err := s.Analytics.LatestEvent(ctx)
	if err != nil {
		return in.ClinicalDashboard{}, err
	}
	if !built {
		return in.ClinicalDashboard{}, unavailable("clinical analytics", errors.New("no projection has been built yet"))
	}
	now := s.Clock.Now()
	d := in.ClinicalDashboard{AsOf: asOf}
	if d.TodayAppointments, d.AppointmentsDeltaVsPrev, err = s.today(ctx, now, dentistID); err != nil {
		return in.ClinicalDashboard{}, err
	}
	if d.WeeklyActivity, err = s.week(ctx, now, dentistID); err != nil {
		return in.ClinicalDashboard{}, err
	}
	if revenue {
		if d.MonthlyRevenue, err = s.revenue(ctx, now); err != nil {
			return in.ClinicalDashboard{}, err
		}
	}
	return d, nil
}

func (s *DashboardService) today(ctx context.Context, now time.Time, dentistID string) (count, delta int, err error) {
	local := now.In(s.Location)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, s.Location)
	today, err := s.Analytics.CountAppointments(ctx, start, start.AddDate(0, 0, 1), dentistID)
	if err != nil {
		return 0, 0, err
	}
	yesterday, err := s.Analytics.CountAppointments(ctx, start.AddDate(0, 0, -1), start, dentistID)
	if err != nil {
		return 0, 0, err
	}
	return today, today - yesterday, nil
}

func (s *DashboardService) week(ctx context.Context, now time.Time, dentistID string) ([]in.DayCount, error) {
	monday, sunday := model.ClinicWeek(now, s.Location)
	counts, err := s.Analytics.ActivityBetween(ctx, model.ClinicDay(monday, s.Location), model.ClinicDay(sunday, s.Location), dentistID)
	if err != nil {
		return nil, err
	}
	week := make([]in.DayCount, 0, 7)
	for day := monday; !day.After(sunday); day = day.AddDate(0, 0, 1) {
		key := model.ClinicDay(day, s.Location)
		week = append(week, in.DayCount{Day: key, Count: counts[key]})
	}
	return week, nil
}

func (s *DashboardService) revenue(ctx context.Context, now time.Time) (*in.MonthlyRevenue, error) {
	local := now.In(s.Location)
	first := time.Date(local.Year(), local.Month(), 1, 0, 0, 0, 0, s.Location)
	current, _, err := s.Analytics.CollectedRevenue(ctx, model.ClinicMonth(first, s.Location))
	if err != nil {
		return nil, err
	}
	previous, _, err := s.Analytics.CollectedRevenue(ctx, model.ClinicMonth(first.AddDate(0, -1, 0), s.Location))
	if err != nil {
		return nil, err
	}
	r := &in.MonthlyRevenue{AmountCents: current}
	if change, ok := model.ChangePercent(current, previous); ok {
		r.ChangePercent = &change
	}
	return r, nil
}
