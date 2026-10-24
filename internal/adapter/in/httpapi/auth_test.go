package httpapi

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

var whoami = Route{
	Pattern: "GET /api/v1/whoami",
	Handler: func(w http.ResponseWriter, r *http.Request) {
		p, _ := PrincipalFrom(r.Context())
		writeJSON(w, http.StatusOK, p)
	},
}

func TestValidTokenReachesHandlerWithPrincipal(t *testing.T) {
	rec := do(testRouter(nil, whoami), http.MethodGet, "/api/v1/whoami", validToken(t), nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body %s", rec.Code, rec.Body.String())
	}
	p, err := testVerifier().Verify(validToken(t))
	if err != nil {
		t.Fatal(err)
	}
	if p.Subject != testSubject || !slices.Contains(p.Roles, "DENTIST") || !p.HasPermission("clinical:read") {
		t.Fatalf("principal = %+v", p)
	}
}

func TestMissingTokenIs401WithChallenge(t *testing.T) {
	rec := do(testRouter(nil, whoami), http.MethodGet, "/api/v1/whoami", "", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
	if rec.Header().Get("WWW-Authenticate") == "" {
		t.Fatal("missing WWW-Authenticate challenge")
	}
	if e := decodeEnvelope(t, rec); e.Error != "UNAUTHORIZED" {
		t.Fatalf("error = %q", e.Error)
	}
}

func TestRejectedTokens(t *testing.T) {
	rs := map[string]any{"alg": "RS256", "typ": "JWT"}
	with := func(mut func(map[string]any)) map[string]any {
		c := validClaims()
		mut(c)
		return c
	}
	cases := map[string]string{
		"alg none":            signToken(t, map[string]any{"alg": "none"}, validClaims(), nil),
		"alg HS256":           signToken(t, map[string]any{"alg": "HS256"}, validClaims(), testKey),
		"alg rs256 lowercase": signToken(t, map[string]any{"alg": "rs256"}, validClaims(), testKey),
		"signed by other key": signToken(t, rs, validClaims(), otherKey),
		"expired":             signToken(t, rs, with(func(c map[string]any) { c["exp"] = testNow.Add(-31 * time.Second).Unix() }), testKey),
		"no exp":              signToken(t, rs, with(func(c map[string]any) { delete(c, "exp") }), testKey),
		"no sub":              signToken(t, rs, with(func(c map[string]any) { delete(c, "sub") }), testKey),
		"sub not uuid":        signToken(t, rs, with(func(c map[string]any) { c["sub"] = "admin" }), testKey),
		"wrong issuer":        signToken(t, rs, with(func(c map[string]any) { c["iss"] = "https://evil.test" }), testKey),
		"wrong audience":      signToken(t, rs, with(func(c map[string]any) { c["aud"] = "billing-service" }), testKey),
		"not yet valid":       signToken(t, rs, with(func(c map[string]any) { c["nbf"] = testNow.Add(time.Minute).Unix() }), testKey),
		// authentication.md: sub, iss, aud, iat, exp, jti, sid, roles and permissions are required.
		"no iat":         signToken(t, rs, with(func(c map[string]any) { delete(c, "iat") }), testKey),
		"no jti":         signToken(t, rs, with(func(c map[string]any) { delete(c, "jti") }), testKey),
		"no sid":         signToken(t, rs, with(func(c map[string]any) { delete(c, "sid") }), testKey),
		"no roles":       signToken(t, rs, with(func(c map[string]any) { delete(c, "roles") }), testKey),
		"no permissions": signToken(t, rs, with(func(c map[string]any) { delete(c, "permissions") }), testKey),
		"two segments":   "abc.def",
		"garbage":        "not-a-jwt",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			rec := do(testRouter(nil, whoami), http.MethodGet, "/api/v1/whoami", token, nil, nil)
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d", rec.Code)
			}
			decodeEnvelope(t, rec)
		})
	}
}

