package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
)

const appointmentA = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"

type treatmentsEnv struct {
	*entriesEnv
	diagnosis string
}

func newTreatmentsEnv(t *testing.T) *treatmentsEnv {
	store := persistence.NewMemory()
	care, patients, clk, ids := stubCare{}, stubPatients{}, &clock{t: testNow}, uuids{}
	records := &usecase.RecordService{Repo: store, Care: care, Patients: patients, Clock: clk, IDs: ids}
	entries := &usecase.EntryService{Records: store, Entries: store, Care: care, Patients: patients, Clock: clk, IDs: ids}
	treatments := &usecase.TreatmentService{Records: store, Entries: store, Treatments: store, Care: care, Patients: patients, Clock: clk, IDs: ids}
	routes := append(RecordRoutes(records), EntryRoutes(entries)...)
	h := NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Routes: append(routes, TreatmentRoutes(treatments)...)})
	rec := post(h, validToken(t), key1, `{"patientId":"`+patientA+`"}`)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	env := &treatmentsEnv{entriesEnv: &entriesEnv{h: h, store: store, record: body["id"].(string)}}
	path := "/api/v1/clinical-records/" + env.record + "/entries"
	_, cons := env.send(t, path, env.key(), `{"kind":"CONSULTATION","text":"Dolor"}`)
	_, diag := env.send(t, path, env.key(), `{"kind":"DIAGNOSIS","text":"Caries","consultationId":"`+cons["id"].(string)+`"}`)
	env.diagnosis = diag["id"].(string)
	return env
}

func (e *treatmentsEnv) planBody(extra string) string {
	return `{"patientId":"` + patientA + `","diagnosisId":"` + e.diagnosis + `","procedures":[{"procedureCode":"OPER-01","appointmentId":"` + appointmentA + `"}]` + extra + `}`
}

