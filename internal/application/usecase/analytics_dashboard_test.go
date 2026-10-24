package usecase

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// fakeReader records the scope each query received.
type fakeReader struct {
	latest   time.Time
	built    bool
	revenue  map[string]int64
	scopes   []string
	activity map[string]int
}

func (f *fakeReader) CountAppointments(_ context.Context, from, _ time.Time, dentist string) (int, error) {
	f.scopes = append(f.scopes, dentist)
	if from.Day() == 23 {
		return 12, nil
	}
	return 9, nil
}

func (f *fakeReader) ActivityBetween(_ context.Context, from, to, dentist string) (map[string]int, error) {
	f.scopes = append(f.scopes, dentist)
	return f.activity, nil
}

func (f *fakeReader) CollectedRevenue(_ context.Context, month string) (int64, bool, error) {
	v, ok := f.revenue[month]
	return v, ok, nil
}

func (f *fakeReader) LatestEvent(context.Context) (time.Time, bool, error) {
	return f.latest, f.built, nil
}

func dashboard() (*DashboardService, *fakeReader) {
	reader := &fakeReader{latest: at10, built: true, revenue: map[string]int64{"2026-10": 15000000, "2026-09": 10000000},
		activity: map[string]int{"2026-10-20": 3, "2026-10-23": 5}}
	loc, _ := time.LoadLocation("America/Bogota")
	// Friday 2026-10-23 10:00 in Bogotá.
	return &DashboardService{Analytics: reader, Clock: &fixedClock{t: time.Date(2026, 10, 23, 15, 0, 0, 0, time.UTC)}, Location: loc}, reader
}

func TestDashboardForTheAdministrator(t *testing.T) {
	svc, reader := dashboard()
	d, err := svc.Dashboard(context.Background(), admin)
	if err != nil {
		t.Fatal(err)
	}
	if d.TodayAppointments != 12 || d.AppointmentsDeltaVsPrev != 3 || !d.AsOf.Equal(at10) {
		t.Fatalf("today = %d delta %d asOf %s", d.TodayAppointments, d.AppointmentsDeltaVsPrev, d.AsOf)
	}
	want := []in.DayCount{{Day: "2026-10-19", Count: 0}, {Day: "2026-10-20", Count: 3}, {Day: "2026-10-21", Count: 0}, {Day: "2026-10-22", Count: 0}, {Day: "2026-10-23", Count: 5}, {Day: "2026-10-24", Count: 0}, {Day: "2026-10-25", Count: 0}}
	if len(d.WeeklyActivity) != 7 {
		t.Fatalf("week = %+v", d.WeeklyActivity)
	}
	for i, day := range want {
		if d.WeeklyActivity[i] != day {
			t.Fatalf("week = %+v", d.WeeklyActivity)
		}
	}
	if d.MonthlyRevenue == nil || d.MonthlyRevenue.AmountCents != 15000000 || d.MonthlyRevenue.ChangePercent == nil || *d.MonthlyRevenue.ChangePercent != 50 {
		t.Fatalf("revenue = %+v", d.MonthlyRevenue)
	}
	for _, s := range reader.scopes {
		if s != "" {
			t.Fatal("the Administrator sees the whole clinic")
		}
	}
}

func TestDashboardScopesByRole(t *testing.T) {
	svc, reader := dashboard()
	d, err := svc.Dashboard(context.Background(), dentist)
	if err != nil || d.MonthlyRevenue != nil {
		t.Fatalf("the Dentist never sees revenue: %+v %v", d.MonthlyRevenue, err)
	}
	for _, s := range reader.scopes {
		if s != dentist.ID {
			t.Fatalf("the Dentist sees only own work: %v", reader.scopes)
		}
	}
	svc, reader = dashboard()
	d, err = svc.Dashboard(context.Background(), assistant)
	if err != nil || d.MonthlyRevenue != nil || reader.scopes[0] != "" {
		t.Fatalf("the Assistant sees the whole clinic without revenue: %+v %v %v", d.MonthlyRevenue, reader.scopes, err)
	}
	if _, err := svc.Dashboard(context.Background(), in.Actor{ID: "x", Roles: []string{"PATIENT"}}); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("got %v", err)
	}
}

func TestDashboardWithoutReferenceOrProjection(t *testing.T) {
	svc, reader := dashboard()
	reader.revenue = map[string]int64{}
	d, err := svc.Dashboard(context.Background(), admin)
	if err != nil || d.MonthlyRevenue == nil || d.MonthlyRevenue.AmountCents != 0 || d.MonthlyRevenue.ChangePercent != nil {
		t.Fatalf("no payments: amount 0, changePercent omitted (PD-CLN003-04): %+v %v", d.MonthlyRevenue, err)
	}
	reader.built = false
	if _, err := svc.Dashboard(context.Background(), admin); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("no projection yet answers 503: %v", err)
	}
}
