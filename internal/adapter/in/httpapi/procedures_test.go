package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
)

type stubAppointments struct{ err error }

func (s stubAppointments) InAttention(context.Context, string, string) (bool, error) {
	return s.err == nil, s.err
}

type proceduresEnv struct {
	*treatmentsEnv
	procedure string
}

func newProceduresEnv(t *testing.T, apts out.AppointmentAuthority) *proceduresEnv {
	store := persistence.NewMemory()
	care, patients, clk, ids := stubCare{}, stubPatients{}, &clock{t: testNow}, uuids{}
	routes := append(RecordRoutes(&usecase.RecordService{Repo: store, Care: care, Patients: patients, Clock: clk, IDs: ids}),
		EntryRoutes(&usecase.EntryService{Records: store, Entries: store, Care: care, Patients: patients, Clock: clk, IDs: ids})...)
	routes = append(routes, TreatmentRoutes(&usecase.TreatmentService{Records: store, Entries: store, Treatments: store, Care: care, Patients: patients, Clock: clk, IDs: ids})...)
	routes = append(routes, ProcedureRoutes(&usecase.ProcedureService{Procedures: store, Treatments: store, Care: care, Patients: patients, Appointments: apts, Clock: clk, IDs: ids})...)
	h := NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Routes: routes})
	rec := post(h, validToken(t), key1, `{"patientId":"`+patientA+`"}`)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	env := &proceduresEnv{treatmentsEnv: &treatmentsEnv{entriesEnv: &entriesEnv{h: h, store: store, record: body["id"].(string)}}}
	_, tr := env.send(t, "/api/v1/treatments", env.key(), `{"patientId":"`+patientA+`","clinicalReason":"Control","procedures":[{"procedureCode":"OPER-01","appointmentId":"`+appointmentA+`"}]}`)
	_, _ = env.send(t, "/api/v1/treatments/"+tr["id"].(string)+"/starts", env.key(), `{"expectedVersion":1}`)
	env.procedure = tr["procedures"].([]any)[0].(map[string]any)["id"].(string)
	return env
}

const extraJSON = `{"sourceRecordId":"%s","type":"ADDITIONAL_MATERIAL","code":"RES-1","quantity":"1.5","description":"Resina","clinicalReason":"Fractura"}`

func completionBody(materials string) string {
	return `{"expectedVersion":1,"materialsUsed":[` + materials + `],"additionalRequirements":[]}`
}

func TestCompleteProcedureReturnsProgressOnly(t *testing.T) {
	e := newProceduresEnv(t, stubAppointments{})
	path := "/api/v1/procedures/" + e.procedure + "/completions"
	k := e.key()
	body := completionBody(fmt.Sprintf(extraJSON, patientB))
	rec, done := e.send(t, path, k, body)
	if rec.Code != http.StatusOK || done["procedureId"] != e.procedure || done["appointmentId"] != appointmentA || done["version"] != float64(2) || !rfc3339UTC.MatchString(done["completedAt"].(string)) || len(done) != 5 {
		t.Fatalf("completion = %d %v", rec.Code, done)
	}
	if again, same := e.send(t, path, k, body); again.Code != 200 || same["completedAt"] != done["completedAt"] {
		t.Fatalf("retry = %d %v", again.Code, same)
	}
	if rec, _ := e.send(t, path, e.key(), `{"expectedVersion":2}`); rec.Code != 409 || decodeEnvelope(t, rec).Error != "CARE_STATE_CONFLICT" {
		t.Fatalf("second completion = %d %s", rec.Code, rec.Body.String())
	}
	var completed int
	for _, ev := range e.store.Outbox() {
		if ev.EventType == "ProcedureCompleted" {
			completed++
		}
	}
	if completed != 1 {
		t.Fatalf("exactly one ProcedureCompleted, got %d", completed)
	}
}

func TestCompleteProcedureValidation(t *testing.T) {
	e := newProceduresEnv(t, stubAppointments{})
	path := "/api/v1/procedures/" + e.procedure + "/completions"
	cases := map[string]struct{ body, field string }{
		"no version":       {`{"materialsUsed":[]}`, "expectedVersion"},
		"source not uuid":  {completionBody(fmt.Sprintf(extraJSON, "src-1")), "materialsUsed[0].sourceRecordId"},
		"numeric quantity": {`{"expectedVersion":1,"materialsUsed":[{"sourceRecordId":"` + patientB + `","type":"ADDITIONAL_MATERIAL","quantity":1.5,"description":"d","clinicalReason":"r"}]}`, "materialsUsed.quantity"},
		"zero quantity":    {completionBody(`{"sourceRecordId":"` + patientB + `","type":"ADDITIONAL_MATERIAL","quantity":"0","description":"d","clinicalReason":"r"}`), "quantity"},
		"price field":      {completionBody(`{"sourceRecordId":"` + patientB + `","type":"ADDITIONAL_MATERIAL","quantity":"1","description":"d","clinicalReason":"r","unitPrice":"9.00"}`), "unitPrice"},
		"unknown type":     {completionBody(`{"sourceRecordId":"` + patientB + `","type":"MATERIAL","quantity":"1","description":"d","clinicalReason":"r"}`), "type"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _ := e.send(t, path, e.key(), c.body)
			env := decodeEnvelope(t, rec)
			if rec.Code != 400 || len(env.Details) == 0 || env.Details[0].Field != c.field {
				t.Fatalf("got %d %+v", rec.Code, env)
			}
		})
	}
	if rec, _ := e.send(t, "/api/v1/procedures/nope/completions", e.key(), completionBody("")); rec.Code != 400 {
		t.Fatalf("bad id = %d", rec.Code)
	}
	if rec, _ := e.send(t, path, "", completionBody("")); rec.Code != 400 {
		t.Fatalf("missing key = %d", rec.Code)
	}
	if rec, _ := e.send(t, "/api/v1/procedures/"+patientA+"/completions", e.key(), completionBody("")); rec.Code != 404 {
		t.Fatalf("missing procedure = %d", rec.Code)
	}
}

func TestCompleteProcedureFailsClosedWithoutAppointments(t *testing.T) {
	e := newProceduresEnv(t, stubAppointments{err: out.ErrUnavailable})
	rec, _ := e.send(t, "/api/v1/procedures/"+e.procedure+"/completions", e.key(), completionBody(""))
	if rec.Code != 503 || decodeEnvelope(t, rec).Error != "SERVICE_UNAVAILABLE" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
