package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/out/persistence"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/usecase"
)

const (
	patientA  = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	patientB  = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	assistant = "22222222-2222-4222-8222-222222222222"
	key1      = "9f0c1a2e-0000-4000-8000-000000000001"
)

type stubCare struct{ err error }

func (s stubCare) IsAssigned(_ context.Context, p, d string) (bool, error) {
	return d == testSubject && (p == patientA || p == patientB), s.err
}
func (s stubCare) AssignedPatients(_ context.Context, d string) ([]string, error) {
	if d != testSubject {
		return nil, s.err
	}
	return []string{patientA, patientB}, s.err
}

type stubPatients struct{ inactive string }

func (s stubPatients) IsActive(_ context.Context, id string) (bool, error) {
	return id != s.inactive, nil
}

type clock struct{ t time.Time }

func (c *clock) Now() time.Time { c.t = c.t.Add(time.Second); return c.t }

type uuids struct{}

func (uuids) NewID() string { return newUUID() }

func recordsRouter(care out.CareAuthority, patients out.PatientDirectory) http.Handler {
	store := persistence.NewMemory()
	svc := &usecase.RecordService{Repo: store, Care: care, Patients: patients, Guard: testGuard(store, care, patients), Clock: &clock{t: testNow}, IDs: uuids{}}
	return NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Routes: RecordRoutes(svc)})
}

func tokenFor(t *testing.T, sub string, roles ...string) string {
	c := validClaims()
	c["sub"], c["roles"], c["permissions"] = sub, roles, []string{}
	return signToken(t, map[string]any{"alg": "RS256"}, c, testKey)
}

func post(h http.Handler, token, key, body string) *httptest.ResponseRecorder {
	headers := map[string]string{"Content-Type": "application/json"}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return do(h, http.MethodPost, "/api/v1/clinical-records", token, []byte(body), headers)
}

