package httpapi

import (
	"net/http"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// dashboardResponse is ClinicalDashboard of clinical-service.yaml (HU-CLN-003).
type dashboardResponse struct {
	AsOf              string           `json:"asOf"`
	TodayAppointments todayResponse    `json:"todayAppointments"`
	WeeklyActivity    []dayResponse    `json:"weeklyActivity"`
	MonthlyRevenue    *revenueResponse `json:"monthlyRevenue,omitempty"`
}

type todayResponse struct {
	Count            int `json:"count"`
	DeltaVsYesterday int `json:"deltaVsYesterday"`
}

type dayResponse struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

type revenueResponse struct {
	Amount        string   `json:"amount"` // shared Money: exact COP decimal
	ChangePercent *float64 `json:"changePercent,omitempty"`
}

// AnalyticsRoutes exposes the read-only Clinical Analytics dashboard.
func AnalyticsRoutes(svc in.ClinicalAnalytics) []Route {
	return []Route{{Pattern: "GET /api/v1/clinical-analytics/dashboard", Handler: func(w http.ResponseWriter, r *http.Request) {
		d, err := svc.Dashboard(r.Context(), actorFrom(r))
		if err != nil {
			WriteError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, toDashboardResponse(d))
	}}}
}

func toDashboardResponse(d in.ClinicalDashboard) dashboardResponse {
	res := dashboardResponse{AsOf: d.AsOf.UTC().Format(time.RFC3339),
		TodayAppointments: todayResponse{Count: d.TodayAppointments, DeltaVsYesterday: d.AppointmentsDeltaVsPrev},
		WeeklyActivity:    make([]dayResponse, 0, len(d.WeeklyActivity))}
	for _, day := range d.WeeklyActivity {
		res.WeeklyActivity = append(res.WeeklyActivity, dayResponse{Day: day.Day, Count: day.Count})
	}
	if r := d.MonthlyRevenue; r != nil {
		res.MonthlyRevenue = &revenueResponse{Amount: model.FormatCOP(r.AmountCents), ChangePercent: r.ChangePercent}
	}
	return res
}
