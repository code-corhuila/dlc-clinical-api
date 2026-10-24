package usecase

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

type fakeClosures struct {
	records   *fakeRepo
	items     map[string]model.CareClosure
	idem      map[string]out.IdempotencyRecord
	outbox    []out.OutboxEvent
	completed []string
	retried   int
	commitErr error // fails the declaration commit, as a concurrent write would
}

func (r *fakeClosures) FindIdempotency(_ context.Context, a, o, k string) (out.IdempotencyRecord, bool, error) {
	rec, ok := r.idem[scope(a, o, k)]
	return rec, ok, nil
}

func (r *fakeClosures) GetClosure(_ context.Context, id string) (model.CareClosure, error) {
	c, ok := r.items[id]
	if !ok {
		return model.CareClosure{}, out.ErrNotFound
	}
	return c, nil
}

func (r *fakeClosures) FindClosureByEncounter(_ context.Context, rec, cons string) (model.CareClosure, bool, error) {
	for _, c := range r.items {
		if c.RecordID == rec && c.ConsultationID == cons {
			return c, true, nil
		}
	}
	return model.CareClosure{}, false, nil
}

func (r *fakeClosures) CompletedProcedureIDs(context.Context, string, string) ([]string, error) {
	return r.completed, nil
}

func (r *fakeClosures) DeclareCareCompletion(_ context.Context, c model.CareClosure, from int, idem out.IdempotencyRecord, ev []out.OutboxEvent) error {
	if r.commitErr != nil {
		return r.commitErr
	}
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	rec := r.records.records[c.RecordID]
	if rec.Version != from {
		return out.ErrVersionConflict
	}
	rec.Version = c.ClinicalVersion
	r.records.records[c.RecordID] = rec
	r.items[c.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = c, idem
	r.outbox = append(r.outbox, ev...)
	return nil
}

// fakeBilling freezes an encounter's charges and records every release.
type fakeBilling struct {
	charges    []string
	pending    int
	err        error
	frozen     []string
	released   []string
	releaseErr error
}

func (b *fakeBilling) FreezeManualCharges(_ context.Context, _, _, operationID string) (out.ManualChargeSettlement, error) {
	b.frozen = append(b.frozen, operationID)
	return out.ManualChargeSettlement{Settled: b.pending == 0, PendingWrites: b.pending, ChargeIDs: b.charges, Version: 1}, b.err
}

func (b *fakeBilling) ReleaseManualChargeFreeze(_ context.Context, _, _, operationID string) error {
	b.released = append(b.released, operationID)
	return b.releaseErr
}

type closureFixture struct {
	entryFixture
	closures     *fakeClosures
	billing      *fakeBilling
	appointments *fakeAppointments
	svc          *CareCompletionService
	consultation string
}

func setupClosures(t *testing.T) closureFixture {
	ef := setupEntries(t)
	cons, err := ef.record(dentist, ef.rec1, model.KindConsultation, "cc-cons-01")
	if err != nil {
		t.Fatal(err)
	}
	cf := closureFixture{entryFixture: ef, consultation: cons.Entry.ID,
		closures:     &fakeClosures{records: ef.repo, items: map[string]model.CareClosure{}, idem: map[string]out.IdempotencyRecord{}, completed: []string{"proc-2", "proc-1"}},
		billing:      &fakeBilling{charges: []string{"charge-1"}},
		appointments: &fakeAppointments{inAttention: true, patientID: "pat-1", dentistID: dentist.ID}}
	cf.svc = &CareCompletionService{Records: ef.repo, Entries: ef.entries, Closures: cf.closures, Care: ef.care, Patients: ef.patients,
		Appointments: cf.appointments, Billing: cf.billing, Intents: ef.coord, Admissions: ef.coord, Clock: ef.svc.Clock, IDs: ef.svc.IDs}
	return cf
}

func (cf closureFixture) declare(actor in.Actor, key string, mut ...func(*in.DeclareCareCompletionCommand)) (in.CareClosureResult, error) {
	cmd := in.DeclareCareCompletionCommand{Actor: actor, RecordID: cf.rec1, ConsultationID: cf.consultation, AppointmentID: "apt-1",
		ExpectedVersion: 1, IdempotencyKey: key, RequestHash: "h-" + key, CorrelationID: "corr-cc"}
	for _, m := range mut {
		m(&cmd)
	}
	return cf.svc.Declare(context.Background(), cmd)
}

func TestDeclareCareCompletionCreatesOneClosurePerEncounter(t *testing.T) {
	cf := setupClosures(t)
	res, err := cf.declare(dentist, "k-decl-01")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Closure
	if c.Status != model.ClosureAwaitingFinalization || c.DentistID != "dentist-1" || c.PatientID != "pat-1" || c.ClinicalVersion != 2 ||
		len(c.ProcedureIDs) != 2 || c.ProcedureIDs[0] != "proc-1" || len(c.ManualChargeIDs) != 1 {
		t.Fatalf("closure = %+v", c)
	}
	if cf.repo.records[cf.rec1].Version != 2 {
		t.Fatal("the declaration raises the clinical record version")
	}
	ev := cf.closures.outbox[0]
	if ev.EventType != "ClinicalCareCompleted" || ev.AggregateType != "ClinicalRecord" || ev.AggregateID != cf.rec1 ||
		ev.Payload["consultationId"] != cf.consultation || ev.Payload["clinicalVersion"] != 2 || len(ev.Payload) != 9 {
		t.Fatalf("event = %+v", ev)
	}
	again, err := cf.declare(dentist, "k-decl-01")
	if err != nil || !again.Replayed || again.Closure.ID != c.ID || len(cf.closures.outbox) != 1 {
		t.Fatalf("retry = %+v %v", again, err)
	}
	_, err = cf.declare(dentist, "k-decl-02", func(c *in.DeclareCareCompletionCommand) { c.ExpectedVersion = 2 })
	if kindOf(t, err) != model.KindConflict || err.(*model.Error).Code != "CARE_STATE_CONFLICT" {
		t.Fatalf("a second declaration of the encounter must conflict, got %v", err)
	}
}

func TestDeclareCareCompletionRules(t *testing.T) {
	cases := map[string]struct {
		actor in.Actor
		prep  func(closureFixture)
		mut   func(*in.DeclareCareCompletionCommand)
		want  model.Kind
		code  string
	}{
		"administrator":                 {admin, nil, nil, model.KindForbidden, "FORBIDDEN"},
		"assistant":                     {assistant, nil, nil, model.KindForbidden, "FORBIDDEN"},
		"unassigned dentist":            {other, nil, nil, model.KindNotFound, "NOT_FOUND"},
		"stale record":                  {dentist, nil, func(c *in.DeclareCareCompletionCommand) { c.ExpectedVersion = 9 }, model.KindStaleVersion, "STALE_VERSION"},
		"not a consultation":            {dentist, nil, func(c *in.DeclareCareCompletionCommand) { c.ConsultationID = "nope" }, model.KindRule, "BUSINESS_RULE_VIOLATION"},
		"other encounter":               {dentist, nil, func(c *in.DeclareCareCompletionCommand) { c.AppointmentID = "apt-9" }, model.KindRule, "BUSINESS_RULE_VIOLATION"},
		"not in attention":              {dentist, func(cf closureFixture) { cf.appointments.inAttention = false }, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
		"inactive patient":              {dentist, func(cf closureFixture) { cf.patients.inactive["pat-1"] = true }, nil, model.KindRule, "BUSINESS_RULE_VIOLATION"},
		"another dentist's appointment": {dentist, func(cf closureFixture) { cf.appointments.dentistID = "dentist-9" }, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
		"unknown appointment":           {dentist, func(cf closureFixture) { cf.appointments.err = out.ErrNotFound }, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
		"billing writes pending":        {dentist, func(cf closureFixture) { cf.billing.pending = 1 }, nil, model.KindConflict, "CARE_STATE_CONFLICT"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			if c.prep != nil {
				c.prep(cf)
			}
			var muts []func(*in.DeclareCareCompletionCommand)
			if c.mut != nil {
				muts = append(muts, c.mut)
			}
			_, err := cf.declare(c.actor, "k-rule-01", muts...)
			if kindOf(t, err) != c.want || err.(*model.Error).Code != c.code {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestRejectedDeclarationReleasesTheBillingFreeze(t *testing.T) {
	for name, prep := range map[string]func(closureFixture){
		"pending manual charge": func(cf closureFixture) { cf.billing.pending = 2 },
		"billing unavailable":   func(cf closureFixture) { cf.billing.err = out.ErrUnavailable },
		"stale at commit":       func(cf closureFixture) { cf.closures.commitErr = out.ErrVersionConflict },
	} {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			prep(cf)
			if _, err := cf.declare(dentist, "k-rel-01"); err == nil {
				t.Fatal("expected a rejection")
			}
			if len(cf.billing.frozen) != 1 || !slices.Equal(cf.billing.released, cf.billing.frozen) || len(cf.closures.outbox) != 0 {
				t.Fatalf("frozen %v released %v", cf.billing.frozen, cf.billing.released)
			}
		})
	}
	cf := setupClosures(t)
	if _, err := cf.declare(dentist, "k-rel-02"); err != nil || len(cf.billing.released) != 0 {
		t.Fatalf("a committed declaration keeps its freeze: %v %v", err, cf.billing.released)
	}
}

func TestDeclarationFailsClosedWithoutOwners(t *testing.T) {
	for name, prep := range map[string]func(closureFixture){
		"billing unavailable": func(cf closureFixture) { cf.billing.err = out.ErrUnavailable },
		"appointments down":   func(cf closureFixture) { cf.appointments.err = out.ErrUnavailable },
	} {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			prep(cf)
			if _, err := cf.declare(dentist, "k-down-01"); !errors.Is(err, out.ErrUnavailable) {
				t.Fatalf("got %v", err)
			}
			if len(cf.closures.items) != 0 || len(cf.closures.outbox) != 0 {
				t.Fatal("no declaration may be committed when an owner cannot confirm")
			}
		})
	}
}

func TestGetClosureIsScoped(t *testing.T) {
	cf := setupClosures(t)
	res, _ := cf.declare(dentist, "k-decl-01")
	if got, err := cf.svc.GetClosure(context.Background(), dentist, res.Closure.ID); err != nil || got.ID != res.Closure.ID {
		t.Fatalf("got %+v %v", got, err)
	}
	if _, err := cf.svc.GetClosure(context.Background(), other, res.Closure.ID); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := cf.svc.GetClosure(context.Background(), dentist, "missing"); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := cf.svc.GetClosure(context.Background(), assistant, res.Closure.ID); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("got %v", err)
	}
}

func (r *fakeClosures) RetryClosure(_ context.Context, c model.CareClosure, from int, idem out.IdempotencyRecord) error {
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	if r.items[c.ID].Version != from {
		return out.ErrVersionConflict
	}
	r.items[c.ID], r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = c, idem
	r.retried++
	return nil
}

func (cf closureFixture) failClosure(t *testing.T) model.CareClosure {
	t.Helper()
	res, err := cf.declare(dentist, "k-decl-01")
	if err != nil {
		t.Fatal(err)
	}
	c := res.Closure
	c.MarkFailed("billing intervention required", c.UpdatedAt)
	cf.closures.items[c.ID] = c
	return c
}

func (cf closureFixture) retry(actor in.Actor, id string, version int, key string) (in.CareClosureResult, error) {
	return cf.svc.Retry(context.Background(), in.RetryClosureCommand{Actor: actor, ClosureID: id, Reason: "precio corregido en Billing", ExpectedVersion: version, IdempotencyKey: key, RequestHash: "h-" + key})
}

func TestRetryClosureResumesTheSameProcess(t *testing.T) {
	cf := setupClosures(t)
	failed := cf.failClosure(t)
	res, err := cf.retry(dentist, failed.ID, 2, "k-retry-01")
	if err != nil || res.Closure.Status != model.ClosureAwaitingFinalization || res.Closure.FailureReason != "" || res.Closure.Version != 3 || cf.closures.retried != 1 {
		t.Fatalf("retry = %+v %v", res, err)
	}
	if again, err := cf.retry(dentist, failed.ID, 2, "k-retry-01"); err != nil || !again.Replayed || cf.closures.retried != 1 {
		t.Fatalf("a repeated retry returns the saved result: %+v %v", again, err)
	}
	if len(cf.closures.outbox) != 1 {
		t.Fatal("a retry never creates a new declaration event")
	}
	if _, err := cf.retry(dentist, failed.ID, 3, "k-retry-02"); kindOf(t, err) != model.KindConflict {
		t.Fatalf("an awaiting closure cannot be retried, got %v", err)
	}
}

func TestRetryClosureRules(t *testing.T) {
	cases := map[string]struct {
		actor   in.Actor
		version int
		reason  string
		want    model.Kind
	}{
		"assistant":          {assistant, 2, "r", model.KindForbidden},
		"unassigned dentist": {other, 2, "r", model.KindNotFound},
		"stale":              {dentist, 1, "r", model.KindStaleVersion},
		"no reason":          {dentist, 2, "", model.KindInvalidInput},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			failed := cf.failClosure(t)
			_, err := cf.svc.Retry(context.Background(), in.RetryClosureCommand{Actor: c.actor, ClosureID: failed.ID, Reason: c.reason, ExpectedVersion: c.version, IdempotencyKey: "k-r-01", RequestHash: "h"})
			if kindOf(t, err) != c.want {
				t.Fatalf("got %v", err)
			}
		})
	}
	cf := setupClosures(t)
	if _, err := cf.retry(dentist, "missing", 1, "k-r-02"); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
}

func TestDeclarationHoldsTheEncounterFreeze(t *testing.T) {
	cf := setupClosures(t)
	if _, err := cf.declare(dentist, "k-hold-01"); err != nil {
		t.Fatal(err)
	}
	w, ok := cf.coord.declaration()
	if !ok || w.State != out.IntentCompleted || w.Encounter != (out.EncounterKey{RecordID: cf.rec1, ConsultationID: cf.consultation}) ||
		cf.coord.frozenBy != w.OperationID || !slices.Equal(cf.billing.frozen, []string{w.OperationID}) || len(cf.billing.released) != 0 {
		t.Fatalf("intent %+v frozenBy %q billing %v/%v", w, cf.coord.frozenBy, cf.billing.frozen, cf.billing.released)
	}
	if _, err := cf.record(dentist, cf.rec1, model.KindEvolution, "k-hold-02"); kindOf(t, err) != model.KindConflict {
		t.Fatalf("the declared encounter admits no new write: %v", err)
	}
}

func TestRejectedDeclarationReleasesBothFreezes(t *testing.T) {
	cases := map[string]struct {
		prep    func(closureFixture)
		billing bool // whether Billing was frozen
	}{
		"admitted write pending":  {func(cf closureFixture) { cf.coord.admissions["op-w"] = "ADMITTED" }, false},
		"not in attention":        {func(cf closureFixture) { cf.appointments.inAttention = false }, false},
		"billing writes pending":  {func(cf closureFixture) { cf.billing.pending = 1 }, true},
		"billing unavailable":     {func(cf closureFixture) { cf.billing.err = out.ErrUnavailable }, true},
		"commit lost to a writer": {func(cf closureFixture) { cf.closures.commitErr = out.ErrVersionConflict }, true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cf := setupClosures(t)
			tc.prep(cf)
			if _, err := cf.declare(dentist, "k-rej-01"); err == nil {
				t.Fatal("expected a rejection")
			}
			w, _ := cf.coord.declaration()
			if w.State != out.IntentAborted || cf.coord.frozenBy != "" || (len(cf.billing.frozen) == 1) != tc.billing || !slices.Equal(cf.billing.released, cf.billing.frozen) {
				t.Fatalf("intent %+v frozenBy %q billing %v/%v", w, cf.coord.frozenBy, cf.billing.frozen, cf.billing.released)
			}
		})
	}
}

func TestConcurrentDeclarationIsRejectedWhileTheFreezeIsHeld(t *testing.T) {
	cf := setupClosures(t)
	cf.coord.frozenBy = "op-other"
	if _, err := cf.declare(dentist, "k-conc-01"); kindOf(t, err) != model.KindConflict || len(cf.billing.frozen) != 0 {
		t.Fatalf("got %v, billing %v", err, cf.billing.frozen)
	}
}

func TestUnreleasedBillingFreezeIsLeftToTheReconciler(t *testing.T) {
	cf := setupClosures(t)
	cf.billing.pending, cf.billing.releaseErr = 1, out.ErrUnavailable
	if _, err := cf.declare(dentist, "k-unrel-01"); err == nil {
		t.Fatal("expected a rejection")
	}
	if w, _ := cf.coord.declaration(); w.State != out.IntentPending || cf.coord.frozenBy != w.OperationID {
		t.Fatalf("intent %+v frozenBy %q: both stay until Billing confirms the release", w, cf.coord.frozenBy)
	}
}
