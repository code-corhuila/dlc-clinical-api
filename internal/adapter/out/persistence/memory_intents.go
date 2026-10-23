package persistence

import (
	"context"
	"sort"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

var (
	_ out.IntentStore    = (*Memory)(nil)
	_ out.AdmissionStore = (*Memory)(nil)
)

// admission is the owner-local admission state of one encounter.
type admission struct {
	frozenBy string // operationId of the declaration holding the freeze
	writes   map[string]string
}

func (m *Memory) SaveIntent(_ context.Context, w out.WriteIntent) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveIntent(w)
}

func (m *Memory) saveIntent(w out.WriteIntent) error {
	if _, ok := m.intents[w.OperationID]; ok {
		return out.ErrDuplicateIdempotency
	}
	m.intents[w.OperationID] = w
	return nil
}

func (m *Memory) AttachGuard(_ context.Context, operationID, guardID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.intents[operationID]
	if !ok {
		return out.ErrNotFound
	}
	w.GuardID = guardID
	m.intents[operationID] = w
	return nil
}

func (m *Memory) FinishIntent(_ context.Context, operationID, state string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.intents[operationID]
	switch {
	case !ok:
		return out.ErrNotFound
	case w.State == state:
		return nil
	case w.State != out.IntentPending:
		return out.ErrIntentSettled
	}
	w.State = state
	m.intents[operationID] = w
	return nil
}

func (m *Memory) GetIntent(_ context.Context, operationID string) (out.WriteIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	w, ok := m.intents[operationID]
	if !ok {
		return out.WriteIntent{}, out.ErrNotFound
	}
	return w, nil
}

func (m *Memory) PendingIntents(_ context.Context, limit int) ([]out.WriteIntent, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending := []out.WriteIntent{}
	for _, w := range m.intents {
		if w.State == out.IntentPending {
			pending = append(pending, w)
		}
	}
	sort.Slice(pending, func(i, j int) bool {
		if !pending[i].CreatedAt.Equal(pending[j].CreatedAt) {
			return pending[i].CreatedAt.Before(pending[j].CreatedAt)
		}
		return pending[i].OperationID < pending[j].OperationID
	})
	if len(pending) > limit {
		pending = pending[:limit]
	}
	return pending, nil
}

func (m *Memory) encounter(key out.EncounterKey) *admission {
	a, ok := m.admissions[key]
	if !ok {
		a = &admission{writes: map[string]string{}}
		m.admissions[key] = a
	}
	return a
}

func (m *Memory) Admit(_ context.Context, key out.EncounterKey, operationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.encounter(key)
	if a.frozenBy != "" {
		return out.ErrEncounterFrozen
	}
	if _, ok := a.writes[operationID]; !ok {
		a.writes[operationID] = "ADMITTED"
	}
	return nil
}

func (m *Memory) AbortAdmission(_ context.Context, key out.EncounterKey, operationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.admissions[key]; ok && a.writes[operationID] == "ADMITTED" {
		a.writes[operationID] = "ABORTED"
	}
	return nil
}

func (m *Memory) BeginDeclaration(_ context.Context, w out.WriteIntent) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.encounter(w.Encounter)
	if a.frozenBy != "" && a.frozenBy != w.OperationID {
		return 0, out.ErrEncounterFrozen
	}
	if err := m.saveIntent(w); err != nil {
		return 0, err
	}
	a.frozenBy = w.OperationID
	pending := 0
	for _, state := range a.writes {
		if state == "ADMITTED" {
			pending++
		}
	}
	return pending, nil
}

func (m *Memory) Unfreeze(_ context.Context, key out.EncounterKey, operationID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if a, ok := m.admissions[key]; ok && a.frozenBy == operationID {
		a.frozenBy = ""
	}
	return nil
}

// applyWriteTag settles the intent and admission of a tagged write. It runs
// with m.mu held, after the write's own checks and before its first change,
// and checks everything before changing anything, so the write stays atomic.
func (m *Memory) applyWriteTag(ctx context.Context) error {
	tag, ok := out.WriteTagFrom(ctx)
	if !ok {
		return nil
	}
	w, found := m.intents[tag.OperationID]
	if !found || w.State != out.IntentPending || w.Committed {
		return out.ErrIntentSettled
	}
	var a *admission
	if tag.Encounter != nil {
		a = m.admissions[*tag.Encounter]
		if a == nil || a.writes[tag.OperationID] != "ADMITTED" {
			return out.ErrIntentSettled
		}
		a.writes[tag.OperationID] = "COMMITTED"
	}
	w.Committed = true
	if tag.Complete {
		w.State = out.IntentCompleted
	}
	m.intents[tag.OperationID] = w
	return nil
}
