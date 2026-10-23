package authority

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/correlation"
	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

const (
	patient = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	dentist = "11111111-1111-4111-8111-111111111111"
)

// owner fakes an internal owner contract and records what Clinical sent.
func owner(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (*Client, *[]*http.Request) {
	t.Helper()
	var seen []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r)
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return &Client{BaseURL: srv.URL, Token: "svc-token", HTTP: &http.Client{Timeout: 200 * time.Millisecond}}, &seen
}

func reply(status int, body string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

var ctx = correlation.WithID(context.Background(), "corr-77")

func TestPatientsValidationIsCalledWithServiceCredentialAndCorrelation(t *testing.T) {
	c, seen := owner(t, reply(200, `{"patientId":"`+patient+`","active":true,"version":3}`))
	active, err := Patients{Client: c}.IsActive(ctx, patient)
	if err != nil || !active {
		t.Fatalf("got %v %v", active, err)
	}
	r := (*seen)[0]
	if r.URL.Path != "/internal/v1/patients/"+patient+"/validation" || r.Header.Get("Authorization") != "Bearer svc-token" || r.Header.Get("X-Correlation-Id") != "corr-77" {
		t.Fatalf("request = %s %v", r.URL, r.Header)
	}
}

func TestPatientsAnswers(t *testing.T) {
	cases := map[string]struct {
		h      func(http.ResponseWriter, *http.Request)
		active bool
		down   bool
	}{
		"inactive":        {reply(200, `{"patientId":"`+patient+`","active":false,"version":4}`), false, false},
		"unknown patient": {reply(404, `{"error":"NOT_FOUND","message":"x"}`), false, false},
		"owner error":     {reply(500, `{"error":"INTERNAL_ERROR","message":"x"}`), false, true},
		"forbidden scope": {reply(403, `{"error":"FORBIDDEN","message":"x"}`), false, true},
		"malformed body":  {reply(200, `{"active":"yes"}`), false, true},
		"other patient":   {reply(200, `{"patientId":"bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb","active":true,"version":1}`), false, true},
		"too slow":        {func(w http.ResponseWriter, r *http.Request) { time.Sleep(time.Second); reply(200, `{}`)(w, r) }, false, true},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := owner(t, c.h)
			active, err := Patients{Client: client}.IsActive(ctx, patient)
			if c.down != errors.Is(err, out.ErrUnavailable) || active != c.active {
				t.Fatalf("got %v %v", active, err)
			}
		})
	}
}

func TestAppointmentsAssignmentValidation(t *testing.T) {
	c, seen := owner(t, reply(200, `{"patientId":"`+patient+`","dentistId":"`+dentist+`","assigned":true,"relationshipVersion":2,"sourceTypes":["APPOINTMENT"]}`))
	ok, err := Appointments{Client: c}.IsAssigned(ctx, patient, dentist)
	if err != nil || !ok {
		t.Fatalf("got %v %v", ok, err)
	}
	r := (*seen)[0]
	if r.URL.Path != "/internal/v1/patients/"+patient+"/assignments" || r.URL.Query().Get("dentistId") != dentist || r.Header.Get("X-Correlation-Id") != "corr-77" {
		t.Fatalf("request = %s", r.URL)
	}
	for name, h := range map[string]func(http.ResponseWriter, *http.Request){
		"not assigned":    reply(200, `{"patientId":"`+patient+`","dentistId":"`+dentist+`","assigned":false,"relationshipVersion":2,"sourceTypes":[]}`),
		"no such patient": reply(404, `{"error":"NOT_FOUND","message":"x"}`),
	} {
		client, _ := owner(t, h)
		if ok, err := (Appointments{Client: client}).IsAssigned(ctx, patient, dentist); ok || err != nil {
			t.Fatalf("%s: got %v %v", name, ok, err)
		}
	}
	other, _ := owner(t, reply(200, `{"patientId":"`+patient+`","dentistId":"22222222-2222-4222-8222-222222222222","assigned":true,"relationshipVersion":1,"sourceTypes":[]}`))
	if _, err := (Appointments{Client: other}).IsAssigned(ctx, patient, dentist); !errors.Is(err, out.ErrUnavailable) {
		t.Fatal("an answer about another dentist is never trusted")
	}
}

func TestOperationsWithoutAnOwnerContractFailClosed(t *testing.T) {
	c, seen := owner(t, reply(200, `{}`))
	if _, err := (Appointments{Client: c}).AssignedPatients(ctx, dentist); !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("got %v", err)
	}
	if len(*seen) != 0 {
		t.Fatal("no undocumented endpoint is invented")
	}
}
