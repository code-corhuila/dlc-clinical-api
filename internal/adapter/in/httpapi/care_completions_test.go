package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
)

const chargeA = "dddddddd-dddd-4ddd-8ddd-dddddddddddd"

type stubBilling struct{ err error }

func (s stubBilling) EncounterManualCharges(context.Context, string, string, string) ([]string, error) {
	return []string{chargeA}, s.err
}

type stubGate struct{ err error }

func (s stubGate) Quiesce(context.Context, string, string, string) error { return s.err }

type careEnv struct {
	*entriesEnv
	consultation, procedure string
}

func newCareEnv(t *testing.T, gate out.EncounterGate) *careEnv {
	store := persistence.NewMemory()
	care, patients, clk, ids, apts := stubCare{}, stubPatients{}, &clock{t: testNow}, uuids{}, stubAppointments{}
	routes := append(RecordRoutes(&usecase.RecordService{Repo: store, Care: care, Patients: patients, Clock: clk, IDs: ids}),
		EntryRoutes(&usecase.EntryService{Records: store, Entries: store, Care: care, Patients: patients, Clock: clk, IDs: ids})...)
	routes = append(routes, TreatmentRoutes(&usecase.TreatmentService{Records: store, Entries: store, Treatments: store, Care: care, Patients: patients, Clock: clk, IDs: ids})...)
	routes = append(routes, ProcedureRoutes(&usecase.ProcedureService{Procedures: store, Treatments: store, Care: care, Patients: patients, Appointments: apts, Clock: clk, IDs: ids})...)
	routes = append(routes, CareCompletionRoutes(&usecase.CareCompletionService{Records: store, Entries: store, Closures: store, Care: care, Patients: patients,
		Appointments: apts, Billing: stubBilling{}, Gate: gate, Clock: clk, IDs: ids})...)
	h := NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Routes: routes})
	rec := post(h, validToken(t), key1, `{"patientId":"`+patientA+`"}`)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	e := &careEnv{entriesEnv: &entriesEnv{h: h, store: store, record: body["id"].(string)}}
	_, cons := e.send(t, "/api/v1/clinical-records/"+e.record+"/entries", e.key(), `{"kind":"CONSULTATION","text":"Dolor","appointmentId":"`+appointmentA+`"}`)
	e.consultation = cons["id"].(string)
	_, tr := e.send(t, "/api/v1/treatments", e.key(), `{"patientId":"`+patientA+`","clinicalReason":"Control","procedures":[{"procedureCode":"OPER-01","appointmentId":"`+appointmentA+`"}]}`)
	_, _ = e.send(t, "/api/v1/treatments/"+tr["id"].(string)+"/starts", e.key(), `{"expectedVersion":1}`)
	e.procedure = tr["procedures"].([]any)[0].(map[string]any)["id"].(string)
	_, _ = e.send(t, "/api/v1/procedures/"+e.procedure+"/completions", e.key(), `{"expectedVersion":1}`)
	return e
}

func (e *careEnv) declaration(version string) string {
	return `{"consultationId":"` + e.consultation + `","appointmentId":"` + appointmentA + `","expectedVersion":` + version + `}`
}

