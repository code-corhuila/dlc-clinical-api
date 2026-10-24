package usecase

import (
	"context"
	"errors"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

// fakeCoordination is the intent store, admission store and patient gate of
// one test, with switches for every owner answer.
type fakeCoordination struct {
	intents    map[string]out.WriteIntent
	admissions map[string]string // operationId -> ADMITTED|COMMITTED|ABORTED
	frozen     bool              // the encounter rejects admissions
	frozenBy   string            // operationId of the declaration holding the freeze
	acquireErr error
	resolveErr error
	resolved   map[string]string // guardId -> outcome
}

func newCoordination() *fakeCoordination {
	return &fakeCoordination{intents: map[string]out.WriteIntent{}, admissions: map[string]string{}, resolved: map[string]string{}}
}

func (f *fakeCoordination) SaveIntent(_ context.Context, w out.WriteIntent) error {
	f.intents[w.OperationID] = w
	return nil
}

func (f *fakeCoordination) AttachGuard(_ context.Context, op, guard string) error {
	w := f.intents[op]
	w.GuardID = guard
	f.intents[op] = w
	return nil
}

func (f *fakeCoordination) FinishIntent(_ context.Context, op, state string) error {
	w := f.intents[op]
	w.State = state
	f.intents[op] = w
	return nil
}

func (f *fakeCoordination) FenceIntent(_ context.Context, op string) (bool, error) {
	w, ok := f.intents[op]
	if !ok {
		return false, out.ErrNotFound
	}
	if !w.Committed {
		w.Fenced = true
		f.intents[op] = w
	}
	return w.Committed, nil
}

func (f *fakeCoordination) GetIntent(_ context.Context, op string) (out.WriteIntent, error) {
	w, ok := f.intents[op]
	if !ok {
		return out.WriteIntent{}, out.ErrNotFound
	}
	return w, nil
}

func (f *fakeCoordination) PendingIntents(context.Context, int) ([]out.WriteIntent, error) {
	return nil, nil
}

func (f *fakeCoordination) Admit(_ context.Context, _ out.EncounterKey, op string) error {
	if f.frozen || f.frozenBy != "" {
		return out.ErrEncounterFrozen
	}
	f.admissions[op] = "ADMITTED"
	return nil
}

func (f *fakeCoordination) AbortAdmission(_ context.Context, _ out.EncounterKey, op string) error {
	if f.admissions[op] == "ADMITTED" {
		f.admissions[op] = "ABORTED"
	}
	return nil
}

func (f *fakeCoordination) BeginDeclaration(_ context.Context, w out.WriteIntent) (int, error) {
	if f.frozenBy != "" && f.frozenBy != w.OperationID {
		return 0, out.ErrEncounterFrozen
	}
	f.intents[w.OperationID], f.frozenBy = w, w.OperationID
	pending := 0
	for _, state := range f.admissions {
		if state == "ADMITTED" {
			pending++
		}
	}
	return pending, nil
}

func (f *fakeCoordination) Unfreeze(_ context.Context, _ out.EncounterKey, op string) error {
	if f.frozenBy == op {
		f.frozenBy = ""
	}
	return nil
}

// declaration returns the DECLARATION intent of a test, if any.
func (f *fakeCoordination) declaration() (out.WriteIntent, bool) {
	for _, w := range f.intents {
		if w.Kind == out.IntentDeclaration {
			return w, true
		}
	}
	return out.WriteIntent{}, false
}

func (f *fakeCoordination) AcquireActivityGuard(_ context.Context, _, op string) (out.Guard, error) {
	if f.acquireErr != nil {
		return out.Guard{}, f.acquireErr
	}
	return out.Guard{ID: "guard-" + op, State: out.GuardOpen, Token: "token-" + op}, nil
}

func (f *fakeCoordination) ResolveGuard(_ context.Context, id, token, outcome string) error {
	if f.resolveErr != nil {
		return f.resolveErr
	}
	if token != "token-"+id[len("guard-"):] {
		return out.ErrOwnerConflict
	}
	f.resolved[id] = outcome
	return nil
}

// commitTagged plays the repository: it settles the tag like the adapters do.
func (f *fakeCoordination) commitTagged(ctx context.Context) error {
	tag, ok := out.WriteTagFrom(ctx)
	if !ok {
		return errors.New("write without tag")
	}
	w := f.intents[tag.OperationID]
	w.Committed = true
	f.intents[tag.OperationID] = w
	if tag.Encounter != nil {
		f.admissions[tag.OperationID] = "COMMITTED"
	}
	return nil
}

// only returns the single intent of a test.
func (f *fakeCoordination) only(t *testing.T) out.WriteIntent {
	t.Helper()
	if len(f.intents) != 1 {
		t.Fatalf("intents = %+v", f.intents)
	}
	for _, w := range f.intents {
		return w
	}
	return out.WriteIntent{}
}

func guardedFixture() (*GuardedWrites, *fakeCoordination, fixture) {
	f := setup()
	c := newCoordination()
	g := &GuardedWrites{Intents: c, Admissions: c, Gate: c, Care: f.care, Patients: f.patients, Clock: &fixedClock{t: t0}, IDs: &seqIDs{}}
	return g, c, f
}

var anEncounter = &out.EncounterKey{RecordID: "rec-1", ConsultationID: "cons-1"}

func TestGuardedWriteCommitsUnderTheGuard(t *testing.T) {
	g, c, _ := guardedFixture()
	if err := g.Run(context.Background(), GuardedWrite{Actor: dentist, PatientID: "pat-1", Encounter: anEncounter, Commit: c.commitTagged}); err != nil {
		t.Fatal(err)
	}
	w := c.only(t)
	if w.State != out.IntentCompleted || !w.Committed || w.Kind != out.IntentGuardedWrite || w.PatientID != "pat-1" || w.Encounter != *anEncounter ||
		c.resolved[w.GuardID] != out.GuardFinalized || c.admissions[w.OperationID] != "COMMITTED" {
		t.Fatalf("intent %+v resolved %v admissions %v", w, c.resolved, c.admissions)
	}
}

func TestGuardedWriteRejections(t *testing.T) {
	cases := map[string]struct {
		prep     func(*fakeCoordination, fixture)
		kind     model.Kind
		state    string // final intent state
		guard    string // guard resolution, "" when none
		admitted string
	}{
		"frozen encounter":      {func(c *fakeCoordination, _ fixture) { c.frozen = true }, model.KindConflict, out.IntentAborted, "", ""},
		"blocked patient gate":  {func(c *fakeCoordination, _ fixture) { c.acquireErr = out.ErrOwnerConflict }, model.KindRule, out.IntentAborted, "", "ABORTED"},
		"deactivated meanwhile": {func(_ *fakeCoordination, f fixture) { f.patients.inactive["pat-1"] = true }, model.KindRule, out.IntentAborted, out.GuardAborted, "ABORTED"},
		"assignment ended":      {func(_ *fakeCoordination, f fixture) { f.care.assigned["dentist-1"] = nil }, model.KindForbidden, out.IntentAborted, out.GuardAborted, "ABORTED"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f := guardedFixture()
			tc.prep(c, f)
			committed := false
			err := g.Run(context.Background(), GuardedWrite{Actor: dentist, PatientID: "pat-1", Encounter: anEncounter,
				Commit: func(context.Context) error { committed = true; return nil }})
			w := c.only(t)
			if kindOf(t, err) != tc.kind || committed || w.State != tc.state || c.resolved[w.GuardID] != tc.guard || c.admissions[w.OperationID] != tc.admitted {
				t.Fatalf("err %v committed %v intent %+v resolved %v admissions %v", err, committed, w, c.resolved, c.admissions)
			}
		})
	}
}

