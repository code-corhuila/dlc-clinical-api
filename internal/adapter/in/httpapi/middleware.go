package httpapi

import (
	"context"
	"crypto/rand"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/adapter/correlation"
)

type ctxKey int

const (
	loggerKey ctxKey = iota
	principalKey
)

const correlationHeader = "X-Correlation-Id"

func correlationFrom(ctx context.Context) string {
	return correlation.ID(ctx)
}

func loggerFrom(ctx context.Context) *slog.Logger {
	if l, ok := ctx.Value(loggerKey).(*slog.Logger); ok {
		return l
	}
	return slog.Default()
}

// withCorrelation reuses a safe X-Correlation-Id or generates one, returns it
// and binds a logger that writes it on every line (Norma 5.3.9).
func withCorrelation(base *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(correlationHeader)
		if !safeCorrelation(id) {
			id = newUUID()
		}
		w.Header().Set(correlationHeader, id)
		ctx := correlation.WithID(r.Context(), id)
		ctx = context.WithValue(ctx, loggerKey, base.With("correlationId", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func safeCorrelation(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, c := range id {
		if c < 0x21 || c > 0x7e {
			return false
		}
	}
	return true
}

// statusWriter records the status for the access log and turns the router's
// plain-text 404/405 into the common envelope.
type statusWriter struct {
	http.ResponseWriter
	r         *http.Request
	status    int
	rewritten bool
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status != 0 {
		return
	}
	plain := strings.HasPrefix(s.Header().Get("Content-Type"), "text/plain")
	if plain && (code == http.StatusNotFound || code == http.StatusMethodNotAllowed) {
		s.rewritten = true
		s.Header().Del("X-Content-Type-Options")
		err := notFound()
		if code == http.StatusMethodNotAllowed {
			err = methodNotAllowed()
		}
		WriteError(s.ResponseWriter, s.r, err)
		s.status = code
		return
	}
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.WriteHeader(http.StatusOK)
	}
	if s.rewritten {
		return len(b), nil
	}
	return s.ResponseWriter.Write(b)
}

// withAccessLog writes one JSON line per request (never headers or tokens)
// and converts a panic into a neutral 500 whose detail stays in the log.
func withAccessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, r: r}
		defer func() {
			if v := recover(); v != nil {
				WriteError(sw, r, fmt.Errorf("panic: %v", v))
			}
			loggerFrom(r.Context()).Info("request",
				"method", r.Method, "path", r.URL.Path, "status", sw.status,
				"durationMs", time.Since(start).Milliseconds())
		}()
		next.ServeHTTP(sw, r)
	})
}

func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
