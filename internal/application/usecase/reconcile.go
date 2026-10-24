package usecase

import (
	"context"
	"errors"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// ReconcileService settles write intents whose outcome is unknown after a
// lost owner answer or a crash (ADR-017 §2.4, §6.6). Each decision comes
// from Clinical's own commit mark, after fencing; never from a timeout. MinAge
// only keeps it away from requests still in flight.
type ReconcileService struct {
	Intents    out.IntentStore
	Admissions out.AdmissionStore
	Gate       out.PatientGate
	Billing    out.BillingAuthority
	Clock      out.Clock
	MinAge     time.Duration
	BatchSize  int
}

// ReconcileReport counts one run: settled intents and those left PENDING
// because an owner could not confirm a step.
type ReconcileReport struct {
	Completed, Aborted, Skipped int
}

// RunOnce settles up to BatchSize pending intents older than MinAge.
func (s *ReconcileService) RunOnce(ctx context.Context) (ReconcileReport, error) {
	var report ReconcileReport
	pending, err := s.Intents.PendingIntents(ctx, s.BatchSize)
	if err != nil {
		return report, err
	}
	cutoff := s.Clock.Now().Add(-s.MinAge)
	for _, w := range pending {
		if w.CreatedAt.After(cutoff) {
			continue
		}
		var state string
		if w.Kind == out.IntentDeclaration {
			state = s.settleDeclaration(ctx, w)
		} else {
			state = s.settleWrite(ctx, w)
		}
		switch state {
		case out.IntentCompleted:
			report.Completed++
		case out.IntentAborted:
			report.Aborted++
		default:
			report.Skipped++
		}
	}
	return report, nil
}

// settleWrite re-acquires the guard (a repeat returns it with a rotated
// token), fences the intent and resolves the guard from the commit mark. It
// returns the final state, or "" when the intent stays PENDING.
func (s *ReconcileService) settleWrite(ctx context.Context, w out.WriteIntent) string {
	guard, err := s.Gate.AcquireActivityGuard(ctx, w.PatientID, w.OperationID)
	noGuard := errors.Is(err, out.ErrOwnerConflict) || errors.Is(err, out.ErrNotFound)
	if err != nil && !noGuard {
		return ""
	}
	committed, err := s.Intents.FenceIntent(ctx, w.OperationID)
	if err != nil || (committed && noGuard) {
		return "" // a committed write always holds its guard; wait for the owner
	}
	if committed {
		return s.finish(ctx, w, guard, out.GuardFinalized, out.IntentCompleted)
	}
	if w.Encounter != (out.EncounterKey{}) && s.Admissions.AbortAdmission(ctx, w.Encounter, w.OperationID) != nil {
		return ""
	}
	if noGuard {
		return s.finish(ctx, w, out.Guard{}, "", out.IntentAborted)
	}
	return s.finish(ctx, w, guard, out.GuardAborted, out.IntentAborted)
}

func (s *ReconcileService) finish(ctx context.Context, w out.WriteIntent, guard out.Guard, outcome, state string) string {
	if outcome != "" && s.Gate.ResolveGuard(ctx, guard.ID, guard.Token, outcome) != nil {
		return ""
	}
	if s.Intents.FinishIntent(ctx, w.OperationID, state) != nil {
		return ""
	}
	return state
}

// settleDeclaration keeps both freezes of a committed declaration; otherwise
// Billing's freeze is released first, then the local one (§6.6).
func (s *ReconcileService) settleDeclaration(ctx context.Context, w out.WriteIntent) string {
	committed, err := s.Intents.FenceIntent(ctx, w.OperationID)
	if err != nil {
		return ""
	}
	if committed {
		return s.finish(ctx, w, out.Guard{}, "", out.IntentCompleted)
	}
	k := w.Encounter
	if s.Billing.ReleaseManualChargeFreeze(ctx, k.RecordID, k.ConsultationID, w.OperationID) != nil {
		return ""
	}
	if s.Admissions.Unfreeze(ctx, k, w.OperationID) != nil {
		return ""
	}
	return s.finish(ctx, w, out.Guard{}, "", out.IntentAborted)
}
