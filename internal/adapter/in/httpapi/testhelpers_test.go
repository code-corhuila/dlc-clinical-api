package httpapi

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

const (
	testIssuer   = "https://auth.dilucca.test"
	testAudience = "clinical-service"
	testSubject  = "11111111-1111-4111-8111-111111111111"
)

var (
	testKey  = mustKey()
	otherKey = mustKey()
	testNow  = time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
)

func mustKey() *rsa.PrivateKey {
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		panic(err)
	}
	return k
}

func b64(v []byte) string { return base64.RawURLEncoding.EncodeToString(v) }

func validClaims() map[string]any {
	return map[string]any{
		"sub":         testSubject,
		"iss":         testIssuer,
		"aud":         testAudience,
		"iat":         testNow.Add(-time.Minute).Unix(),
		"exp":         testNow.Add(time.Hour).Unix(),
		"jti":         "jti-1",
		"sid":         "sid-1",
		"roles":       []string{"DENTIST"},
		"permissions": []string{"clinical:read"},
	}
}

// signToken builds a compact JWT with an arbitrary header so negative cases
// (alg none, HS256, foreign key) can be expressed directly.
func signToken(t *testing.T, header, claims map[string]any, key *rsa.PrivateKey) string {
	t.Helper()
	h, _ := json.Marshal(header)
	c, _ := json.Marshal(claims)
	input := b64(h) + "." + b64(c)
	if key == nil {
		return input + "."
	}
	sum := sha256.Sum256([]byte(input))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return input + "." + b64(sig)
}

func validToken(t *testing.T) string {
	return signToken(t, map[string]any{"alg": "RS256", "typ": "JWT"}, validClaims(), testKey)
}

func testVerifier() *Verifier {
	return &Verifier{
		Key:      &testKey.PublicKey,
		Issuer:   testIssuer,
		Audience: testAudience,
		Now:      func() time.Time { return testNow },
	}
}

func testRouter(logs io.Writer, extra ...Route) http.Handler {
	if logs == nil {
		logs = io.Discard
	}
	return NewRouter(Options{
		Verifier: testVerifier(),
		Logger:   slog.New(slog.NewJSONHandler(logs, nil)),
		Routes:   extra,
	})
}

func do(h http.Handler, method, path, token string, body []byte, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

type envelope struct {
	Error   string `json:"error"`
	Message string `json:"message"`
	Details []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"details"`
	TraceID string `json:"traceId"`
}

func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("content type = %q, body %s", ct, rec.Body.String())
	}
	var e envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &e); err != nil {
		t.Fatalf("invalid envelope %q: %v", rec.Body.String(), err)
	}
	if e.Error == "" || e.Message == "" || e.TraceID == "" {
		t.Fatalf("envelope missing error/message/traceId: %+v", e)
	}
	return e
}

func publicPEM(t *testing.T) string {
	t.Helper()
	der, err := x509.MarshalPKIXPublicKey(&testKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}
