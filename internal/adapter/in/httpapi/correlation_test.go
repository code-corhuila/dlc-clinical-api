package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

const sagaID = "5a9a1d2e-3b4c-4d5e-8f60-718293a4b5c6"

func TestCorrelationIdIsEchoedAndUsedAsTraceID(t *testing.T) {
	rec := do(testRouter(nil), http.MethodGet, "/api/v1/nope", validToken(t), nil, map[string]string{"X-Correlation-Id": sagaID})
	if got := rec.Header().Get("X-Correlation-Id"); got != sagaID {
		t.Fatalf("response header = %q", got)
	}
	if e := decodeEnvelope(t, rec); e.TraceID != sagaID {
		t.Fatalf("traceId = %q", e.TraceID)
	}
}

// The id travels into every event's metadata.correlationId, which the event
// schemas declare as format uuid: any other value is replaced, not echoed.
func TestNonUUIDCorrelationIdIsReplaced(t *testing.T) {
	rec := do(testRouter(nil), http.MethodGet, "/api/v1/nope", validToken(t), nil, map[string]string{"X-Correlation-Id": "e2e-saga-1"})
	got := rec.Header().Get("X-Correlation-Id")
	if got == "e2e-saga-1" || len(got) != 36 {
		t.Fatalf("a non-UUID id must be replaced by a generated UUID, got %q", got)
	}
}

func TestCorrelationIdIsGeneratedWhenMissing(t *testing.T) {
	rec := do(testRouter(nil), http.MethodGet, "/api/v1/nope", validToken(t), nil, nil)
	id := rec.Header().Get("X-Correlation-Id")
	if len(id) != 36 {
		t.Fatalf("expected a generated UUID, got %q", id)
	}
	if e := decodeEnvelope(t, rec); e.TraceID != id {
		t.Fatalf("traceId %q differs from header %q", e.TraceID, id)
	}
}

func TestUnsafeCorrelationIdIsReplaced(t *testing.T) {
	rec := do(testRouter(nil), http.MethodGet, "/health", "", nil, map[string]string{"X-Correlation-Id": strings.Repeat("a", 129)})
	if got := rec.Header().Get("X-Correlation-Id"); len(got) != 36 {
		t.Fatalf("oversized id must be replaced, got %q", got)
	}
}

func TestEveryLogLineIsJSONWithCorrelationId(t *testing.T) {
	var logs bytes.Buffer
	do(testRouter(&logs, panicRoute), http.MethodGet, "/api/v1/panics", validToken(t), nil, map[string]string{"X-Correlation-Id": "42424242-4242-4242-8242-424242424242"})

	lines := 0
	sc := bufio.NewScanner(&logs)
	for sc.Scan() {
		lines++
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if line["correlationId"] != "42424242-4242-4242-8242-424242424242" {
			t.Fatalf("log line without correlationId: %s", sc.Text())
		}
	}
	if lines < 2 {
		t.Fatalf("expected error and access lines, got %d", lines)
	}
}

func TestAccessLogNeverContainsTheToken(t *testing.T) {
	var logs bytes.Buffer
	token := validToken(t)
	do(testRouter(&logs), http.MethodGet, "/api/v1/nope", token, nil, nil)
	if strings.Contains(logs.String(), token) {
		t.Fatal("token leaked into logs")
	}
}
