package persistence

import (
	"context"
	"sort"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// CreateEntry stores the entry, its key and its events together or none.
func (m *Memory) CreateEntry(ctx context.Context, e model.Entry, idem out.IdempotencyRecord, events []out.OutboxEvent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := keyScope(idem.ActorID, idem.Operation, idem.Key)
	if _, ok := m.keys[scope]; ok {
		return out.ErrDuplicateIdempotency
	}
	if err := m.applyWriteTag(ctx); err != nil {
		return err
	}
	m.entries[e.ID] = e
	m.keys[scope] = idem
	m.outbox = append(m.outbox, events...)
	return nil
}

func (m *Memory) GetEntry(_ context.Context, id string) (model.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.entries[id]
	if !ok {
		return model.Entry{}, out.ErrNotFound
	}
	return e, nil
}

// ListEntries returns a record's entries oldest first (ties by id).
func (m *Memory) ListEntries(_ context.Context, recordID string, offset, limit int) ([]model.Entry, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []model.Entry
	for _, e := range m.entries {
		if e.RecordID == recordID {
			matched = append(matched, e)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.Before(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	if offset >= len(matched) {
		return []model.Entry{}, len(matched), nil
	}
	return matched[offset:min(offset+limit, len(matched))], len(matched), nil
}

// Outbox returns a copy of the stored events, for tests and local inspection.
func (m *Memory) Outbox() []out.OutboxEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]out.OutboxEvent(nil), m.outbox...)
}
