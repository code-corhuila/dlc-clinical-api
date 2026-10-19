package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestCorrelationIdIsEchoedAndUsedAsTraceID(t *testing.T) {
	rec := do(testRouter(nil), http.MethodGet, "/api/v1/nope", validToken(t), nil, map[string]string{"X-Correlation-Id": "e2e-saga-1"})
	if got := rec.Header().Get("X-Correlation-Id"); got != "e2e-saga-1" {
		t.Fatalf("response header = %q", got)
	}
	if e := decodeEnvelope(t, rec); e.TraceID != "e2e-saga-1" {
		t.Fatalf("traceId = %q", e.TraceID)
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
	do(testRouter(&logs, panicRoute), http.MethodGet, "/api/v1/panics", validToken(t), nil, map[string]string{"X-Correlation-Id": "corr-42"})

	lines := 0
	sc := bufio.NewScanner(&logs)
	for sc.Scan() {
		lines++
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("log line is not JSON: %s", sc.Text())
		}
		if line["correlationId"] != "corr-42" {
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
