package httpapi

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/code-corhuila/dlc-clinical-api/internal/application/port/out"
)

// KeySet resolves the public key that signed a token from its kid header.
type KeySet interface {
	PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error)
}

// StaticKey is a single configured key (dev); it ignores kid.
type StaticKey struct{ Key *rsa.PublicKey }

func (s StaticKey) PublicKey(context.Context, string) (*rsa.PublicKey, error) { return s.Key, nil }

const maxJWKSBytes = 1 << 20

// JWKS reads the public-only keys Auth publishes at /api/v1/auth/jwks
// (authentication.md). Keys are cached by kid; an unknown kid refreshes the
// set at most once per MinRefresh, so a rotation is picked up without a
// restart and a flood of forged kids cannot hammer Auth.
type JWKS struct {
	URL        string
	Client     *http.Client // bounded by its own Timeout
	MinRefresh time.Duration
	Now        func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (j *JWKS) PublicKey(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	if kid == "" {
		return nil, errInvalidToken
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if k, ok := j.keys[kid]; ok {
		return k, nil
	}
	if !j.fetched.IsZero() && j.Now().Sub(j.fetched) < j.MinRefresh {
		return nil, errInvalidToken
	}
	j.fetched = j.Now()
	keys, err := j.fetch(ctx)
	if err != nil {
		return nil, fmt.Errorf("jwks: %v: %w", err, out.ErrUnavailable)
	}
	j.keys = keys
	if k, ok := keys[kid]; ok {
		return k, nil
	}
	return nil, errInvalidToken
}

func (j *JWKS) fetch(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, j.URL, nil)
	if err != nil {
		return nil, err
	}
	res, err := j.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", res.StatusCode)
	}
	var set struct {
		Keys []struct {
			Kty, Kid, Use, Alg, N, E string
		} `json:"keys"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxJWKSBytes)).Decode(&set); err != nil {
		return nil, err
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		// Only RSA signing keys for RS256; anything else is ignored, never trusted.
		if k.Kty != "RSA" || k.Kid == "" || (k.Use != "" && k.Use != "sig") || (k.Alg != "" && k.Alg != "RS256") {
			continue
		}
		n, errN := base64.RawURLEncoding.DecodeString(k.N)
		e, errE := base64.RawURLEncoding.DecodeString(k.E)
		if errN != nil || errE != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		pub := &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
		if pub.N.BitLen() < 2048 {
			continue
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, errors.New("no usable RS256 key")
	}
	return keys, nil
}