func TestNonBearerSchemeIs401(t *testing.T) {
	rec := do(testRouter(nil, whoami), http.MethodGet, "/api/v1/whoami", "", nil, map[string]string{"Authorization": "Basic dXNlcjpwYXNz"})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestClockSkewWithin30SecondsIsTolerated(t *testing.T) {
	c := validClaims()
	c["exp"] = testNow.Add(-29 * time.Second).Unix()
	token := signToken(t, map[string]any{"alg": "RS256"}, c, testKey)
	if _, err := testVerifier().Verify(token); err != nil {
		t.Fatalf("29s skew must be accepted: %v", err)
	}
}

func TestAudienceArrayIsAccepted(t *testing.T) {
	c := validClaims()
	c["aud"] = []string{"other", testAudience}
	token := signToken(t, map[string]any{"alg": "RS256"}, c, testKey)
	if _, err := testVerifier().Verify(token); err != nil {
		t.Fatalf("audience array must be accepted: %v", err)
	}
}

func TestHealthStaysPublic(t *testing.T) {
	if rec := do(testRouter(nil), http.MethodGet, "/health", "", nil, nil); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}

func TestParsePublicKeyAcceptsEscapedNewlines(t *testing.T) {
	pemText := publicPEM(t)
	escaped := ""
	for _, r := range pemText {
		if r == '\n' {
			escaped += `\n`
			continue
		}
		escaped += string(r)
	}
	if _, err := ParseRSAPublicKey(escaped); err != nil {
		t.Fatalf("single-line PEM must parse: %v", err)
	}
	if _, err := ParseRSAPublicKey("not a key"); err == nil {
		t.Fatal("invalid PEM must fail")
	}
}

type fakeSessions struct {
	active bool
	err    error
	got    string
}

func (f *fakeSessions) IsActive(_ context.Context, token string) (bool, error) {
	f.got = token
	return f.active, f.err
}

func sessionRouter(s SessionValidator) http.Handler {
	v := testVerifier()
	v.Sessions = s
	return NewRouter(Options{Verifier: v, Logger: slog.New(slog.NewJSONHandler(io.Discard, nil)), Routes: []Route{whoami}})
}

func TestRevokedSessionIs401AndUnavailableAuthIs503(t *testing.T) {
	token := validToken(t)
	live := &fakeSessions{active: true}
	if rec := do(sessionRouter(live), http.MethodGet, "/api/v1/whoami", token, nil, nil); rec.Code != http.StatusOK || live.got != token {
		t.Fatalf("active: %d, auth got %q", rec.Code, live.got)
	}
	if rec := do(sessionRouter(&fakeSessions{}), http.MethodGet, "/api/v1/whoami", token, nil, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked: %d", rec.Code)
	}
	if rec := do(sessionRouter(&fakeSessions{err: out.ErrUnavailable}), http.MethodGet, "/api/v1/whoami", token, nil, nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("auth down: %d", rec.Code)
	}
	bad := &fakeSessions{active: true}
	if do(sessionRouter(bad), http.MethodGet, "/api/v1/whoami", "x.y.z", nil, nil); bad.got != "" {
		t.Fatal("a locally invalid token is never sent to auth")
	}
}

// The Auth permission catalogue is not published yet: the claim names that
// grant clinical access are configuration, resolved here into booleans.
func TestVerifierResolvesTheConfiguredClinicalPermissions(t *testing.T) {
	v := testVerifier()
	v.ReadPermission, v.WritePermission = "clinical.history.read", "clinical.history.write"
	c := validClaims()
	c["permissions"] = []string{"clinical.history.read"}
	p, err := v.Verify(signToken(t, map[string]any{"alg": "RS256"}, c, testKey))
	if err != nil || !p.ClinicalRead || p.ClinicalWrite {
		t.Fatalf("principal = %+v %v", p, err)
	}
	c["permissions"] = []string{"clinical:read", "clinical:write"} // other names grant nothing
	p, _ = v.Verify(signToken(t, map[string]any{"alg": "RS256"}, c, testKey))
	if p.ClinicalRead || p.ClinicalWrite {
		t.Fatalf("unconfigured names must not grant access: %+v", p)
	}
}
