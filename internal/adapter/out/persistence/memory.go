// Package persistence holds the outbound storage adapters. Memory backs the
// HTTP tests and local runs; MongoDB is the production adapter.
package persistence

import (
	"context"
	"slices"
	"sort"
	"sync"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// Memory is a thread-safe in-memory RecordRepository. One mutex makes each
// CreateRecord atomic, mirroring the single transaction of the real adapter.
type Memory struct {
	mu      sync.Mutex
	records map[string]model.ClinicalRecord
	keys    map[string]out.IdempotencyRecord
}

// NewMemory returns an empty repository.
func NewMemory() *Memory {
	return &Memory{records: map[string]model.ClinicalRecord{}, keys: map[string]out.IdempotencyRecord{}}
}

var _ out.RecordRepository = (*Memory)(nil)

func keyScope(actor, operation, key string) string { return actor + "\x00" + operation + "\x00" + key }

func (m *Memory) FindIdempotency(_ context.Context, actor, operation, key string) (out.IdempotencyRecord, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.keys[keyScope(actor, operation, key)]
	return rec, ok, nil
}

// CreateRecord stores the record and its key together or neither.
func (m *Memory) CreateRecord(_ context.Context, rec model.ClinicalRecord, idem out.IdempotencyRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	scope := keyScope(idem.ActorID, idem.Operation, idem.Key)
	if _, ok := m.keys[scope]; ok {
		return out.ErrDuplicateIdempotency
	}
	for _, existing := range m.records {
		if existing.PatientID == rec.PatientID {
			return out.ErrRecordExists
		}
	}
	m.records[rec.ID] = rec
	m.keys[scope] = idem
	return nil
}

func (m *Memory) GetRecord(_ context.Context, id string) (model.ClinicalRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec, ok := m.records[id]
	if !ok {
		return model.ClinicalRecord{}, out.ErrNotFound
	}
	return rec, nil
}

// ListRecords returns newest first (ties broken by id) within the window.
func (m *Memory) ListRecords(_ context.Context, f out.RecordFilter) ([]model.ClinicalRecord, int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var matched []model.ClinicalRecord
	for _, rec := range m.records {
		if f.PatientIDs == nil || slices.Contains(f.PatientIDs, rec.PatientID) {
			matched = append(matched, rec)
		}
	}
	sort.Slice(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID > matched[j].ID
	})
	total := len(matched)
	if f.Offset >= total {
		return []model.ClinicalRecord{}, total, nil
	}
	return matched[f.Offset:min(f.Offset+f.Limit, total)], total, nil
}
