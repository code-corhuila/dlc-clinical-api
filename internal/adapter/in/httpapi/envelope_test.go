package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type probe struct {
	PatientID string `json:"patientId"`
}

// echoRoute is a test-only protected route used to exercise the foundation.
var echoRoute = Route{
	Pattern: "POST /api/v1/probes",
	Handler: func(w http.ResponseWriter, r *http.Request) {
		var in probe
		if err := DecodeJSON(w, r, &in); err != nil {
			WriteError(w, r, err)
			return
		}
		writeJSON(w, http.StatusOK, in)
	},
}

var panicRoute = Route{
	Pattern: "GET /api/v1/panics",
	Handler: func(http.ResponseWriter, *http.Request) { panic("driver: connection refused at db-01") },
}

var internalRoute = Route{
	Pattern: "GET /api/v1/failures",
	Handler: func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, r, errors.New("mongo: server selection timeout db-01:27017"))
	},
}

func TestUnknownRouteReturnsEnvelope404(t *testing.T) {
	rec := do(testRouter(nil), http.MethodGet, "/api/v1/nope", validToken(t), nil, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
	if e := decodeEnvelope(t, rec); e.Error != "NOT_FOUND" {
		t.Fatalf("error = %q", e.Error)
	}
}

func TestWrongMethodReturnsEnvelope405(t *testing.T) {
	rec := do(testRouter(nil), http.MethodPost, "/health", "", nil, nil)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rec.Code)
	}
	if e := decodeEnvelope(t, rec); e.Error != "METHOD_NOT_ALLOWED" {
		t.Fatalf("error = %q", e.Error)
	}
}

func TestMalformedJSONIsValidationError(t *testing.T) {
	rec := do(testRouter(nil, echoRoute), http.MethodPost, "/api/v1/probes", validToken(t), []byte(`{"patientId":`), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	if e := decodeEnvelope(t, rec); e.Error != "VALIDATION_ERROR" {
		t.Fatalf("error = %q", e.Error)
	}
}

func TestUnknownFieldIsRejectedAndNamed(t *testing.T) {
	rec := do(testRouter(nil, echoRoute), http.MethodPost, "/api/v1/probes", validToken(t), []byte(`{"patientId":"x","totalCents":5}`), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
	e := decodeEnvelope(t, rec)
	if len(e.Details) != 1 || e.Details[0].Field != "totalCents" {
		t.Fatalf("details should name the unknown field: %+v", e.Details)
	}
}

func TestTrailingDataIsRejected(t *testing.T) {
	rec := do(testRouter(nil, echoRoute), http.MethodPost, "/api/v1/probes", validToken(t), []byte(`{"patientId":"x"}{}`), nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestPanicBecomesNeutral500(t *testing.T) {
	var logs bytes.Buffer
	rec := do(testRouter(&logs, panicRoute), http.MethodGet, "/api/v1/panics", validToken(t), nil, nil)
	assertNeutral500(t, rec)
	if !strings.Contains(logs.String(), "db-01") {
		t.Fatal("full detail must reach the log")
	}
}

func TestUnexpectedErrorBecomesNeutral500(t *testing.T) {
	rec := do(testRouter(nil, internalRoute), http.MethodGet, "/api/v1/failures", validToken(t), nil, nil)
	assertNeutral500(t, rec)
}

func assertNeutral500(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d", rec.Code)
	}
	e := decodeEnvelope(t, rec)
	if e.Error != "INTERNAL_ERROR" || strings.Contains(rec.Body.String(), "db-01") {
		t.Fatalf("500 must be neutral: %s", rec.Body.String())
	}
}

func TestValidationErrorCarriesFieldDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	WriteError(rec, req, Validation(FieldError{Field: "Idempotency-Key", Message: "header required, 8 to 128 characters"}))
	var e envelope
	_ = json.Unmarshal(rec.Body.Bytes(), &e)
	if rec.Code != http.StatusBadRequest || e.Details[0].Field != "Idempotency-Key" {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}
