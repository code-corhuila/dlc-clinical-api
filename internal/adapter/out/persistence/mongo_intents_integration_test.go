package persistence

import (
	"errors"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// Operation ids must be UUIDs: the write_intents validator enforces it.
const (
	opWrite1 = "a1a1a1a1-0000-4000-8000-000000000001"
	opWrite2 = "a1a1a1a1-0000-4000-8000-000000000002"
	opWrite3 = "a1a1a1a1-0000-4000-8000-000000000003"
	opDecl1  = "d1d1d1d1-0000-4000-8000-000000000001"
	opDecl2  = "d1d1d1d1-0000-4000-8000-000000000002"
)

func cleanIntents(t *testing.T, m *Mongo) {
	t.Helper()
	for _, c := range []string{"write_intents", "encounter_admissions", "clinical_entries", "idempotency_keys"} {
		if _, err := m.db.Collection(c).DeleteMany(ctx, bson.D{}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMongoIntentLifecycle(t *testing.T) {
	m := newTestMongo(t)
	cleanIntents(t, m)
	if err := m.SaveIntent(ctx, intent(opWrite1, out.IntentGuardedWrite)); err != nil {
		t.Fatalf("rejected by the real validator: %v", err)
	}
	if err := m.SaveIntent(ctx, intent(opWrite1, out.IntentGuardedWrite)); !errors.Is(err, out.ErrDuplicateIdempotency) {
		t.Fatalf("one intent per operationId: %v", err)
	}
	later := intent(opDecl1, out.IntentDeclaration)
	later.CreatedAt = later.CreatedAt.Add(time.Minute)
	_ = m.SaveIntent(ctx, later)
	if err := m.AttachGuard(ctx, opWrite1, "guard-1"); err != nil {
		t.Fatal(err)
	}
	pending, err := m.PendingIntents(ctx, 10)
	if err != nil || len(pending) != 2 || pending[0].OperationID != opWrite1 || pending[0].GuardID != "guard-1" || pending[0].Encounter != encounter {
		t.Fatalf("pending = %+v %v", pending, err)
	}
	if err := m.FinishIntent(ctx, opWrite1, out.IntentCompleted); err != nil {
		t.Fatal(err)
	}
	if err := m.FinishIntent(ctx, opWrite1, out.IntentCompleted); err != nil {
		t.Fatal("finishing again with the same state is a no-op")
	}
	if err := m.FinishIntent(ctx, opWrite1, out.IntentAborted); !errors.Is(err, out.ErrIntentSettled) {
		t.Fatalf("a completed intent never aborts: %v", err)
	}
	if _, err := m.GetIntent(ctx, opWrite3); !errors.Is(err, out.ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}

func TestMongoDeclarationFreezesAdmissions(t *testing.T) {
	m := newTestMongo(t)
	cleanIntents(t, m)
	if err := m.Admit(ctx, encounter, opWrite1); err != nil {
		t.Fatalf("rejected by the real validator: %v", err)
	}
	_ = m.Admit(ctx, encounter, opWrite1)
	_ = m.Admit(ctx, encounter, opWrite2)
	_ = m.AbortAdmission(ctx, encounter, opWrite2)
	pending, err := m.BeginDeclaration(ctx, intent(opDecl1, out.IntentDeclaration))
	if err != nil || pending != 1 {
		t.Fatalf("pending = %d %v", pending, err)
	}
	if err := m.Admit(ctx, encounter, opWrite3); !errors.Is(err, out.ErrEncounterFrozen) {
		t.Fatalf("a frozen encounter admits nothing: %v", err)
	}
	if _, err := m.BeginDeclaration(ctx, intent(opDecl2, out.IntentDeclaration)); !errors.Is(err, out.ErrEncounterFrozen) {
		t.Fatalf("another declaration cannot take the freeze: %v", err)
	}
	if _, err := m.GetIntent(ctx, opDecl2); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("a rejected declaration leaves no intent")
	}
	_ = m.Unfreeze(ctx, encounter, opDecl2)
	if err := m.Admit(ctx, encounter, opWrite3); !errors.Is(err, out.ErrEncounterFrozen) {
		t.Fatal("only the holder unfreezes")
	}
	_ = m.Unfreeze(ctx, encounter, opDecl1)
	if err := m.Admit(ctx, encounter, opWrite3); err != nil {
		t.Fatalf("released encounter admits again: %v", err)
	}
}

func TestMongoTaggedWriteCommitsItsIntentAndAdmission(t *testing.T) {
	m := newTestMongo(t)
	cleanIntents(t, m)
	at := time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)
	_ = m.SaveIntent(ctx, intent(opWrite1, out.IntentGuardedWrite))
	_ = m.Admit(ctx, encounter, opWrite1)
	tagged := out.WithWriteTag(ctx, out.WriteTag{OperationID: opWrite1, Encounter: &encounter})
	if err := m.CreateEntry(tagged, kindEntry("e-tag-1", model.KindEvolution, at), entryKey("key-tag-0001", "e-tag-1", at), nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := m.GetIntent(ctx, opWrite1); !got.Committed || got.State != out.IntentPending {
		t.Fatalf("intent = %+v", got)
	}
	if pending, _ := m.BeginDeclaration(ctx, intent(opDecl1, out.IntentDeclaration)); pending != 0 {
		t.Fatalf("a committed admission no longer blocks the declaration: %d", pending)
	}
	err := m.CreateEntry(tagged, kindEntry("e-tag-2", model.KindEvolution, at), entryKey("key-tag-0002", "e-tag-2", at), nil)
	if !errors.Is(err, out.ErrIntentSettled) {
		t.Fatalf("an intent commits one write: %v", err)
	}
	if _, err := m.GetEntry(ctx, "e-tag-2"); !errors.Is(err, out.ErrNotFound) {
		t.Fatal("a rejected tagged write leaves nothing behind")
	}
	if _, found, _ := m.FindIdempotency(ctx, "d", "op", "key-tag-0002"); found {
		t.Fatal("nor its idempotency key")
	}
}
