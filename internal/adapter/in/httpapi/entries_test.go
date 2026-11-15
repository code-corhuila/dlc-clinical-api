package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
)

type entriesEnv struct {
	h      http.Handler
	store  *persistence.Memory
	record string
	n      int
}

func newEntriesEnv(t *testing.T) *entriesEnv {
	store := persistence.NewMemory()
	deps := func() (stubCare, stubPatients, *clock, uuids) {
		return stubCare{}, stubPatients{}, &clock{t: testNow}, uuids{}
	}
	care, patients, clk, ids := deps()
	records := &usecase.RecordService{Repo: store, Care: care, Patients: patients, Guard: testGuard(store, care, patients), Clock: clk, IDs: ids}
	entries := &usecase.EntryService{Records: store, Entries: store, Closures: store, Care: care, Patients: patients, Guard: testGuard(store, care, patients), Clock: clk, IDs: ids}
	h := NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)),
		Routes: append(RecordRoutes(records), EntryRoutes(entries)...)})
	rec := post(h, validToken(t), key1, `{"patientId":"`+patientA+`"}`)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return &entriesEnv{h: h, store: store, record: body["id"].(string)}
}

func (e *entriesEnv) key() string {
	e.n++
	return "9f0c1a2e-1111-4000-8000-" + strconv.FormatInt(int64(100000000000+e.n), 10)
}

func (e *entriesEnv) send(t *testing.T, path, key, body string) (*httptest.ResponseRecorder, map[string]any) {
	headers := map[string]string{"X-Correlation-Id": "c0c0c0c0-0000-4000-8000-0000000000e1"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	rec := do(e.h, http.MethodPost, path, validToken(t), []byte(body), headers)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec, out
}

func TestRecordConsultationAndDiagnosis(t *testing.T) {
	e := newEntriesEnv(t)
	path := "/api/v1/clinical-records/" + e.record + "/entries"
	rec, cons := e.send(t, path, e.key(), `{"kind":"CONSULTATION","text":"Dolor en 36","appointmentId":"`+patientB+`"}`)
	if rec.Code != http.StatusCreated || cons["recordId"] != e.record || cons["authorId"] != testSubject || cons["version"] != float64(1) || !rfc3339UTC.MatchString(cons["createdAt"].(string)) {
		t.Fatalf("consultation = %d %v", rec.Code, cons)
	}
	if rec.Header().Get("Location") != "/api/v1/clinical-entries/"+cons["id"].(string) {
		t.Fatalf("Location = %q", rec.Header().Get("Location"))
	}
	if _, ok := cons["consultationId"]; ok {
		t.Fatal("absent optional fields are omitted")
	}
	rec, diag := e.send(t, path, e.key(), `{"kind":"DIAGNOSIS","text":"Caries","code":"K02.1","appointmentId":"`+patientB+`","consultationId":"`+cons["id"].(string)+`"}`)
	if rec.Code != http.StatusCreated || diag["consultationId"] != cons["id"] || diag["code"] != "K02.1" {
		t.Fatalf("diagnosis = %d %v", rec.Code, diag)
	}
	events := e.store.Outbox()
	if len(events) != 2 || events[0].CorrelationID != "c0c0c0c0-0000-4000-8000-0000000000e1" || events[1].EventType != "DiagnosisRecorded" {
		t.Fatalf("outbox = %+v", events)
	}
}

func TestRecordEntryValidation(t *testing.T) {
	e := newEntriesEnv(t)
	path := "/api/v1/clinical-records/" + e.record + "/entries"
	cases := map[string]struct{ path, key, body, field string }{
		"no key":            {path, "", `{"kind":"EVOLUTION","text":"x"}`, "Idempotency-Key"},
		"bad kind":          {path, e.key(), `{"kind":"NOTE","text":"x"}`, "kind"},
		"diagnosis no cons": {path, e.key(), `{"kind":"DIAGNOSIS","text":"x"}`, "consultationId"},
		"bad appointment":   {path, e.key(), `{"kind":"EVOLUTION","text":"x","appointmentId":"a"}`, "appointmentId"},
		"unknown field":     {path, e.key(), `{"kind":"EVOLUTION","text":"x","unitPrice":1}`, "unitPrice"},
		"bad record id":     {"/api/v1/clinical-records/nope/entries", e.key(), `{"kind":"EVOLUTION","text":"x"}`, "id"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _ := e.send(t, c.path, c.key, c.body)
			env := decodeEnvelope(t, rec)
			if rec.Code != http.StatusBadRequest || len(env.Details) == 0 || env.Details[0].Field != c.field {
				t.Fatalf("got %d %+v", rec.Code, env)
			}
		})
	}
}

