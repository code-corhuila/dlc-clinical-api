package usecase

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/in"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/domain/model"
)

var t0 = time.Date(2026, 10, 19, 9, 0, 0, 0, time.UTC)

type fixedClock struct{ t time.Time }

func (c *fixedClock) Now() time.Time { c.t = c.t.Add(time.Second); return c.t }

type seqIDs struct{ n int }

func (s *seqIDs) NewID() string { s.n++; return fmt.Sprintf("id-%d", s.n) }

type fakeRepo struct {
	records map[string]model.ClinicalRecord
	idem    map[string]out.IdempotencyRecord
	fail    error
}

func newRepo() *fakeRepo {
	return &fakeRepo{records: map[string]model.ClinicalRecord{}, idem: map[string]out.IdempotencyRecord{}}
}

func scope(a, o, k string) string { return a + "|" + o + "|" + k }

func (r *fakeRepo) FindIdempotency(_ context.Context, a, o, k string) (out.IdempotencyRecord, bool, error) {
	rec, ok := r.idem[scope(a, o, k)]
	return rec, ok, r.fail
}

func (r *fakeRepo) CreateRecord(_ context.Context, rec model.ClinicalRecord, idem out.IdempotencyRecord) error {
	if r.fail != nil {
		return r.fail
	}
	if _, ok := r.idem[scope(idem.ActorID, idem.Operation, idem.Key)]; ok {
		return out.ErrDuplicateIdempotency
	}
	for _, existing := range r.records {
		if existing.PatientID == rec.PatientID {
			return out.ErrRecordExists
		}
	}
	r.records[rec.ID] = rec
	r.idem[scope(idem.ActorID, idem.Operation, idem.Key)] = idem
	return nil
}

func (r *fakeRepo) GetRecord(_ context.Context, id string) (model.ClinicalRecord, error) {
	rec, ok := r.records[id]
	if !ok {
		return model.ClinicalRecord{}, out.ErrNotFound
	}
	return rec, nil
}

func (r *fakeRepo) ListRecords(_ context.Context, f out.RecordFilter) ([]model.ClinicalRecord, int, error) {
	var all []model.ClinicalRecord
	for _, rec := range r.records {
		if f.PatientIDs == nil || contains(f.PatientIDs, rec.PatientID) {
			all = append(all, rec)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].CreatedAt.Before(all[j].CreatedAt) })
	total := len(all)
	if f.Offset >= total {
		return nil, total, nil
	}
	end := min(f.Offset+f.Limit, total)
	return all[f.Offset:end], total, nil
}

type fakeCare struct {
	assigned map[string][]string // dentist -> patients
	err      error
}

func (c fakeCare) IsAssigned(_ context.Context, p, d string) (bool, error) {
	return contains(c.assigned[d], p), c.err
}

func (c fakeCare) AssignedPatients(_ context.Context, d string) ([]string, error) {
	return c.assigned[d], c.err
}

type fakePatients struct {
	inactive map[string]bool
	err      error
}

