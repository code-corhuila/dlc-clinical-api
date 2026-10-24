package httpapi

import (
	"crypto/rsa"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

func jwk(kid string, k *rsa.PublicKey) map[string]any {
	return map[string]any{"kty": "RSA", "kid": kid, "use": "sig", "alg": "RS256",
		"n": b64(k.N.Bytes()), "e": b64(big.NewInt(int64(k.E)).Bytes())}
}

// jwksServer publishes the keys in *keys and counts the fetches.
func jwksServer(t *testing.T, keys *[]map[string]any) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": *keys})
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

func kidToken(t *testing.T, kid string, key *rsa.PrivateKey) string {
	return signToken(t, map[string]any{"alg": "RS256", "kid": kid}, validClaims(), key)
}

func jwksVerifier(url string, now *time.Time) *Verifier {
	return &Verifier{Keys: &JWKS{URL: url, Client: http.DefaultClient, MinRefresh: time.Minute, Now: func() time.Time { return *now }},
		Issuer: testIssuer, Audience: testAudience, Now: func() time.Time { return testNow }}
}

func TestJWKSSelectsTheKeyByKidAndFollowsRotation(t *testing.T) {
	keys := []map[string]any{jwk("k1", &testKey.PublicKey)}
	srv, hits := jwksServer(t, &keys)
	now := testNow
	v := jwksVerifier(srv.URL, &now)

	if _, err := v.Verify(kidToken(t, "k1", testKey)); err != nil {
		t.Fatalf("published key rejected: %v", err)
	}
	// Auth rotates: the new key appears and the previous one stays published.
	keys = append(keys, jwk("k2", &otherKey.PublicKey))
	now = now.Add(2 * time.Minute)
	if _, err := v.Verify(kidToken(t, "k2", otherKey)); err != nil {
		t.Fatalf("rotated key rejected: %v", err)
	}
	if _, err := v.Verify(kidToken(t, "k1", testKey)); err != nil || hits.Load() != 2 {
		t.Fatalf("previous key must stay cached: %v, fetches %d", err, hits.Load())
	}
}

func TestJWKSRejectsUnknownOrMissingKidAndBoundsRefreshes(t *testing.T) {
	keys := []map[string]any{jwk("k1", &testKey.PublicKey)}
	srv, hits := jwksServer(t, &keys)
	now := testNow
	v := jwksVerifier(srv.URL, &now)

	for name, token := range map[string]string{
		"unknown kid":       kidToken(t, "nope", testKey),
		"no kid":            validToken(t),
		"kid of other key":  kidToken(t, "k1", otherKey),
		"unknown kid again": kidToken(t, "nope-2", testKey),
	} {
		if _, err := v.Verify(token); !errors.Is(err, errInvalidToken) {
			t.Fatalf("%s: got %v", name, err)
		}
	}
	// Unknown kids refresh at most once per MinRefresh: a flood cannot hammer Auth.
	if hits.Load() != 1 {
		t.Fatalf("fetches = %d, want 1", hits.Load())
	}
	now = now.Add(2 * time.Minute)
	_, _ = v.Verify(kidToken(t, "nope-3", testKey))
	if hits.Load() != 2 {
		t.Fatalf("fetches after MinRefresh = %d, want 2", hits.Load())
	}
}

func TestJWKSUnreachableIsUnavailableNotUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }))
	t.Cleanup(srv.Close)
	now := testNow
	_, err := jwksVerifier(srv.URL, &now).Verify(kidToken(t, "k1", testKey))
	if !errors.Is(err, out.ErrUnavailable) {
		t.Fatalf("got %v, want ErrUnavailable", err)
	}
}

func TestStaticKeyAcceptsAnyKid(t *testing.T) {
	if _, err := testVerifier().Verify(kidToken(t, "whatever", testKey)); err != nil {
		t.Fatal(err)
	}
}
