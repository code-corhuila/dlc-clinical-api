package persistence

import (
	"context"
	"slices"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func cloneClosure(c model.CareClosure) model.CareClosure {
	c.ProcedureIDs, c.ManualChargeIDs = slices.Clone(c.ProcedureIDs), slices.Clone(c.ManualChargeIDs)
	return c
}

func (m *Memory) GetClosure(_ context.Context, id string) (model.CareClosure, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.closure[id]
	if !ok {
		return model.CareClosure{}, out.ErrNotFound
	}
	return cloneClosure(c), nil
}

func (m *Memory) FindClosureByEncounter(_ context.Context, recordID, consultationID string) (model.CareClosure, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range m.closure {
		if c.RecordID == recordID && c.ConsultationID == consultationID {
			return cloneClosure(c), true, nil
		}
	}
	return model.CareClosure{}, false, nil
}

// CompletedProcedureIDs derives the encounter's completed procedures.
func (m *Memory) CompletedProcedureIDs(_ context.Context, recordID, appointmentID string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids := []string{}
	for _, p := range m.procs {
		if p.RecordID == recordID && p.AppointmentID == appointmentID && p.Status == model.StatusCompleted {
			ids = append(ids, p.ID)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// DeclareCareCompletion stores closure, record version, key and events
// together; one closure per encounter and only from the expected version.
func (m *Memory) DeclareCareCompletion(_ context.Context, c model.CareClosure, recordFromVersion int, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := keyScope(idem.ActorID, idem.Operation, idem.Key)
	if _, ok := m.keys[scope]; ok {
		return out.ErrDuplicateIdempotency
	}
	for _, existing := range m.closure {
		if existing.RecordID == c.RecordID && existing.ConsultationID == c.ConsultationID {
			return out.ErrClosureExists
		}
	}
	rec, ok := m.records[c.RecordID]
	if !ok || rec.Version != recordFromVersion {
		return out.ErrVersionConflict
	}
	rec.Version, rec.UpdatedAt = c.ClinicalVersion, c.CompletedAt
	m.records[rec.ID], m.closure[c.ID], m.keys[scope] = rec, cloneClosure(c), idem
	m.outbox = append(m.outbox, events...)
	return nil
}
