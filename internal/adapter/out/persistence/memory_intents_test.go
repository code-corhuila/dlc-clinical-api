package persistence

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

var encounter = out.EncounterKey{RecordID: "rec-1", ConsultationID: "cons-1"}

func intent(op, kind string) out.WriteIntent {
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	return out.WriteIntent{OperationID: op, Kind: kind, PatientID: "pat-1", Encounter: encounter, State: out.IntentPending, CreatedAt: at, UpdatedAt: at}
}

func TestMemoryIntentLifecycle(t *testing.T) {
	m := NewMemory()
	if err := m.SaveIntent(ctx, intent("op-1", out.IntentGuardedWrite)); err != nil {
		t.Fatal(err)
	}
	if err := m.SaveIntent(ctx, intent("op-1", out.IntentGuardedWrite)); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("one intent per operationId: %v", err)
	}
	_ = m.SaveIntent(ctx, intent("op-2", out.IntentDeclaration))
	if err := m.AttachGuard(ctx, "op-1", "guard-1"); err != nil {
		t.Fatal(err)
	}
	pending, _ := m.PendingIntents(ctx, 10)
	if len(pending) != 2 || pending[0].OperationID != "op-1" || pending[0].GuardID != "guard-1" {
		t.Fatalf("pending = %+v", pending)
	}
	if err := m.FinishIntent(ctx, "op-1", out.IntentCompleted); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishIntent(ctx, "op-1", out.IntentCompleted); err != nil {
		t.Fatal("finishing again with the same state is a no-op")
	}
	if err := m.FinishIntent(ctx, "op-1", out.IntentAborted); !errors.Is(err, out.ErrIntentSettled) {
		t.Fatalf("a completed intent never aborts: %v", err)
	}
	if got, _ := m.GetIntent(ctx, "op-1"); got.State != out.IntentCompleted {
		t.Fatalf("got %+v", got)
	}
	if pending, _ := m.PendingIntents(ctx, 10); len(pending) != 1 || pending[0].OperationID != "op-2" {
		t.Fatalf("pending = %+v", pending)
	}
	if _, err := m.GetIntent(ctx, "missing"); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMemoryDeclarationFreezesAdmissions(t *testing.T) {
	m := NewMemory()
	if err := m.Admit(ctx, encounter, "op-w1"); err != nil {
		t.Fatal(err)
	}
	_ = m.Admit(ctx, encounter, "op-w2")
	_ = m.AbortAdmission(ctx, encounter, "op-w2")
	pending, err := m.BeginDeclaration(ctx, intent("op-d1", out.IntentDeclaration))
	if err != nil || pending != 1 {
		t.Fatalf("pending = %d %v", pending, err)
	}
	if _, err := m.GetIntent(ctx, "op-d1"); err != nil {
		t.Fatal("the declaration intent is saved with the freeze")
	}
	if err := m.Admit(ctx, encounter, "op-w3"); !errors.Is(err, out.ErrEncounterFrozen) {
		t.Fatalf("a frozen encounter admits nothing: %v", err)
	}
	if _, err := m.BeginDeclaration(ctx, intent("op-d2", out.IntentDeclaration)); !errors.Is(err, out.ErrEncounterFrozen) {
		t.Fatalf("another declaration cannot take the freeze: %v", err)
	}
	_ = m.Unfreeze(ctx, encounter, "op-other")
	if err := m.Admit(ctx, encounter, "op-w3"); !errors.Is(err, out.ErrEncounterFrozen) {
		t.Fatal("only the holder unfreezes")
	}
	_ = m.Unfreeze(ctx, encounter, "op-d1")
	if err := m.Admit(ctx, encounter, "op-w3"); err != nil {
		t.Fatalf("released encounter admits again: %v", err)
	}
}

func TestMemoryTaggedWriteCommitsItsIntentAndAdmission(t *testing.T) {
	m := NewMemory()
	_ = m.SaveIntent(ctx, intent("op-w1", out.IntentGuardedWrite))
	_ = m.Admit(ctx, encounter, "op-w1")
	tagged := out.WithWriteTag(ctx, out.WriteTag{OperationID: "op-w1", Encounter: &encounter})
	if err := m.CreateEntry(tagged, entry("e-1", "rec-1", time.Now()), key("k-tag-1", "e-1"), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetIntent(ctx, "op-w1"); !got.Committed || got.State != out.IntentPending {
		t.Fatalf("intent = %+v", got)
	}
	if pending, _ := m.BeginDeclaration(ctx, intent("op-d1", out.IntentDeclaration)); pending != 0 {
		t.Fatalf("a committed admission no longer blocks the declaration: %d", pending)
	}
	if err := m.CreateEntry(tagged, entry("e-2", "rec-1", time.Now()), key("k-tag-2", "e-2"), nil); !errors.Is(err, out.ErrIntentSettled) {
		t.Fatalf("an intent commits one write: %v", err)
	}
	if _, err := m.GetEntry(context.Background(), "e-2"); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("a rejected tagged write leaves nothing behind")
	}
}

func TestMemoryFenceExcludesALateCommit(t *testing.T) {
	m := NewMemory()
	_ = m.SaveIntent(ctx, intent("op-f1", out.IntentGuardedWrite))
	if committed, err := m.FenceIntent(ctx, "op-f1"); err != nil || committed {
		t.Fatalf("fence = %v %v", committed, err)
	}
	if committed, err := m.FenceIntent(ctx, "op-f1"); err != nil || committed {
		t.Fatal("fencing again is a no-op")
	}
	tagged := out.WithWriteTag(ctx, out.WriteTag{OperationID: "op-f1"})
	if err := m.CreateEntry(tagged, entry("e-f1", "rec-1", time.Now()), key("k-f1", "e-f1"), nil); !errors.Is(err, out.ErrIntentSettled) {
		t.Fatalf("a fenced intent rejects its late write: %v", err)
	}
	_ = m.SaveIntent(ctx, intent("op-f2", out.IntentGuardedWrite))
	_ = m.CreateEntry(out.WithWriteTag(ctx, out.WriteTag{OperationID: "op-f2"}), entry("e-f2", "rec-1", time.Now()), key("k-f2", "e-f2"), nil)
	if committed, err := m.FenceIntent(ctx, "op-f2"); err != nil || !committed {
		t.Fatalf("a committed intent is never fenced: %v %v", committed, err)
	}
	if w, _ := m.GetIntent(ctx, "op-f2"); w.Fenced {
		t.Fatal("and keeps no fence")
	}
}