func TestGuardedWriteKeepsUnknownOutcomesPending(t *testing.T) {
	cases := map[string]struct {
		prep   func(*fakeCoordination, fixture)
		commit func(*fakeCoordination) func(context.Context) error
		down   bool
	}{
		"gate unavailable": {func(c *fakeCoordination, _ fixture) { c.acquireErr = out.ErrUnavailable }, nil, true},
		"care unavailable": {func(_ *fakeCoordination, f fixture) { f.care.err = out.ErrUnavailable }, nil, true},
		"resolution lost after commit": {func(c *fakeCoordination, _ fixture) { c.resolveErr = out.ErrUnavailable },
			func(c *fakeCoordination) func(context.Context) error { return c.commitTagged }, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, c, f := guardedFixture()
			tc.prep(c, f)
			commit := func(context.Context) error { t.Fatal("must not commit"); return nil }
			if tc.commit != nil {
				commit = tc.commit(c)
			}
			err := g.Run(context.Background(), GuardedWrite{Actor: dentist, PatientID: "pat-1", Commit: commit})
			if tc.down != errors.Is(err, out.ErrUnavailable) || c.only(t).State != out.IntentPending {
				t.Fatalf("err %v intent %+v", err, c.only(t))
			}
		})
	}
}

func TestGuardedWriteDecidesFromTheCommitMark(t *testing.T) {
	g, c, _ := guardedFixture()
	failed := errors.New("write conflict")
	err := g.Run(context.Background(), GuardedWrite{Actor: dentist, PatientID: "pat-1", Encounter: anEncounter, Commit: func(context.Context) error { return failed }})
	w := c.only(t)
	if !errors.Is(err, failed) || w.State != out.IntentAborted || c.resolved[w.GuardID] != out.GuardAborted || c.admissions[w.OperationID] != "ABORTED" {
		t.Fatalf("uncommitted write: %v %+v %v", err, w, c.resolved)
	}

	g, c, _ = guardedFixture()
	lostAck := func(ctx context.Context) error { _ = c.commitTagged(ctx); return failed }
	err = g.Run(context.Background(), GuardedWrite{Actor: dentist, PatientID: "pat-1", Commit: lostAck})
	if w := c.only(t); !errors.Is(err, failed) || w.State != out.IntentCompleted || c.resolved[w.GuardID] != out.GuardFinalized {
		t.Fatalf("committed write with a lost answer: %v %+v %v", err, w, c.resolved)
	}
}

