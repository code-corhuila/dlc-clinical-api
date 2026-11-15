package usecase

import (
	"context"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

type reconcileFixture struct {
	coord   *fakeCoordination
	billing *fakeBilling
	svc     *ReconcileService
}

func setupReconcile() reconcileFixture {
	rf := reconcileFixture{coord: newCoordination(), billing: &fakeBilling{}}
	rf.svc = &ReconcileService{Intents: rf.coord, Admissions: rf.coord, Gate: rf.coord, Billing: rf.billing,
		Clock: &fixedClock{t: t0.Add(time.Hour)}, MinAge: time.Minute, BatchSize: 10}
	return rf
}

// pending stores a PENDING intent created at t0, older than MinAge.
func (rf reconcileFixture) pending(op, kind string, committed bool) {
	rf.coord.intents[op] = out.WriteIntent{OperationID: op, Kind: kind, PatientID: "pat-1", Encounter: *anEncounter,
		GuardID: "guard-" + op, Committed: committed, State: out.IntentPending, CreatedAt: t0, UpdatedAt: t0}
}

func (rf reconcileFixture) run(t *testing.T) ReconcileReport {
	t.Helper()
	report, err := rf.svc.RunOnce(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestReconcileGuardedWrites(t *testing.T) {
	rf := setupReconcile()
	rf.pending("op-a", out.IntentGuardedWrite, true)
	rf.pending("op-b", out.IntentGuardedWrite, false)
	rf.coord.admissions["op-b"] = "ADMITTED"
	report := rf.run(t)
	a, b := rf.coord.intents["op-a"], rf.coord.intents["op-b"]
	if a.State != out.IntentCompleted || rf.coord.resolved["guard-op-a"] != out.GuardFinalized {
		t.Fatalf("committed write: %+v %v", a, rf.coord.resolved)
	}
	if b.State != out.IntentAborted || !b.Fenced || rf.coord.resolved["guard-op-b"] != out.GuardAborted || rf.coord.admissions["op-b"] != "ABORTED" {
		t.Fatalf("uncommitted write: %+v %v %v", b, rf.coord.resolved, rf.coord.admissions)
	}
	if report != (ReconcileReport{Completed: 1, Aborted: 1}) {
		t.Fatalf("report = %+v", report)
	}
}

func TestReconcileWriteWithoutAGuard(t *testing.T) {
	rf := setupReconcile()
	rf.pending("op-c", out.IntentGuardedWrite, false)
	rf.coord.acquireErr = out.ErrOwnerConflict
	rf.run(t)
	if w := rf.coord.intents["op-c"]; w.State != out.IntentAborted || !w.Fenced || len(rf.coord.resolved) != 0 {
		t.Fatalf("a write that never got its guard never committed: %+v %v", w, rf.coord.resolved)
	}
}

func TestReconcileLeavesUnknownOutcomesPending(t *testing.T) {
	rf := setupReconcile()
	rf.pending("op-d", out.IntentGuardedWrite, false)
	rf.coord.acquireErr = out.ErrUnavailable
	if report := rf.run(t); report.Skipped != 1 || rf.coord.intents["op-d"].State != out.IntentPending || rf.coord.intents["op-d"].Fenced {
		t.Fatalf("gate unavailable: %+v %+v", report, rf.coord.intents["op-d"])
	}

	rf = setupReconcile()
	rf.pending("op-e", out.IntentGuardedWrite, false)
	young := rf.coord.intents["op-e"]
	young.CreatedAt = t0.Add(time.Hour - time.Second)
	rf.coord.intents["op-e"] = young
	if report := rf.run(t); report.Skipped != 0 || rf.coord.intents["op-e"].Fenced {
		t.Fatalf("a request still in flight is not touched: %+v %+v", report, rf.coord.intents["op-e"])
	}
}

func TestReconcileDeclarations(t *testing.T) {
	rf := setupReconcile()
	rf.pending("op-f", out.IntentDeclaration, true)
	rf.coord.frozenBy = "op-f"
	rf.run(t)
	if rf.coord.intents["op-f"].State != out.IntentCompleted || rf.coord.frozenBy != "op-f" || len(rf.billing.released) != 0 {
		t.Fatalf("a committed declaration keeps both freezes: %+v %q %v", rf.coord.intents["op-f"], rf.coord.frozenBy, rf.billing.released)
	}

	rf = setupReconcile()
	rf.pending("op-g", out.IntentDeclaration, false)
	rf.coord.frozenBy = "op-g"
	rf.run(t)
	if w := rf.coord.intents["op-g"]; w.State != out.IntentAborted || !w.Fenced || rf.coord.frozenBy != "" || len(rf.billing.released) != 1 {
		t.Fatalf("an uncommitted declaration releases both freezes: %+v %q %v", w, rf.coord.frozenBy, rf.billing.released)
	}

	rf = setupReconcile()
	rf.pending("op-h", out.IntentDeclaration, false)
	rf.coord.frozenBy, rf.billing.releaseErr = "op-h", out.ErrUnavailable
	if report := rf.run(t); report.Skipped != 1 || rf.coord.intents["op-h"].State != out.IntentPending || rf.coord.frozenBy != "op-h" {
		t.Fatalf("an unconfirmed Billing release keeps the freeze: %+v %q", report, rf.coord.frozenBy)
	}
}
