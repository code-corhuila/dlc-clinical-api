// Package httpapi is the inbound HTTP adapter: routes, validation, error
// envelope, correlation and token verification. It depends only on inbound ports.
package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"
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
}

// NewRouter registers the service routes behind correlation, access log,
// panic recovery and authentication. Health is public and exposes only its status.
func NewRouter(o Options) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	for _, rt := range o.Routes {
		mux.HandleFunc(rt.Pattern, rt.Handler)
	}
	public := map[string]bool{"/health": true}
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
