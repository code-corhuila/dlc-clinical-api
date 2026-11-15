package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var storeDown = errors.New("store down")

// A store failure at any step of a guarded write never commits it and never
// releases what it cannot prove: the intent stays for the reconciler.
func TestGuardedWriteStoreFaults(t *testing.T) {
	for name, tc := range map[string]struct {
		op      string
		prep    func(*fakeCoordination)
		pending bool // whether an intent remains PENDING
	}{
		"intent not saved":       {"SaveIntent", nil, false},
		"guard not recorded":     {"AttachGuard", nil, true},
		"fence lost on abort":    {"FenceIntent", func(c *fakeCoordination) { c.acquireErr = out.ErrOwnerConflict }, true},
		"admission abort failed": {"AbortAdmission", func(c *fakeCoordination) { c.acquireErr = out.ErrOwnerConflict }, true},
	} {
		t.Run(name, func(t *testing.T) {
			g, c, _ := guardedFixture()
			c.fail[tc.op] = storeDown
			if tc.prep != nil {
				tc.prep(c)
			}
			committed := false
			err := g.Run(context.Background(), GuardedWrite{Actor: dentist, PatientID: "pat-1", Encounter: anEncounter,
				Commit: func(context.Context) error { committed = true; return nil }})
			if err == nil || committed {
				t.Fatalf("err %v committed %v", err, committed)
			}
			pending := 0
			for _, w := range c.intents {
				if w.State == out.IntentPending {
					pending++
				}
			}
			if (pending == 1) != tc.pending {
				t.Fatalf("intents %+v", c.intents)
			}
		})
	}
}

func TestDeclarationStoreFaults(t *testing.T) {
	cf := setupClosures(t)
	cf.coord.fail["BeginDeclaration"] = storeDown
	if _, err := cf.declare(dentist, "k-df-01"); !errors.Is(err, storeDown) || len(cf.billing.frozen) != 0 {
		t.Fatalf("no freeze, no Billing call: %v %v", err, cf.billing.frozen)
	}

	for name, op := range map[string]string{"fence lost": "FenceIntent", "local release failed": "Unfreeze"} {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			cf.billing.pending = 1
			cf.coord.fail[op] = storeDown
			if _, err := cf.declare(dentist, "k-df-02"); kindOf(t, err) != model.KindConflict {
				t.Fatalf("got %v", err)
			}
			if w, _ := cf.coord.declaration(); w.State != out.IntentPending || cf.coord.frozenBy != w.OperationID {
				t.Fatalf("an unconfirmed rejection keeps the intent and the freeze: %+v %q", w, cf.coord.frozenBy)
			}
		})
	}
}

func TestReconcilerStoreAndOwnerFaults(t *testing.T) {
	rf := setupReconcile()
	rf.coord.fail["PendingIntents"] = storeDown
	if _, err := rf.svc.RunOnce(context.Background()); !errors.Is(err, storeDown) {
		t.Fatalf("got %v", err)
	}

	for name, tc := range map[string]struct {
		kind      string
		committed bool
		prep      func(reconcileFixture)
	}{
		"guard resolution lost":   {out.IntentGuardedWrite, true, func(rf reconcileFixture) { rf.coord.resolveErr = out.ErrUnavailable }},
		"intent not finished":     {out.IntentGuardedWrite, true, func(rf reconcileFixture) { rf.coord.fail["FinishIntent"] = storeDown }},
		"fence lost":              {out.IntentGuardedWrite, false, func(rf reconcileFixture) { rf.coord.fail["FenceIntent"] = storeDown }},
		"admission not aborted":   {out.IntentGuardedWrite, false, func(rf reconcileFixture) { rf.coord.fail["AbortAdmission"] = storeDown }},
		"committed without guard": {out.IntentGuardedWrite, true, func(rf reconcileFixture) { rf.coord.acquireErr = out.ErrOwnerConflict }},
		"declaration fence lost":  {out.IntentDeclaration, false, func(rf reconcileFixture) { rf.coord.fail["FenceIntent"] = storeDown }},
		"local freeze kept":       {out.IntentDeclaration, false, func(rf reconcileFixture) { rf.coord.fail["Unfreeze"] = storeDown }},
	} {
		t.Run(name, func(t *testing.T) {
			rf := setupReconcile()
			rf.pending("op-x", tc.kind, tc.committed)
			rf.coord.admissions["op-x"] = "ADMITTED"
			tc.prep(rf)
			report, err := rf.svc.RunOnce(context.Background())
			if err != nil || report.Skipped != 1 || rf.coord.intents["op-x"].State != out.IntentPending {
				t.Fatalf("report %+v err %v intent %+v", report, err, rf.coord.intents["op-x"])
			}
		})
	}
}
