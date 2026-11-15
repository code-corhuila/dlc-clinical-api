package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// The distroless image has no shell or curl: the binary probes itself.
func TestProbeReportsReadiness(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health/ready" {
			t.Errorf("probed %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	if err := Probe(context.Background(), srv.URL, time.Second); err != nil {
		t.Fatalf("ready service reported down: %v", err)
	}
	status = http.StatusServiceUnavailable
	if err := Probe(context.Background(), srv.URL, time.Second); err == nil {
		t.Fatal("a 503 must be reported as not ready")
	}
	srv.Close()
	if err := Probe(context.Background(), srv.URL, time.Second); err == nil {
		t.Fatal("an unreachable service must be reported as not ready")
	}
}
