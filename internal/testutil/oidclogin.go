package testutil

// A real OIDC access token from the fixture's built-in IdP, without a browser.
//
// Every API test before this used a fake validator, and the fakes agreed with
// the code about what a token's subject is. A real token disagreed: its `sub`
// is not the OpenCloud user id (pkg/api/users.go). This helper walks the same
// authorization-code + PKCE flow the web client does, through the IdP's
// identifier API, so a test can hold exactly what the browser would send.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

// FixtureWebClientID is the OpenCloud web client the extension runs inside;
// its tokens carry `aud: web` (decisions.md #21).
const FixtureWebClientID = "web"

// FixtureAccessToken logs user in at the fixture's IdP (base, e.g.
// https://localhost:9200) and returns an access token for the web client.
// client must trust the fixture's certificate.
func FixtureAccessToken(t *testing.T, client *http.Client, base, user, password string) string {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	c := *client
	c.Jar = jar
	c.Timeout = 30 * time.Second
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	redirect := base + "/oidc-callback.html"
	verifier, challenge := pkcePair(t)

	logon, err := json.Marshal(map[string]any{
		"params": []string{user, password, "1"},
		"hello": map[string]string{
			"scope": "openid profile email", "client_id": FixtureWebClientID,
			"redirect_uri": redirect, "flow": "oidc",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodPost, base+"/signin/v1/identifier/_/logon", strings.NewReader(string(logon)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Kopano-Konnect-XSRF", "1")
	req.Header.Set("Referer", base+"/signin/v1/identifier")
	expectStatus(t, &c, req, http.StatusOK, "logon")

	q := url.Values{
		"client_id": {FixtureWebClientID}, "response_type": {"code"}, "redirect_uri": {redirect},
		"scope": {"openid profile email"}, "state": {"test"}, "prompt": {"none"}, "flow": {"oidc"},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	req, _ = http.NewRequest(http.MethodGet, base+"/signin/v1/identifier/_/authorize?"+q.Encode(), nil)
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("authorize: %v", err)
	}
	_ = resp.Body.Close()
	loc, err := url.Parse(resp.Header.Get("Location"))
	if err != nil || loc.Query().Get("code") == "" {
		t.Fatalf("authorize: no code in redirect (status %d)", resp.StatusCode)
	}

	form := url.Values{
		"grant_type": {"authorization_code"}, "client_id": {FixtureWebClientID},
		"code": {loc.Query().Get("code")}, "code_verifier": {verifier}, "redirect_uri": {redirect},
	}
	resp, err = c.PostForm(base+"/konnect/v1/token", form)
	if err != nil {
		t.Fatalf("token: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var tok struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&tok) != nil || tok.AccessToken == "" {
		t.Fatalf("token: status %d, no access token", resp.StatusCode)
	}
	return tok.AccessToken
}

func pkcePair(t *testing.T) (verifier, challenge string) {
	t.Helper()
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	verifier = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

func expectStatus(t *testing.T, c *http.Client, req *http.Request, want int, step string) {
	t.Helper()
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("%s: %v", step, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != want {
		t.Fatalf("%s: status %d, want %d", step, resp.StatusCode, want)
	}
}
