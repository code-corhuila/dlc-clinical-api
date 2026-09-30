package httpapi

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
)

const correlationHeader = "X-Correlation-Id"

func correlation(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		correlationID := request.Header.Get(correlationHeader)
		if correlationID == "" {
			correlationID = newCorrelationID()
		}
		writer.Header().Set(correlationHeader, correlationID)
		next.ServeHTTP(writer, request)
	})
}

func newCorrelationID() string {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "correlation-id-unavailable"
	}
	return hex.EncodeToString(bytes)
}
