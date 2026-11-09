package authority

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// scopes are the owner scopes Clinical calls per audience (internal-coordination.yaml x-service-scope).
var scopes = map[string]string{
	"auth-service":     "identity:validate",
	"patients-api":     "patients:validate",
	"appointments-api": "appointments:encounter-status:read assignments:read patient-gates:coordinate",
	"billing-api":      "billing:encounter-freeze:write",
}

// ScopesFor returns the scopes Clinical requests for an owner audience.
func ScopesFor(audience string) string { return scopes[audience] }

// IssuedCredential is a service JWT issued by IAM for one audience through
// client credentials (ADR-020, POST /internal/v1/service-tokens). It is cached
// until two thirds of its lifetime; the client secret is reread from its file
// on each renewal so it can rotate. It is never logged.
type IssuedCredential struct {
	endpoint, clientID, secretFile string
	body                           []byte
	http                           *http.Client
	now                            func() time.Time

	mu      sync.Mutex
	token   string
	renewAt time.Time
}

// NewIssuedCredential builds the credential for one audience and scope.
func NewIssuedCredential(iamURL, clientID, secretFile, audience, scope string, timeout time.Duration, now func() time.Time) *IssuedCredential {
	body, _ := json.Marshal(struct {
		Audience string `json:"audience"`
		Scope    string `json:"scope"`
	}{audience, scope})
	return &IssuedCredential{
		endpoint: strings.TrimRight(iamURL, "/") + "/internal/v1/service-tokens", clientID: clientID,
		secretFile: secretFile, body: body, now: now,
		http: &http.Client{Timeout: timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
}

func (c *IssuedCredential) Token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	if c.token != "" && now.Before(c.renewAt) {
		return c.token, nil
	}
	secret, err := os.ReadFile(c.secretFile)
	if err != nil {
		return "", fmt.Errorf("service client secret file: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint, bytes.NewReader(c.body))
	if err != nil {
		return "", err
	}
	req.SetBasicAuth(c.clientID, strings.TrimSpace(string(secret)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("service token issuer unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("service token issuer answered %d", resp.StatusCode)
	}
	var issued struct {
		AccessToken string `json:"accessToken"`
		ExpiresIn   int64  `json:"expiresIn"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&issued); err != nil || issued.AccessToken == "" || issued.ExpiresIn < 3 {
		return "", errors.New("service token issuer returned an invalid token")
	}
	c.token = issued.AccessToken
	c.renewAt = now.Add(time.Duration(issued.ExpiresIn*2/3) * time.Second)
	return c.token, nil
}

// OwnerCredential picks the IAM-issued token for the audience when a service
// client secret is configured, else the owner file, the shared file or value.
func OwnerCredential(iamURL, clientID, secretFile, audience, ownFile, sharedFile, shared string, timeout time.Duration) Credential {
	if secretFile != "" {
		return NewIssuedCredential(iamURL, clientID, secretFile, audience, ScopesFor(audience), timeout, time.Now)
	}
	return CredentialFor(ownFile, sharedFile, shared)
}