func TestPlanAndStartTreatment(t *testing.T) {
	e := newTreatmentsEnv(t)
	k := e.key()
	rec, tr := e.send(t, "/api/v1/treatments", k, e.planBody(""))
	if rec.Code != http.StatusCreated || tr["status"] != "PLANNED" || tr["patientId"] != patientA || tr["diagnosisId"] != e.diagnosis || tr["version"] != float64(1) {
		t.Fatalf("plan = %d %v", rec.Code, tr)
	}
	procs := tr["procedures"].([]any)
	p := procs[0].(map[string]any)
	if len(procs) != 1 || !isUUID(p["id"].(string)) || p["procedureCode"] != "OPER-01" || p["appointmentId"] != appointmentA || p["status"] != "PLANNED" || len(p) != 4 {
		t.Fatalf("procedures = %v", procs)
	}
	if rec.Header().Get("Location") != "/api/v1/treatments/"+tr["id"].(string) {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	if _, leaked := tr["createdBy"]; leaked {
		t.Fatal("only contract fields")
	}
	again, same := e.send(t, "/api/v1/treatments", k, e.planBody(""))
	if again.Code != http.StatusOK || same["id"] != tr["id"] {
		t.Fatalf("retry = %d", again.Code)
	}

	start := "/api/v1/treatments/" + tr["id"].(string) + "/starts"
	sk := e.key()
	rec, started := e.send(t, start, sk, `{"expectedVersion":1}`)
	if rec.Code != http.StatusOK || started["status"] != "IN_PROGRESS" || started["version"] != float64(2) {
		t.Fatalf("start = %d %v", rec.Code, started)
	}
	if rec, _ := e.send(t, start, sk, `{"expectedVersion":1}`); rec.Code != http.StatusOK {
		t.Fatalf("retried start = %d", rec.Code)
	}
	if rec, _ := e.send(t, start, e.key(), `{"expectedVersion":2}`); rec.Code != 422 || decodeEnvelope(t, rec).Error != "INVALID_STATUS_TRANSITION" {
		t.Fatalf("double start = %d %s", rec.Code, rec.Body.String())
	}
	if rec, _ := e.send(t, start, e.key(), `{"expectedVersion":1}`); rec.Code != 409 || decodeEnvelope(t, rec).Error != "STALE_VERSION" {
		t.Fatalf("stale start = %d", rec.Code)
	}
}

func TestPlanTreatmentValidation(t *testing.T) {
	e := newTreatmentsEnv(t)
	cases := map[string]struct {
		body, field string
	}{
		"patient not uuid":     {`{"patientId":"x","clinicalReason":"r","procedures":[{"procedureCode":"A","appointmentId":"` + appointmentA + `"}]}`, "patientId"},
		"diagnosis not uuid":   {`{"patientId":"` + patientA + `","diagnosisId":"d","procedures":[{"procedureCode":"A","appointmentId":"` + appointmentA + `"}]}`, "diagnosisId"},
		"appointment not uuid": {`{"patientId":"` + patientA + `","clinicalReason":"r","procedures":[{"procedureCode":"A","appointmentId":"a"}]}`, "procedures[0].appointmentId"},
		"money in procedure":   {`{"patientId":"` + patientA + `","clinicalReason":"r","procedures":[{"procedureCode":"A","appointmentId":"` + appointmentA + `","price":"1.00"}]}`, "price"},
		"no procedures":        {`{"patientId":"` + patientA + `","clinicalReason":"r","procedures":[]}`, "procedures"},
		"no reason nor diag":   {`{"patientId":"` + patientA + `","procedures":[{"procedureCode":"A","appointmentId":"` + appointmentA + `"}]}`, "diagnosisId"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _ := e.send(t, "/api/v1/treatments", e.key(), c.body)
			env := decodeEnvelope(t, rec)
			if rec.Code != 400 || len(env.Details) == 0 || env.Details[0].Field != c.field {
				t.Fatalf("got %d %+v", rec.Code, env)
			}
		})
	}
	if rec, _ := e.send(t, "/api/v1/treatments", "", e.planBody("")); rec.Code != 400 {
		t.Fatalf("missing key = %d", rec.Code)
	}
	if rec, _ := e.send(t, "/api/v1/treatments/nope/starts", e.key(), `{"expectedVersion":1}`); rec.Code != 400 {
		t.Fatalf("bad id = %d", rec.Code)
	}
	if rec, _ := e.send(t, "/api/v1/treatments/"+patientB+"/starts", e.key(), `{}`); rec.Code != 400 {
		t.Fatalf("missing version = %d", rec.Code)
	}
}

func TestListAndReadTreatments(t *testing.T) {
	e := newTreatmentsEnv(t)
	_, first := e.send(t, "/api/v1/treatments", e.key(), e.planBody(""))
	_, _ = e.send(t, "/api/v1/treatments", e.key(), `{"patientId":"`+patientA+`","clinicalReason":"Control","procedures":[{"procedureCode":"RX","appointmentId":"`+appointmentA+`"}]}`)
	list := do(e.h, http.MethodGet, "/api/v1/treatments?patientId="+patientA+"&limit=1", validToken(t), nil, nil)
	var page struct {
		Data []map[string]any `json:"data"`
		Meta struct{ Page, Limit, Total, TotalPages int }
	}
	_ = json.Unmarshal(list.Body.Bytes(), &page)
	if list.Code != 200 || page.Meta.Total != 2 || page.Meta.TotalPages != 2 || page.Data[0]["clinicalReason"] != "Control" {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if rec := do(e.h, http.MethodGet, "/api/v1/treatments/"+first["id"].(string), validToken(t), nil, nil); rec.Code != 200 {
		t.Fatalf("read = %d", rec.Code)
	}
	for p, want := range map[string]int{"/api/v1/treatments/" + patientB: 404, "/api/v1/treatments/x": 400, "/api/v1/treatments?status=PLANNED": 400, "/api/v1/treatments?limit=0": 400} {
		if rec := do(e.h, http.MethodGet, p, validToken(t), nil, nil); rec.Code != want {
			t.Errorf("%s = %d, want %d", p, rec.Code, want)
		}
	}
}
