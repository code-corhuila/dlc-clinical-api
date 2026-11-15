package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthRespondsWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q", ct)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if len(body) != 1 || body["status"] != "UP" {
		t.Fatalf("health must expose only status, got %v", body)
	}
}

func TestHealthRejectsOtherMethods(t *testing.T) {
	rec := httptest.NewRecorder()
	testRouter(nil).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
}

func readinessRouter(check func(context.Context) error) http.Handler {
	return NewRouter(Options{Verifier: testVerifier(), Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Ready: check})
}

func TestReadinessReportsUpWithoutToken(t *testing.T) {
	rec := do(readinessRouter(func(context.Context) error { return nil }), http.MethodGet, "/health/ready", "", nil, nil)
	if rec.Code != http.StatusOK || rec.Body.String() != "{\"status\":\"UP\"}\n" {
		t.Fatalf("got %d %q", rec.Code, rec.Body.String())
	}
}

func TestReadinessFailsWithoutLeakingTheCause(t *testing.T) {
	rec := do(readinessRouter(func(context.Context) error { return errors.New("server selection timeout db-01:27017") }), http.MethodGet, "/health/ready", "", nil, nil)
	if rec.Code != http.StatusServiceUnavailable || decodeEnvelope(t, rec).Error != "SERVICE_UNAVAILABLE" || strings.Contains(rec.Body.String(), "db-01") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
}

func TestReadinessIsBoundedInTime(t *testing.T) {
	rec := do(readinessRouter(func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }), http.MethodGet, "/health/ready", "", nil, nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("a hanging dependency must not hang the probe, got %d", rec.Code)
	}
}