var rfc3339UTC = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`)

func TestOpenRecordCreatesThenReplays(t *testing.T) {
	h := recordsRouter(stubCare{}, stubPatients{})
	body := `{"patientId":"` + patientA + `"}`
	first := post(h, validToken(t), key1, body)
	if first.Code != http.StatusCreated {
		t.Fatalf("status = %d body %s", first.Code, first.Body.String())
	}
	var rec map[string]any
	_ = json.Unmarshal(first.Body.Bytes(), &rec)
	id, _ := rec["id"].(string)
	if !isUUID(id) || rec["patientId"] != patientA || rec["version"] != float64(1) || !rfc3339UTC.MatchString(rec["createdAt"].(string)) {
		t.Fatalf("representation = %v", rec)
	}
	if _, leaked := rec["createdBy"]; leaked || len(rec) != 5 {
		t.Fatalf("only contract fields may be exposed: %v", rec)
	}
	if loc := first.Header().Get("Location"); loc != "/api/v1/clinical-records/"+id {
		t.Fatalf("Location = %q", loc)
	}

	again := post(h, validToken(t), key1, body)
	var replay map[string]any
	_ = json.Unmarshal(again.Body.Bytes(), &replay)
	if again.Code != http.StatusOK || replay["id"] != id || again.Header().Get("Location") == "" {
		t.Fatalf("retry = %d %v", again.Code, replay)
	}

	conflict := post(h, validToken(t), key1, `{"patientId":"`+patientB+`"}`)
	if conflict.Code != http.StatusConflict || decodeEnvelope(t, conflict).Error != "IDEMPOTENCY_CONFLICT" {
		t.Fatalf("payload change with same key = %d %s", conflict.Code, conflict.Body.String())
	}
	dup := post(h, validToken(t), "9f0c1a2e-0000-4000-8000-000000000002", body)
	if dup.Code != http.StatusConflict || decodeEnvelope(t, dup).Error != "RECORD_ALREADY_EXISTS" {
		t.Fatalf("second record = %d %s", dup.Code, dup.Body.String())
	}
}

func TestOpenRecordValidation(t *testing.T) {
	h := recordsRouter(stubCare{}, stubPatients{})
	body := `{"patientId":"` + patientA + `"}`
	cases := map[string]struct {
		key, body, field string
	}{
		"missing key":     {"", body, "Idempotency-Key"},
		"short key":       {"abc", body, "Idempotency-Key"},
		"patient no uuid": {key1, `{"patientId":"p-1"}`, "patientId"},
		"patient missing": {key1, `{}`, "patientId"},
		"unknown field":   {key1, `{"patientId":"` + patientA + `","totalCents":1}`, "totalCents"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(h, validToken(t), c.key, c.body)
			e := decodeEnvelope(t, rec)
			if rec.Code != http.StatusBadRequest || e.Error != "VALIDATION_ERROR" || len(e.Details) == 0 || e.Details[0].Field != c.field {
				t.Fatalf("got %d %+v", rec.Code, e)
			}
		})
	}
}

func TestOpenRecordMapsDomainOutcomes(t *testing.T) {
	body := `{"patientId":"` + patientA + `"}`
	cases := map[string]struct {
		h      http.Handler
		token  string
		status int
		code   string
	}{
		"assistant":          {recordsRouter(stubCare{}, stubPatients{}), tokenFor(t, assistant, "SECRETARY_ASSISTANT"), 403, "FORBIDDEN"},
		"inactive patient":   {recordsRouter(stubCare{}, stubPatients{inactive: patientA}), validToken(t), 422, "BUSINESS_RULE_VIOLATION"},
		"care unavailable":   {recordsRouter(stubCare{err: out.ErrUnavailable}, stubPatients{}), validToken(t), 503, "SERVICE_UNAVAILABLE"},
		"unassigned dentist": {recordsRouter(stubCare{}, stubPatients{}), tokenFor(t, "33333333-3333-4333-8333-333333333333", "DENTIST"), 403, "FORBIDDEN"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			rec := post(c.h, c.token, key1, body)
			if rec.Code != c.status || decodeEnvelope(t, rec).Error != c.code {
				t.Fatalf("got %d %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestListAndReadRecords(t *testing.T) {
	h := recordsRouter(stubCare{}, stubPatients{})
	created := post(h, validToken(t), key1, `{"patientId":"`+patientA+`"}`)
	var rec map[string]any
	_ = json.Unmarshal(created.Body.Bytes(), &rec)
	_ = post(h, validToken(t), "9f0c1a2e-0000-4000-8000-000000000003", `{"patientId":"`+patientB+`"}`)

	list := do(h, http.MethodGet, "/api/v1/clinical-records?limit=1", validToken(t), nil, nil)
	var page struct {
		Data []map[string]any `json:"data"`
		Meta struct{ Page, Limit, Total, TotalPages int }
	}
	_ = json.Unmarshal(list.Body.Bytes(), &page)
	if list.Code != 200 || len(page.Data) != 1 || page.Meta.Page != 1 || page.Meta.Limit != 1 || page.Meta.Total != 2 || page.Meta.TotalPages != 2 {
		t.Fatalf("list = %d %s", list.Code, list.Body.String())
	}
	if page.Data[0]["patientId"] != patientA {
		t.Fatal("oldest first (createdAt ascending)")
	}
	byPatient := do(h, http.MethodGet, "/api/v1/clinical-records?patientId="+patientA, validToken(t), nil, nil)
	_ = json.Unmarshal(byPatient.Body.Bytes(), &page)
	if len(page.Data) != 1 || page.Data[0]["id"] != rec["id"] || page.Meta.Limit != 20 {
		t.Fatalf("filter = %s", byPatient.Body.String())
	}

	got := do(h, http.MethodGet, "/api/v1/clinical-records/"+rec["id"].(string), validToken(t), nil, nil)
	if got.Code != 200 {
		t.Fatalf("read = %d", got.Code)
	}
	for path, want := range map[string]int{
		"/api/v1/clinical-records/not-a-uuid":  400,
		"/api/v1/clinical-records/" + patientB: 404,
		"/api/v1/clinical-records?limit=101":   400,
		"/api/v1/clinical-records?page=0":      400,
		"/api/v1/clinical-records?page=abc":    400,
		"/api/v1/clinical-records?status=OPEN": 400,
		"/api/v1/clinical-records?patientId=x": 400,
		"/api/v1/clinical-records?page=9":      200,
	} {
		if rec := do(h, http.MethodGet, path, validToken(t), nil, nil); rec.Code != want {
			t.Errorf("%s = %d, want %d (%s)", path, rec.Code, want, rec.Body.String())
		}
	}
}

// openGate grants every activity guard, as Appointments does for an active patient.
type openGate struct{}

func (openGate) AcquireActivityGuard(_ context.Context, _, op string) (out.Guard, error) {
	return out.Guard{ID: op, State: out.GuardOpen, Token: "t"}, nil
}

func (openGate) ResolveGuard(context.Context, string, string, string) error { return nil }

func testGuard(store *persistence.Memory, care out.CareAuthority, patients out.PatientDirectory) *usecase.GuardedWrites {
	return &usecase.GuardedWrites{Intents: store, Admissions: store, Gate: openGate{}, Care: care, Patients: patients, Clock: &clock{t: testNow}, IDs: uuids{}}
}
