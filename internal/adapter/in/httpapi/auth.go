package httpapi

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
)

const clockSkew = 30 * time.Second

// Principal is the authenticated staff member. Authorization of resources is
// decided by the use cases, never by token possession alone.
type Principal struct {
	Subject     string   `json:"subject"`
	SessionID   string   `json:"sessionId,omitempty"`
	Roles       []string `json:"roles"`
	Permissions []string `json:"permissions"`
}

func (p Principal) HasRole(role string) bool       { return slices.Contains(p.Roles, role) }
func (p Principal) HasPermission(perm string) bool { return slices.Contains(p.Permissions, perm) }

// PrincipalFrom returns the principal stored by the authentication middleware.
func PrincipalFrom(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(principalKey).(Principal)
	return p, ok
}

// Verifier validates RS256 JWTs by itself (Norma 5.3.7): closed algorithm
// list, public key only, mandatory exp/sub, issuer and audience allowlists.
type Verifier struct {
	Key      *rsa.PublicKey
	Issuer   string
	Audience string
	Now      func() time.Time
	// Sessions, when set, asks auth whether the session is still active after
	// the local checks pass; nil keeps local verification only (dev).
	Sessions SessionValidator
}

// SessionValidator reports whether auth still considers a token active.
type SessionValidator interface {
	IsActive(ctx context.Context, accessToken string) (bool, error)
}

var errInvalidToken = errors.New("invalid token")

type claims struct {
	Sub         string          `json:"sub"`
	Iss         string          `json:"iss"`
	Aud         json.RawMessage `json:"aud"`
	Exp         *int64          `json:"exp"`
	Nbf         *int64          `json:"nbf"`
	Sid         string          `json:"sid"`
	Roles       []string        `json:"roles"`
	Permissions []string        `json:"permissions"`
}

// Verify returns the principal of a valid token or errInvalidToken.
func (v *Verifier) Verify(token string) (Principal, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return Principal{}, errInvalidToken
	}
	var header struct {
		Alg string `json:"alg"`
	}
	if !decodeSegment(parts[0], &header) || header.Alg != "RS256" {
		return Principal{}, errInvalidToken
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return Principal{}, errInvalidToken
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(v.Key, crypto.SHA256, sum[:], sig) != nil {
		return Principal{}, errInvalidToken
	}
	var c claims
	if !decodeSegment(parts[1], &c) {
		return Principal{}, errInvalidToken
	}
	now := v.Now()
	switch {
	case c.Exp == nil || now.After(time.Unix(*c.Exp, 0).Add(clockSkew)):
	case c.Nbf != nil && now.Add(clockSkew).Before(time.Unix(*c.Nbf, 0)):
	case !isUUID(c.Sub), c.Iss != v.Issuer, !audienceMatches(c.Aud, v.Audience):
	default:
		return Principal{Subject: c.Sub, SessionID: c.Sid, Roles: c.Roles, Permissions: c.Permissions}, nil
	}
	return Principal{}, errInvalidToken
}

func decodeSegment(seg string, dst any) bool {
	raw, err := base64.RawURLEncoding.DecodeString(seg)
	return err == nil && json.Unmarshal(raw, dst) == nil
}

func audienceMatches(raw json.RawMessage, want string) bool {
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return one == want
	}
	var many []string
	return json.Unmarshal(raw, &many) == nil && slices.Contains(many, want)
}

func withAuthentication(v *Verifier, public map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if public[r.URL.Path] {
			next.ServeHTTP(w, r)
			return
		}
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			WriteError(w, r, unauthorized())
			return
		}
		p, err := v.Verify(token)
		if err != nil {
			WriteError(w, r, unauthorized())
			return
		}
		if v.Sessions != nil {
			active, err := v.Sessions.IsActive(r.Context(), token)
			switch {
			case err != nil:
				WriteError(w, r, err)
				return
			case !active:
				WriteError(w, r, unauthorized())
				return
			}
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalKey, p)))
	})
}

// ParseRSAPublicKey accepts a PEM public key, also on one line with literal
// "\n" sequences as stored in a .env file.
func ParseRSAPublicKey(text string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(strings.ReplaceAll(text, `\n`, "\n")))
	if block == nil {
		return nil, errors.New("JWT public key: no PEM block found")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, errors.New("JWT public key: " + err.Error())
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("JWT public key: not an RSA key")
	}
	return rsaKey, nil
}
