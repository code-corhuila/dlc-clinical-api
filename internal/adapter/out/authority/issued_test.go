package authority

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

// ADR-020: Clinical exchanges client credentials at IAM for service tokens, cached until 2/3 of their life.
func TestIssuedCredentialRequestsWithBasicCredentialsAndCachesUntilTwoThirdsOfItsLife(t *testing.T) {
	var issued atomic.Int32
	var seen []string
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seen = append(seen, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization")+" "+string(body))
		fmt.Fprintf(w, `{"accessToken":"tok-%d","tokenType":"Bearer","expiresIn":900,"scope":"patients:validate"}`, issued.Add(1))
	}))
	defer iam.Close()
	secret := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(secret, []byte("s3cret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	c := NewIssuedCredential(iam.URL, "clinical-api", secret, "patients-api", "patients:validate", 2*time.Second,
		func() time.Time { return now })

	want := []string{"tok-1", "tok-1", "tok-2"}
	for i, advance := range []time.Duration{0, 599 * time.Second, time.Second} {
		now = now.Add(advance)
		got, err := c.Token()
		if err != nil || got != want[i] {
			t.Fatalf("call %d: got %q, %v; want %q", i, got, err, want[i])
		}
	}
	basic := "Basic " + base64.StdEncoding.EncodeToString([]byte("clinical-api:s3cret"))
	if len(seen) != 2 || seen[0] != `POST /internal/v1/service-tokens `+basic+` {"audience":"patients-api","scope":"patients:validate"}` {
		t.Fatalf("requests = %q", seen)
	}
}

func TestIssuedCredentialFailsWhenIAMRejectsOrIsUnreachable(t *testing.T) {
	iam := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	secret := filepath.Join(t.TempDir(), "secret")
	_ = os.WriteFile(secret, []byte("s3cret"), 0o600)
	c := NewIssuedCredential(iam.URL, "clinical-api", secret, "billing-api", "billing:encounter-freeze:write",
		time.Second, time.Now)
	if _, err := c.Token(); err == nil {
		t.Fatal("rejected client produced a token")
	}
	iam.Close()
	if _, err := c.Token(); err == nil {
		t.Fatal("unreachable IAM produced a token")
	}
}

func TestScopesForCoverEveryOwnerAudienceClinicalCalls(t *testing.T) {
	for audience, want := range map[string]string{
		"auth-service":     "identity:validate",
		"patients-api":     "patients:validate",
		"appointments-api": "appointments:encounter-status:read assignments:read patient-gates:coordinate",
		"billing-api":      "billing:encounter-freeze:write",
	} {
		if got := ScopesFor(audience); got != want {
			t.Errorf("ScopesFor(%q) = %q, want %q", audience, got, want)
		}
	}
}
