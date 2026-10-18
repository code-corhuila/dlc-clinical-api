// Package httpapi is the inbound HTTP adapter: routes, validation, error
// envelope and correlation. It depends only on inbound ports.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// NewRouter registers the service routes. Health is public and exposes only
// its status, never database or dependency details.
func NewRouter() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	return mux
}

func health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "UP"})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