func TestDeclareCareCompletionAndReadClosure(t *testing.T) {
	e := newCareEnv(t, stubGate{})
	path := "/api/v1/clinical-records/" + e.record + "/care-completions"
	k := e.key()
	rec, decl := e.send(t, path, k, e.declaration("1"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("declaration = %d %s", rec.Code, rec.Body.String())
	}
	procs, charges := decl["procedureIds"].([]any), decl["manualChargeIds"].([]any)
	if decl["careClosureId"] != decl["id"] || decl["clinicalRecordId"] != e.record || decl["dentistId"] != testSubject || decl["patientId"] != patientA ||
		decl["clinicalVersion"] != float64(2) || len(procs) != 1 || procs[0] != e.procedure || len(charges) != 1 || charges[0] != chargeA || len(decl) != 11 {
		t.Fatalf("declaration = %v", decl)
	}
	closurePath := "/api/v1/care-closures/" + decl["careClosureId"].(string)
	if rec.Header().Get("Location") != closurePath {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	if again, same := e.send(t, path, k, e.declaration("1")); again.Code != 200 || same["id"] != decl["id"] {
		t.Fatalf("retry = %d", again.Code)
	}
	if rec, _ := e.send(t, path, e.key(), e.declaration("2")); rec.Code != 409 || decodeEnvelope(t, rec).Error != "CARE_STATE_CONFLICT" {
		t.Fatalf("second declaration = %d %s", rec.Code, rec.Body.String())
	}

	got := do(e.h, http.MethodGet, closurePath, validToken(t), nil, nil)
	var cl map[string]any
	_ = json.Unmarshal(got.Body.Bytes(), &cl)
	if got.Code != 200 || cl["status"] != "AWAITING_ADMINISTRATIVE_FINALIZATION" || cl["appointmentOutcome"] != "PENDING" || cl["billingOutcome"] != "PENDING" ||
		cl["consultationId"] != e.consultation || cl["appointmentId"] != appointmentA || cl["version"] != float64(1) {
		t.Fatalf("closure = %d %v", got.Code, cl)
	}
	if _, leaked := cl["dentistId"]; leaked {
		t.Fatal("the closure exposes only the CareClosure schema")
	}
	for p, want := range map[string]int{"/api/v1/care-closures/x": 400, "/api/v1/care-closures/" + patientB: 404} {
		if rec := do(e.h, http.MethodGet, p, validToken(t), nil, nil); rec.Code != want {
			t.Errorf("%s = %d, want %d", p, rec.Code, want)
		}
	}
}

func TestDeclarationValidationAndRoles(t *testing.T) {
	e := newCareEnv(t, stubGate{})
	path := "/api/v1/clinical-records/" + e.record + "/care-completions"
	for name, c := range map[string]struct{ body, field string }{
		"consultation not uuid": {`{"consultationId":"c","appointmentId":"` + appointmentA + `","expectedVersion":1}`, "consultationId"},
		"no appointment":        {`{"consultationId":"` + e.consultation + `","expectedVersion":1}`, "appointmentId"},
		"no version":            {`{"consultationId":"` + e.consultation + `","appointmentId":"` + appointmentA + `"}`, "expectedVersion"},
		"client source set":     {`{"consultationId":"` + e.consultation + `","appointmentId":"` + appointmentA + `","expectedVersion":1,"procedureIds":[]}`, "procedureIds"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := e.send(t, path, e.key(), c.body)
			env := decodeEnvelope(t, rec)
			if rec.Code != 400 || len(env.Details) == 0 || env.Details[0].Field != c.field {
				t.Fatalf("got %d %+v", rec.Code, env)
			}
		})
	}
	admin := do(e.h, http.MethodPost, path, tokenFor(t, "44444444-4444-4444-8444-444444444444", "ADMINISTRATOR"), []byte(e.declaration("1")), map[string]string{"Idempotency-Key": e.key()})
	if admin.Code != 403 {
		t.Fatalf("only the Dentist declares, administrator got %d", admin.Code)
	}
}

func TestDeclarationFailsClosedUntilTheWriteGateExists(t *testing.T) {
	e := newCareEnv(t, stubGate{err: out.ErrUnavailable})
	rec, _ := e.send(t, "/api/v1/clinical-records/"+e.record+"/care-completions", e.key(), e.declaration("1"))
	if rec.Code != 503 || decodeEnvelope(t, rec).Error != "SERVICE_UNAVAILABLE" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestRetryClosureRoute(t *testing.T) {
	e := newCareEnv(t, stubGate{})
	_, decl := e.send(t, "/api/v1/clinical-records/"+e.record+"/care-completions", e.key(), e.declaration("1"))
	path := "/api/v1/care-closures/" + decl["careClosureId"].(string) + "/retries"
	if rec, _ := e.send(t, path, e.key(), `{"reason":"Precio corregido","expectedVersion":1}`); rec.Code != 409 || decodeEnvelope(t, rec).Error != "CARE_STATE_CONFLICT" {
		t.Fatalf("an awaiting closure is never retried: %d %s", rec.Code, rec.Body.String())
	}
	for name, c := range map[string]struct{ path, body, field string }{
		"no reason":  {path, `{"expectedVersion":1}`, "reason"},
		"no version": {path, `{"reason":"r"}`, "expectedVersion"},
		"bad id":     {"/api/v1/care-closures/x/retries", `{"reason":"r","expectedVersion":1}`, "id"},
	} {
		t.Run(name, func(t *testing.T) {
			rec, _ := e.send(t, c.path, e.key(), c.body)
			env := decodeEnvelope(t, rec)
			if rec.Code != 400 || env.Details[0].Field != c.field {
				t.Fatalf("got %d %+v", rec.Code, env)
			}
		})
	}
	if rec, _ := e.send(t, "/api/v1/care-closures/"+patientB+"/retries", e.key(), `{"reason":"r","expectedVersion":1}`); rec.Code != 404 {
		t.Fatalf("missing closure = %d", rec.Code)
	}
}