func TestUnconfiguredGuardFailsClosed(t *testing.T) {
	var g *GuardedWrites
	if err := g.Run(context.Background(), GuardedWrite{Commit: func(context.Context) error { t.Fatal("unguarded write"); return nil }}); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
}

// intentsFor returns the guarded-write intents admitted to an encounter.
func (f *fakeCoordination) intentsFor(consultationID string) []out.WriteIntent {
	var found []out.WriteIntent
	for _, w := range f.intents {
		if w.Encounter.ConsultationID == consultationID {
			found = append(found, w)
		}
	}
	return found
}

// consultationOf returns the fixture's consultation for apt-1 on rec1.
func (ef entryFixture) consultationOf(t *testing.T) string {
	t.Helper()
	for _, e := range ef.entries.entries {
		if e.Kind == model.KindConsultation && e.RecordID == ef.rec1 && e.AppointmentID == "apt-1" {
			return e.ID
		}
	}
	t.Fatal("no consultation for apt-1")
	return ""
}

func TestAppointmentWritesAreAdmittedToTheConsultationEncounter(t *testing.T) {
	pf := setupProcedures(t, true)
	cons := pf.consultationOf(t)
	before := len(pf.coord.intentsFor(cons))
	if _, err := pf.complete(dentist, "k-adm-01"); err != nil {
		t.Fatal(err)
	}
	if _, err := pf.record(dentist, pf.rec1, model.KindEvolution, "k-adm-02"); err != nil {
		t.Fatal(err)
	}
	if got := pf.coord.intentsFor(cons); len(got) != before+2 || got[0].Encounter.RecordID != pf.rec1 {
		t.Fatalf("admitted = %+v", got)
	}
	pf = setupProcedures(t, true)
	pf.coord.frozen = true
	if _, err := pf.complete(dentist, "k-adm-03"); kindOf(t, err) != model.KindConflict {
		t.Fatalf("a frozen encounter rejects the completion: %v", err)
	}
	if _, err := pf.record(dentist, pf.rec1, model.KindEvolution, "k-adm-04"); kindOf(t, err) != model.KindConflict {
		t.Fatalf("and the encounter entry: %v", err)
	}
}

func TestTreatmentPlanningRunsUnderTheGuard(t *testing.T) {
	tf := setupTreatments(t)
	tf.coord.acquireErr = out.ErrOwnerConflict
	if _, err := tf.plan(dentist, "pat-1", "k-gplan-01"); kindOf(t, err) != model.KindRule {
		t.Fatalf("a blocked patient gate rejects the plan: %v", err)
	}
	if len(tf.repo.items) != 0 {
		t.Fatal("no treatment is stored")
	}
}
