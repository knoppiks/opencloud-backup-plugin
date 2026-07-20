package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// testIDP is a minimal fake OpenID provider: it serves discovery + JWKS and
// signs tokens, so middleware tests exercise the real verification path.
type testIDP struct {
	issuer string
	keyID  string
	priv   *rsa.PrivateKey
	server *httptest.Server
}

func newTestIDP(t *testing.T) *testIDP {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	idp := &testIDP{keyID: "test-key", priv: priv}

	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":   idp.issuer,
			"jwks_uri": idp.issuer + "/jwks",
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		set := jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{
			Key: priv.Public(), KeyID: idp.keyID, Algorithm: string(jose.RS256), Use: "sig",
		}}}
		_ = json.NewEncoder(w).Encode(set)
	})

	idp.server = httptest.NewServer(mux)
	idp.issuer = idp.server.URL
	t.Cleanup(idp.server.Close)
	return idp
}

// mint signs a token with the given claims overrides.
func (idp *testIDP) mint(t *testing.T, sub, aud string, exp time.Time, extra map[string]any) string {
	t.Helper()
	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: idp.priv},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", idp.keyID),
	)
	if err != nil {
		t.Fatalf("new signer: %v", err)
	}
	claims := map[string]any{
		"iss": idp.issuer,
		"sub": sub,
		"iat": time.Now().Unix(),
		"exp": exp.Unix(),
	}
	if aud != "" {
		claims["aud"] = aud
	}
	for k, v := range extra {
		claims[k] = v
	}
	tok, err := jwt.Signed(signer).Claims(claims).Serialize()
	if err != nil {
		t.Fatalf("sign: %v", err)
	}
	return tok
}

// mintWithKey signs with an unrelated key to simulate a bad signature.
func (idp *testIDP) mintWrongKey(t *testing.T, sub string) string {
	t.Helper()
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	signer, _ := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.RS256, Key: other},
		(&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", idp.keyID),
	)
	tok, _ := jwt.Signed(signer).Claims(map[string]any{
		"iss": idp.issuer, "sub": sub, "exp": time.Now().Add(time.Hour).Unix(),
	}).Serialize()
	return tok
}

func newValidator(t *testing.T, idp *testIDP, aud string) TokenValidator {
	t.Helper()
	ks, err := DiscoverKeySet(context.Background(), idp.issuer, idp.server.Client(), time.Hour)
	if err != nil {
		t.Fatalf("discover keyset: %v", err)
	}
	v, err := NewOIDCValidator(OIDCConfig{Issuer: idp.issuer, Audience: aud, KeySet: ks})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	return v
}

func TestOIDCValidate_Valid(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")
	tok := idp.mint(t, "user-1", "backup-api", time.Now().Add(time.Hour), map[string]any{
		"preferred_username": "alice",
	})
	id, err := v.Validate(context.Background(), tok)
	if err != nil {
		t.Fatalf("valid token rejected: %v", err)
	}
	if id.Subject != "user-1" || id.Username != "alice" || id.Token != tok {
		t.Fatalf("unexpected identity: %+v", id)
	}
}

func TestOIDCValidate_Expired(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")
	tok := idp.mint(t, "user-1", "backup-api", time.Now().Add(-time.Hour), nil)
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("expired token must be rejected")
	}
}

func TestOIDCValidate_WrongAudience(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")
	tok := idp.mint(t, "user-1", "some-other-api", time.Now().Add(time.Hour), nil)
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("wrong-audience token must be rejected")
	}
}

func TestOIDCValidate_WrongIssuer(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "")
	// Mint with a bogus issuer by using a second IDP's signer would change keys;
	// instead override the iss claim directly.
	tok := idp.mint(t, "user-1", "", time.Now().Add(time.Hour), map[string]any{
		"iss": "https://evil.example.org",
	})
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("wrong-issuer token must be rejected")
	}
}

func TestOIDCValidate_BadSignature(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "")
	tok := idp.mintWrongKey(t, "user-1")
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("token signed with wrong key must be rejected")
	}
}

func TestOIDCValidate_Empty(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "")
	if _, err := v.Validate(context.Background(), ""); err == nil {
		t.Fatal("empty token must be rejected")
	}
}

func TestAuthenticateMiddleware(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "")
	srv := NewServer(WithTokenValidator(v))

	// Reach an authed route: /api/v1/spaces (no space reader -> 503, but only
	// after auth succeeds; without a token it must be 401).
	t.Run("no token -> 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})

	t.Run("bad token -> 401", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
		req.Header.Set("Authorization", "Bearer not-a-jwt")
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("want 401, got %d", rec.Code)
		}
	})

	t.Run("valid token passes auth", func(t *testing.T) {
		tok := idp.mint(t, "user-1", "", time.Now().Add(time.Hour), nil)
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		srv.Handler().ServeHTTP(rec, req)
		// Auth passed; no space reader wired -> 503, not 401.
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("valid token should pass auth, got 401")
		}
	})
}
