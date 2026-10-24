package persistence

import (
	"context"
	"slices"
	"sort"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// cloneTreatment copies the procedures so callers never share the stored slice.
func cloneTreatment(t model.Treatment) model.Treatment {
	t.Procedures = append([]model.Procedure(nil), t.Procedures...)
	return t
}

// CreateTreatment stores treatment, key and events together or none.
func (m *Memory) CreateTreatment(_ context.Context, t model.Treatment, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := keyScope(idem.ActorID, idem.Operation, idem.Key)
	if _, ok := m.keys[scope]; ok {
		return out.ErrDuplicateIdempotency
	}
	m.treats[t.ID], m.keys[scope] = cloneTreatment(t), idem
	m.outbox = append(m.outbox, events...)
	return nil
}

// UpdateTreatment replaces t only when the stored version is still fromVersion.
func (m *Memory) UpdateTreatment(_ context.Context, t model.Treatment, fromVersion int, idem out.IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := keyScope(idem.ActorID, idem.Operation, idem.Key)
	if _, ok := m.keys[scope]; ok {
		return out.ErrDuplicateIdempotency
	}
	if stored, ok := m.treats[t.ID]; !ok || stored.Version != fromVersion {
		return out.ErrVersionConflict
	}
	m.treats[t.ID], m.keys[scope] = cloneTreatment(t), idem
	return nil
}

func (m *Memory) GetTreatment(_ context.Context, id string) (model.Treatment, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.treats[id]
	if !ok {
		return model.Treatment{}, out.ErrNotFound
	}
	return cloneTreatment(t), nil
}

// ListTreatments returns newest first (ties by id) within the window.
func (m *Memory) ListTreatments(_ context.Context, f out.RecordFilter) ([]model.Treatment, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []model.Treatment
	for _, t := range m.treats {
		if f.PatientIDs == nil || slices.Contains(f.PatientIDs, t.PatientID) {
			matched = append(matched, cloneTreatment(t))
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})
	if f.Offset >= len(matched) {
		return []model.Treatment{}, len(matched), nil
	}
	return matched[f.Offset:min(f.Offset+f.Limit, len(matched))], len(matched), nil
}
