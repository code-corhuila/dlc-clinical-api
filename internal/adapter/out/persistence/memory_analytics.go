package persistence

import (
	"context"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var (
	_ out.AnalyticsProjections = (*Memory)(nil)
	_ out.AnalyticsReader      = (*Memory)(nil)
)

// analytics holds the HU-CLN-003 projections of the memory store.
type analytics struct {
	facts     map[string]model.AppointmentFact
	processed map[string]out.ProcessedEvent
	activity  map[[2]string]int // {day, dentistId}
	revenue   map[string]int64
}

func (m *Memory) analytics() *analytics {
	if m.views == nil {
		m.views = &analytics{facts: map[string]model.AppointmentFact{}, processed: map[string]out.ProcessedEvent{},
			activity: map[[2]string]int{}, revenue: map[string]int64{}}
	}
	return m.views
}

func (m *Memory) FindAppointmentFact(_ context.Context, id string) (model.AppointmentFact, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	f, ok := m.analytics().facts[id]
	return f, ok, nil
}

// Project applies the receipt and its changes together, checking first.
func (m *Memory) Project(_ context.Context, p out.Projection) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.analytics()
	if _, seen := a.processed[p.Event.EventID]; seen {
		return out.ErrDuplicateEvent
	}
	if p.Fact != nil && a.facts[p.Fact.AppointmentID].SourceEventID != p.ExpectedSourceEventID {
		return out.ErrVersionConflict
	}
	a.processed[p.Event.EventID] = p.Event
	if p.Fact != nil {
		a.facts[p.Fact.AppointmentID] = *p.Fact
	}
	if p.Activity != nil {
		a.activity[[2]string{p.Activity.Day, p.Activity.DentistID}]++
	}
	if p.Revenue != nil {
		a.revenue[p.Revenue.Month] += p.Revenue.Cents
	}
	return nil
}

func (m *Memory) CountAppointments(_ context.Context, from, to time.Time, dentistID string) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, f := range m.analytics().facts {
		if f.Counted() && !f.StartAt.Before(from) && f.StartAt.Before(to) && (dentistID == "" || f.DentistID == dentistID) {
			n++
		}
	}
	return n, nil
}

func (m *Memory) ActivityBetween(_ context.Context, fromDay, toDay, dentistID string) (map[string]int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	counts := map[string]int{}
	for key, n := range m.analytics().activity {
		if key[0] >= fromDay && key[0] <= toDay && (dentistID == "" || key[1] == dentistID) {
			counts[key[0]] += n
		}
	}
	return counts, nil
}

func (m *Memory) CollectedRevenue(_ context.Context, month string) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cents, ok := m.analytics().revenue[month]
	return cents, ok, nil
}

func (m *Memory) LatestEvent(context.Context) (time.Time, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest time.Time
	for _, e := range m.analytics().processed {
		if e.OccurredAt.After(latest) {
			latest = e.OccurredAt
		}
	}
	return latest, !latest.IsZero(), nil
}
