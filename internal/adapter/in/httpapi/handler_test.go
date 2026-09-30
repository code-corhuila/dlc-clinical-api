package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthIsPublicAndCorrelated(t *testing.T) {
	request := httptest.NewRequest(http.MethodGet, "/health", nil)
	request.Header.Set(correlationHeader, "test-correlation")
	recorder := httptest.NewRecorder()

	NewHandler().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get(correlationHeader); got != "test-correlation" {
		t.Fatalf("correlation header = %q", got)
	}
}
