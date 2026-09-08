package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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
	// onJWKS, when set, is called on every JWKS request so tests can count them.
	onJWKS func()
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
		if idp.onJWKS != nil {
			idp.onJWKS()
		}
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
	ks := NewLazyKeySet(idp.issuer, idp.server.Client(), time.Hour, nil)
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
	v := newValidator(t, idp, "backup-api")
	// Mint with a bogus issuer by using a second IDP's signer would change keys;
	// instead override the iss claim directly.
	tok := idp.mint(t, "user-1", "backup-api", time.Now().Add(time.Hour), map[string]any{
		"iss": "https://evil.example.org",
	})
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("wrong-issuer token must be rejected")
	}
}

func TestOIDCValidate_BadSignature(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")
	tok := idp.mintWrongKey(t, "user-1")
	if _, err := v.Validate(context.Background(), tok); err == nil {
		t.Fatal("token signed with wrong key must be rejected")
	}
}

func TestOIDCValidate_Empty(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")
	if _, err := v.Validate(context.Background(), ""); err == nil {
		t.Fatal("empty token must be rejected")
	}
}

// One OpenCloud issuer mints tokens for several clients. "Signed by the right
// issuer" therefore says nothing about who the token was for.
func TestNewOIDCValidator_RequiresAnAudience(t *testing.T) {
	idp := newTestIDP(t)
	ks := NewLazyKeySet(idp.issuer, idp.server.Client(), time.Hour, nil)
	if _, err := NewOIDCValidator(OIDCConfig{Issuer: idp.issuer, KeySet: ks}); err == nil {
		t.Fatal("a validator without an audience must be refused")
	}
	if _, err := NewOIDCValidator(OIDCConfig{Audience: "backup-api", KeySet: ks}); err == nil {
		t.Fatal("a validator without an issuer must be refused")
	}
	if _, err := NewOIDCValidator(OIDCConfig{Issuer: idp.issuer, Audience: "backup-api"}); err == nil {
		t.Fatal("a validator without a key set must be refused")
	}
}

// go-jose validates expiry only when the claim is present, so a token without
// one would be valid forever.
func TestOIDCValidate_TokenWithoutExpiry(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")

	tok := idp.mint(t, "user-1", "backup-api", time.Now().Add(time.Hour), map[string]any{"exp": nil})
	_, err := v.Validate(context.Background(), tok)
	if err == nil {
		t.Fatal("a token with no expiry must be rejected")
	}
	if !strings.Contains(err.Error(), "no expiry") {
		t.Fatalf("err = %v, want the rejection to be about the missing expiry", err)
	}
}

// Anyone can put an unknown key id in a token's header without being
// authenticated. Refetching the JWKS for each one turns that into load on the
// household's identity provider.
func TestJWKSKeySet_UnknownKeyIDIsNegativelyCached(t *testing.T) {
	idp := newTestIDP(t)
	var fetches int
	idp.onJWKS = func() { fetches++ }

	ks := NewJWKSKeySet(idp.issuer+"/jwks", idp.server.Client(), time.Hour)
	ctx := context.Background()

	for range 5 {
		keys, err := ks.Key(ctx, "no-such-key")
		if err != nil {
			t.Fatalf("Key: %v", err)
		}
		if len(keys) != 0 {
			t.Fatal("an unknown key id must resolve to nothing")
		}
	}
	if fetches != 1 {
		t.Fatalf("the identity provider was asked %d times, want 1", fetches)
	}

	// A key the provider does publish is still found, from the same one fetch.
	keys, err := ks.Key(ctx, idp.keyID)
	if err != nil {
		t.Fatalf("Key: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("known key id resolved to %d keys", len(keys))
	}
}

// A provider that is not up yet must delay authentication, not startup: a
// crash-looping backup service is harder to diagnose than a 503.
func TestLazyKeySet_RetriesDiscoveryAndSaysWhy(t *testing.T) {
	ctx := context.Background()
	idp := newTestIDP(t)

	unreachable := NewLazyKeySet("http://127.0.0.1:1/idp", idp.server.Client(), time.Hour, nil)
	if _, err := unreachable.Key(ctx, "any"); !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("Key with the IdP down = %v, want ErrKeySetUnavailable", err)
	}
	// The retry is rate-limited, so the second attempt is answered from the
	// failure rather than by hammering the provider.
	if _, err := unreachable.Key(ctx, "any"); !errors.Is(err, ErrKeySetUnavailable) {
		t.Fatalf("second Key = %v, want ErrKeySetUnavailable", err)
	}

	reachable := NewLazyKeySet(idp.issuer, idp.server.Client(), time.Hour, nil)
	keys, err := reachable.Key(ctx, idp.keyID)
	if err != nil {
		t.Fatalf("Key once the IdP is reachable: %v", err)
	}
	if len(keys) != 1 {
		t.Fatalf("resolved %d keys, want 1", len(keys))
	}
}

// An unreachable identity provider is not the caller's fault: 503, not 401, so
// a client retries instead of discarding a good session.
func TestAuthenticate_AnswersUnavailableWhenKeysCannotBeFetched(t *testing.T) {
	ks := NewLazyKeySet("http://127.0.0.1:1/idp", nil, time.Hour, nil)
	v, err := NewOIDCValidator(OIDCConfig{Issuer: "http://127.0.0.1:1/idp", Audience: "backup-api", KeySet: ks})
	if err != nil {
		t.Fatalf("new validator: %v", err)
	}
	srv := NewServer(WithTokenValidator(v))

	idp := newTestIDP(t)
	tok := idp.mint(t, "user-1", "backup-api", time.Now().Add(time.Hour), nil)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/spaces", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	srv.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}

func TestAuthenticateMiddleware(t *testing.T) {
	idp := newTestIDP(t)
	v := newValidator(t, idp, "backup-api")
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
		tok := idp.mint(t, "user-1", "backup-api", time.Now().Add(time.Hour), nil)
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
