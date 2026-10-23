package persistence

import (
	"context"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

func (m *Memory) GetProcedure(_ context.Context, id string) (model.Procedure, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.procs[id]
	if !ok {
		return model.Procedure{}, out.ErrNotFound
	}
	return p, nil
}

// CompleteProcedure applies the completion, its extras, key and events
// together, only while the stored version is fromVersion.
func (m *Memory) CompleteProcedure(ctx context.Context, p model.Procedure, fromVersion int, extras []out.RecordedExtra, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := keyScope(idem.ActorID, idem.Operation, idem.Key)
	if _, ok := m.keys[scope]; ok {
		return out.ErrDuplicateIdempotency
	}
	if stored, ok := m.procs[p.ID]; !ok || stored.Version != fromVersion {
		return out.ErrVersionConflict
	}
	for _, e := range extras {
		if m.sources[e.SourceRecordID] {
			return out.ErrDuplicateSource
		}
	}
	if err := m.applyWriteTag(ctx); err != nil {
		return err
	}
	for _, e := range extras {
		m.sources[e.SourceRecordID] = true
	}
	m.procs[p.ID], m.keys[scope] = p, idem
	if t, ok := m.treats[p.TreatmentID]; ok {
		t = cloneTreatment(t)
		for i := range t.Procedures {
			if t.Procedures[i].ID == p.ID {
				t.Procedures[i].Status = p.Status
			}
		}
		m.treats[t.ID] = t
	}
	m.outbox = append(m.outbox, events...)
	return nil
}