func (p fakePatients) IsActive(_ context.Context, id string) (bool, error) {
	return !p.inactive[id], p.err
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

var (
	dentist   = in.Actor{ID: "dentist-1", Roles: []string{RoleDentist}}
	other     = in.Actor{ID: "dentist-2", Roles: []string{RoleDentist}}
	assistant = in.Actor{ID: "assistant-1", Roles: []string{RoleAssistant}}
	admin     = in.Actor{ID: "admin-1", Roles: []string{RoleAdministrator}, Permissions: []string{PermClinicalRead, PermClinicalWrite}}
	plainAdm  = in.Actor{ID: "admin-2", Roles: []string{RoleAdministrator}}
)

type fixture struct {
	repo     *fakeRepo
	care     *fakeCare
	patients *fakePatients
	svc      *RecordService
}

func setup() fixture {
	f := fixture{
		repo:     newRepo(),
		care:     &fakeCare{assigned: map[string][]string{"dentist-1": {"pat-1", "pat-2"}}},
		patients: &fakePatients{inactive: map[string]bool{}},
	}
	f.svc = &RecordService{Repo: f.repo, Care: f.care, Patients: f.patients, Clock: &fixedClock{t: t0}, IDs: &seqIDs{}}
	return f
}

func open(f fixture, actor in.Actor, patient, key, hash string) (in.OpenRecordResult, error) {
	return f.svc.Open(context.Background(), in.OpenRecordCommand{Actor: actor, PatientID: patient, IdempotencyKey: key, RequestHash: hash})
}

func kindOf(t *testing.T, err error) model.Kind {
	t.Helper()
	var de *model.Error
	if !errors.As(err, &de) {
		t.Fatalf("expected a domain error, got %v", err)
	}
	return de.Kind
}

func TestOpenRecordByAssignedDentist(t *testing.T) {
	f := setup()
	res, err := open(f, dentist, "pat-1", "key-00001", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if res.Replayed || res.Record.PatientID != "pat-1" || res.Record.CreatedBy != "dentist-1" || res.Record.Version != 1 {
		t.Fatalf("result = %+v", res)
	}
	idem := f.repo.idem[scope("dentist-1", OpOpenRecord, "key-00001")]
	if idem.ResourceID != res.Record.ID || idem.ResponseStatus != 201 || idem.ExpiresAt.Sub(idem.CreatedAt) < 7*24*time.Hour {
		t.Fatalf("idempotency record = %+v", idem)
	}
}

func TestOpenRecordRetryReturnsSameRecord(t *testing.T) {
	f := setup()
	first, _ := open(f, dentist, "pat-1", "key-00001", "h1")
	again, err := open(f, dentist, "pat-1", "key-00001", "h1")
	if err != nil {
		t.Fatal(err)
	}
	if !again.Replayed || again.Record.ID != first.Record.ID || len(f.repo.records) != 1 {
		t.Fatalf("retry must replay the original: %+v", again)
	}
}

func TestOpenRecordSameKeyOtherPayloadConflicts(t *testing.T) {
	f := setup()
	_, _ = open(f, dentist, "pat-1", "key-00001", "h1")
	_, err := open(f, dentist, "pat-2", "key-00001", "h2")
	if kindOf(t, err) != model.KindConflict || err.(*model.Error).Code != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("got %v", err)
	}
}

func TestOpenRecordRules(t *testing.T) {
	cases := map[string]struct {
		actor   in.Actor
		patient string
		prep    func(fixture)
		want    model.Kind
	}{
		"assistant denied":            {assistant, "pat-1", nil, model.KindForbidden},
		"admin without clinical perm": {plainAdm, "pat-1", nil, model.KindForbidden},
		"dentist not assigned":        {other, "pat-1", nil, model.KindForbidden},
		"inactive patient":            {dentist, "pat-1", func(f fixture) { f.patients.inactive["pat-1"] = true }, model.KindRule},
		"second record for patient": {dentist, "pat-1", func(f fixture) {
			_, _ = open(f, dentist, "pat-1", "key-first", "h0")
		}, model.KindConflict},
		"missing patient": {dentist, "", nil, model.KindInvalidInput},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := setup()
			if c.prep != nil {
				c.prep(f)
			}
			_, err := open(f, c.actor, c.patient, "key-00009", "h9")
			if kindOf(t, err) != c.want {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestAuthorizedAdministratorMayOpen(t *testing.T) {
	f := setup()
	if _, err := open(f, admin, "pat-9", "key-00001", "h1"); err != nil {
		t.Fatalf("clinically authorized administrator must open: %v", err)
	}
}

func TestOpenFailsClosedWhenAuthoritiesAreUnavailable(t *testing.T) {
	f := setup()
	f.care.err = out.ErrUnavailable
	if _, err := open(f, dentist, "pat-1", "key-00001", "h1"); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("assignment unavailable must fail closed, got %v", err)
	}
	f = setup()
	f.patients.err = out.ErrUnavailable
	if _, err := open(f, dentist, "pat-1", "key-00001", "h1"); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("patient status unavailable must fail closed, got %v", err)
	}
}

func TestReplayRechecksAuthorization(t *testing.T) {
	f := setup()
	_, _ = open(f, dentist, "pat-1", "key-00001", "h1")
	f.care.assigned["dentist-1"] = nil // assignment ended after the first request
	if _, err := open(f, dentist, "pat-1", "key-00001", "h1"); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("a saved result must not bypass current authorization, got %v", err)
	}
}

func seed(f fixture) {
	for i, p := range []string{"pat-1", "pat-2", "pat-3"} {
		_, _ = open(f, admin, p, fmt.Sprintf("seed-key-%d", i), "h")
	}
}

func TestListScopesDentistToAssignedPatientsBeforeCounting(t *testing.T) {
	f := setup()
	seed(f)
	page, err := f.svc.List(context.Background(), in.ListRecordsQuery{Actor: dentist})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 2 || len(page.Items) != 2 || page.Limit != 20 || page.Page != 1 {
		t.Fatalf("page = %+v", page)
	}
	if !page.Items[0].CreatedAt.Before(page.Items[1].CreatedAt) {
		t.Fatal("oldest first (createdAt ascending)")
	}
	foreign, _ := f.svc.List(context.Background(), in.ListRecordsQuery{Actor: dentist, PatientID: "pat-3"})
	if foreign.Total != 0 || len(foreign.Items) != 0 {
		t.Fatalf("an unassigned patient filter must yield an empty page, got %+v", foreign)
	}
}

func TestListPagination(t *testing.T) {
	f := setup()
	seed(f)
	page, _ := f.svc.List(context.Background(), in.ListRecordsQuery{Actor: admin, Page: in.PageRequest{Page: 2, Limit: 2}})
	if page.Total != 3 || len(page.Items) != 1 || page.Page != 2 || page.Limit != 2 {
		t.Fatalf("page = %+v", page)
	}
	beyond, err := f.svc.List(context.Background(), in.ListRecordsQuery{Actor: admin, Page: in.PageRequest{Page: 9}})
	if err != nil || len(beyond.Items) != 0 || beyond.Total != 3 {
		t.Fatalf("beyond the last page is an empty 200: %+v %v", beyond, err)
	}
	for _, bad := range []in.PageRequest{{Page: -1}, {Limit: 101}, {Limit: -5}} {
		if _, err := f.svc.List(context.Background(), in.ListRecordsQuery{Actor: admin, Page: bad}); kindOf(t, err) != model.KindInvalidInput {
			t.Fatalf("%+v must be rejected", bad)
		}
	}
}

func TestListDeniesAssistant(t *testing.T) {
	f := setup()
	if _, err := f.svc.List(context.Background(), in.ListRecordsQuery{Actor: assistant}); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("got %v", err)
	}
}

func TestGetRecordHidesOutOfScopeAsNotFound(t *testing.T) {
	f := setup()
	seed(f)
	var pat3 string
	for id, r := range f.repo.records {
		if r.PatientID == "pat-3" {
			pat3 = id
		}
	}
	if _, err := f.svc.Get(context.Background(), admin, pat3); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Get(context.Background(), dentist, pat3); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("unassigned record must look nonexistent, got %v", err)
	}
	if _, err := f.svc.Get(context.Background(), dentist, "missing"); kindOf(t, err) != model.KindNotFound {
		t.Fatalf("got %v", err)
	}
	if _, err := f.svc.Get(context.Background(), assistant, pat3); kindOf(t, err) != model.KindForbidden {
		t.Fatalf("assistant is denied by role, got %v", err)
	}
}
