package usecase

import (
	"context"
	"errors"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// GuardedWrites runs a Clinical write that creates or appends patient clinical
// data under the ADR-017 §3 sequence: intent → encounter admission → activity
// guard → verify assignment and active patient → commit tagged with the
// operationId → resolve the guard. Every outcome the service cannot prove
// stays PENDING for the reconciler; nothing is released by time.
type GuardedWrites struct {
	Intents    out.IntentStore
	Admissions out.AdmissionStore
	Gate       out.PatientGate
	Care       out.CareAuthority
	Patients   out.PatientDirectory
	Clock      out.Clock
	IDs        out.IDGenerator
}

// GuardedWrite describes one write. Commit receives a context carrying the
// write tag and must perform the repository write with it.
type GuardedWrite struct {
	Actor     in.Actor
	PatientID string
	Encounter *out.EncounterKey // nil when the write belongs to no encounter
	Commit    func(ctx context.Context) error
}

func inactivePatient() error {
	return &model.Error{Kind: model.KindRule, Code: "BUSINESS_RULE_VIOLATION", Message: "only active patients can receive new clinical data"}
}

// Run executes w and returns Commit's error, or the reason it never ran.
func (g *GuardedWrites) Run(ctx context.Context, w GuardedWrite) error {
	if g == nil {
		return unavailable("guarded writes", errors.New("not configured"))
	}
	now := g.Clock.Now()
	intent := out.WriteIntent{OperationID: g.IDs.NewID(), Kind: out.IntentGuardedWrite, PatientID: w.PatientID, State: out.IntentPending, CreatedAt: now, UpdatedAt: now}
	if w.Encounter != nil {
		intent.Encounter = *w.Encounter
	}
	if err := g.Intents.SaveIntent(ctx, intent); err != nil {
		return err
	}
	op := intent.OperationID
	if w.Encounter != nil {
		if err := g.Admissions.Admit(ctx, *w.Encounter, op); err != nil {
			g.abort(ctx, w, op, out.Guard{})
			if errors.Is(err, out.ErrEncounterFrozen) {
				return careStateConflict()
			}
			return err
		}
	}
	guard, err := g.Gate.AcquireActivityGuard(ctx, w.PatientID, op)
	switch {
	case errors.Is(err, out.ErrOwnerConflict), errors.Is(err, out.ErrNotFound):
		g.abort(ctx, w, op, out.Guard{})
		return inactivePatient()
	case err != nil:
		// The guard may exist: the reconciler re-acquires it by operationId.
		return unavailable("patient gate", err)
	}
	if err := g.Intents.AttachGuard(ctx, op, guard.ID); err != nil {
		return err
	}
	if err := g.verify(ctx, w); err != nil {
		if !errors.Is(err, out.ErrUnavailable) {
			g.abort(ctx, w, op, guard)
		}
		return err
	}
	tag := out.WriteTag{OperationID: op, Encounter: w.Encounter}
	if err := w.Commit(out.WithWriteTag(ctx, tag)); err != nil {
		if stored, rerr := g.Intents.GetIntent(ctx, op); rerr == nil && !stored.Committed {
			g.abort(ctx, w, op, guard)
		} else if rerr == nil {
			g.finalize(ctx, op, guard)
		}
		return err
	}
	g.finalize(ctx, op, guard)
	return nil
}

// verify repeats, under the guard, the checks that deactivation or a
// reassignment could have invalidated since the request was authorized.
func (g *GuardedWrites) verify(ctx context.Context, w GuardedWrite) error {
	if err := authorizePatient(ctx, g.Care, w.Actor, w.PatientID, accessWrite); err != nil {
		return err
	}
	active, err := g.Patients.IsActive(ctx, w.PatientID)
	if err != nil {
		return unavailable("patient directory", err)
	}
	if !active {
		return inactivePatient()
	}
	return nil
}

// finalize resolves the guard after a committed write. A failed resolution
// keeps the intent PENDING; the reconciler finishes it from the commit mark.
func (g *GuardedWrites) finalize(ctx context.Context, op string, guard out.Guard) {
	if g.Gate.ResolveGuard(ctx, guard.ID, guard.Token, out.GuardFinalized) == nil {
		_ = g.Intents.FinishIntent(ctx, op, out.IntentCompleted)
	}
}

// abort ends a write proven not to commit: its admission, its guard (when
// acquired) and its intent. A failed step leaves the intent to the reconciler.
func (g *GuardedWrites) abort(ctx context.Context, w GuardedWrite, op string, guard out.Guard) {
	if w.Encounter != nil && g.Admissions.AbortAdmission(ctx, *w.Encounter, op) != nil {
		return
	}
	if guard.ID != "" && g.Gate.ResolveGuard(ctx, guard.ID, guard.Token, out.GuardAborted) != nil {
		return
	}
	_ = g.Intents.FinishIntent(ctx, op, out.IntentAborted)
}
