// Package httpapi is the inbound HTTP adapter: routes, validation, error
// envelope, correlation and token verification. It depends only on inbound ports.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Route binds a method-qualified pattern to a handler.
type Route struct {
	Pattern string
	Handler http.HandlerFunc
}

// Options are the concrete collaborators supplied by the composition root.
type Options struct {
	Verifier *Verifier
	Logger   *slog.Logger
	Routes   []Route
	// Ready checks the dependencies the service cannot work without (MongoDB).
	Ready func(context.Context) error
}

// readyTimeout bounds the readiness probe so a hanging dependency answers 503.
const readyTimeout = 2 * time.Second

// NewRouter registers the service routes behind correlation, access log,
// panic recovery and authentication. Health is public and exposes only its status.
func NewRouter(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	mux.HandleFunc("GET /health/ready", ready(o.Ready))
	for _, rt := range o.Routes {
		mux.HandleFunc(rt.Pattern, rt.Handler)
	}
	public := map[string]bool{"/health": true, "/health/ready": true}
	return withCorrelation(o.Logger, withAccessLog(withAuthentication(o.Verifier, public, mux)))
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ready reports UP only when the dependencies answer within the bound; the
// cause of a failure goes to the log, never to the probe response.
func ready(check func(context.Context) error) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if check != nil {
			ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
			defer cancel()
			if err := check(ctx); err != nil {
				loggerFrom(r.Context()).Warn("readiness check failed", "error", err.Error())
				WriteError(w, r, &httpError{status: http.StatusServiceUnavailable, code: "SERVICE_UNAVAILABLE", message: "the service is not ready"})
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
	}
}
