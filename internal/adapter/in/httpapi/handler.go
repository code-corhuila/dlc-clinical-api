// Package httpapi is the inbound HTTP adapter. It translates HTTP concerns to input ports.
package httpapi

import "net/http"

// NewHandler exposes only operational endpoints until the Clinical use cases and security adapter
// are implemented. Business routes must be registered here through input ports, never directly to
// persistence adapters.
func NewHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", health)
	return correlation(mux)
}

func health(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write([]byte(`{"status":"ok"}`))
}