func TestRecordEntryRetryAndListing(t *testing.T) {
	e := newEntriesEnv(t)
	path := "/api/v1/clinical-records/" + e.record + "/entries"
	k := e.key()
	first, a := e.send(t, path, k, `{"kind":"EVOLUTION","text":"uno"}`)
	again, b := e.send(t, path, k, `{"kind":"EVOLUTION","text":"uno"}`)
	if first.Code != 201 || again.Code != 200 || a["id"] != b["id"] {
		t.Fatalf("retry = %d/%d", first.Code, again.Code)
	}
	_, _ = e.send(t, path, e.key(), `{"kind":"ALLERGY","text":"penicilina"}`)

	list := do(e.h, http.MethodGet, path+"?limit=1", validToken(t), nil, nil)
	var page struct {
		Data []map[string]any `json:"data"`
		Meta struct{ Page, Limit, Total, TotalPages int }
	}
	_ = json.Unmarshal(list.Body.Bytes(), &page)
	if list.Code != 200 || page.Meta.Total != 2 || page.Meta.TotalPages != 2 || len(page.Data) != 1 || page.Data[0]["kind"] != "EVOLUTION" {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	for p, want := range map[string]int{path + "?limit=101": 400, path + "?patientId=" + patientA: 400, "/api/v1/clinical-records/" + patientB + "/entries": 404} {
		if rec := do(e.h, http.MethodGet, p, validToken(t), nil, nil); rec.Code != want {
			t.Errorf("%s = %d, want %d", p, rec.Code, want)
		}
	}
}

func TestAmendEntry(t *testing.T) {
	e := newEntriesEnv(t)
	_, orig := e.send(t, "/api/v1/clinical-records/"+e.record+"/entries", e.key(), `{"kind":"CONSULTATION","text":"Dolor en 36"}`)
	path := "/api/v1/clinical-entries/" + orig["id"].(string) + "/amendments"
	rec, fix := e.send(t, path, e.key(), `{"text":"Dolor en 46","reason":"Pieza equivocada","expectedVersion":1}`)
	if rec.Code != 201 || fix["amendsEntryId"] != orig["id"] || fix["amendmentReason"] != "Pieza equivocada" || fix["kind"] != "CONSULTATION" {
		t.Fatalf("amend = %d %v", rec.Code, fix)
	}
	cases := map[string]struct {
		path, body string
		status     int
		code       string
	}{
		"stale version":   {path, `{"text":"x","reason":"r","expectedVersion":2}`, 409, "STALE_VERSION"},
		"no version":      {path, `{"text":"x","reason":"r"}`, 400, "VALIDATION_ERROR"},
		"decimal version": {path, `{"text":"x","reason":"r","expectedVersion":1.5}`, 400, "VALIDATION_ERROR"},
		"no reason":       {path, `{"text":"x","expectedVersion":1}`, 400, "VALIDATION_ERROR"},
		"bad entry id":    {"/api/v1/clinical-entries/nope/amendments", `{"text":"x","reason":"r","expectedVersion":1}`, 400, "VALIDATION_ERROR"},
		"missing entry":   {"/api/v1/clinical-entries/" + patientB + "/amendments", `{"text":"x","reason":"r","expectedVersion":1}`, 404, "NOT_FOUND"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec, _ := e.send(t, c.path, e.key(), c.body)
			if rec.Code != c.status || decodeEnvelope(t, rec).Error != c.code {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}
