// OIDC bearer-token authentication for the backup API.
//
// The middleware validates access tokens issued by the OpenCloud IDP (Konnect)
// statelessly, per request: it fetches the issuer's JWKS (discovered from
// issuer metadata) and verifies the token signature, issuer, audience, and
// expiry. There are no server-side sessions of our own (phase-2 deliverable 1).
//
// On 7.3.0 the access token carries no role/group claim (phase-0-findings.md,
// admin-role spike), so admin status is resolved separately (see admin.go), not
// from the token.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// Identity is the authenticated caller extracted from a validated token. It is
// placed in the request context and never carries key material.
type Identity struct {
	// Subject is the OIDC `sub` claim — the stable user id.
	Subject string
	// Username is the preferred human-readable name, if present.
	Username string
	// Token is the raw bearer token, retained so downstream calls (CS3 read,
	// graph admin check) can forward the caller's credential. Never logged.
	Token string
}

type identityCtxKey struct{}

// IdentityFrom returns the authenticated identity from ctx, if any.
func IdentityFrom(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(Identity)
	return id, ok
}

// withIdentity returns a copy of ctx carrying id.
func withIdentity(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// signatureAlgorithms are the JWS algorithms we accept, matching the OpenCloud
// IDP's advertised set (phase-0-findings.md). go-jose v4 requires the caller to
// pin these explicitly.
var signatureAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
}

// KeySet resolves signing keys by key id. Implementations must be safe for
// concurrent use.
type KeySet interface {
	// Key returns the candidate verification keys for the given key id.
	Key(ctx context.Context, keyID string) ([]jose.JSONWebKey, error)
}

// TokenValidator validates bearer tokens and extracts an Identity. It is the
// boundary the HTTP middleware depends on, so tests can substitute a fake.
type TokenValidator interface {
	Validate(ctx context.Context, rawToken string) (Identity, error)
}

// OIDCConfig configures the OpenCloud/Konnect token validator.
type OIDCConfig struct {
	// Issuer is the expected `iss` claim, e.g. https://cloud.example.org.
	Issuer string
	// Audience, when non-empty, is required in the token `aud` claim.
	Audience string
	// KeySet supplies verification keys (usually a JWKS-backed cache).
	KeySet KeySet
	// Leeway tolerates small clock skew on exp/nbf/iat. Defaults to 1 minute.
	Leeway time.Duration
	// Now overrides the clock for tests; defaults to time.Now.
	Now func() time.Time
}

// oidcValidator is the concrete TokenValidator for OpenCloud access tokens.
type oidcValidator struct {
	cfg OIDCConfig
}

// NewOIDCValidator constructs a validator. It does not perform network I/O;
// discovery/JWKS fetching is the KeySet's responsibility.
func NewOIDCValidator(cfg OIDCConfig) (TokenValidator, error) {
	if cfg.Issuer == "" {
		return nil, errors.New("oidc: issuer is required")
	}
	if cfg.KeySet == nil {
		return nil, errors.New("oidc: key set is required")
	}
	if cfg.Leeway == 0 {
		cfg.Leeway = time.Minute
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &oidcValidator{cfg: cfg}, nil
}

// standardClaims is the subset of claims we validate and extract.
type standardClaims struct {
	jwt.Claims
	PreferredUsername string `json:"preferred_username,omitempty"`
	Name              string `json:"name,omitempty"`
	Email             string `json:"email,omitempty"`
}

// Validate parses, verifies, and checks a raw bearer token, returning the
// caller's Identity. Errors are intentionally coarse — callers map them to 401
// without leaking specifics to the client.
func (v *oidcValidator) Validate(ctx context.Context, rawToken string) (Identity, error) {
	if rawToken == "" {
		return Identity{}, errInvalidToken
	}

	tok, err := jwt.ParseSigned(rawToken, signatureAlgorithms)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: parse: %v", errInvalidToken, err)
	}
	if len(tok.Headers) == 0 {
		return Identity{}, fmt.Errorf("%w: no JWS header", errInvalidToken)
	}

	keys, err := v.cfg.KeySet.Key(ctx, tok.Headers[0].KeyID)
	if err != nil {
		return Identity{}, fmt.Errorf("%w: key lookup: %v", errInvalidToken, err)
	}
	if len(keys) == 0 {
		return Identity{}, fmt.Errorf("%w: no matching key", errInvalidToken)
	}

	var claims standardClaims
	verified := false
	for _, k := range keys {
		if err := tok.Claims(k.Key, &claims); err == nil {
			verified = true
			break
		}
	}
	if !verified {
		return Identity{}, fmt.Errorf("%w: signature", errInvalidToken)
	}

	expected := jwt.Expected{
		Issuer: v.cfg.Issuer,
		Time:   v.cfg.Now(),
	}
	if v.cfg.Audience != "" {
		expected.AnyAudience = jwt.Audience{v.cfg.Audience}
	}
	if err := claims.Validate(expected); err != nil {
		// Covers expiry, wrong issuer, wrong audience, not-yet-valid.
		if v.cfg.Leeway > 0 {
			if err2 := claims.ValidateWithLeeway(expected, v.cfg.Leeway); err2 == nil {
				err = nil
			}
		}
		if err != nil {
			return Identity{}, fmt.Errorf("%w: claims: %v", errInvalidToken, err)
		}
	}
	if claims.Subject == "" {
		return Identity{}, fmt.Errorf("%w: missing subject", errInvalidToken)
	}

	return Identity{
		Subject:  claims.Subject,
		Username: firstNonEmpty(claims.PreferredUsername, claims.Name, claims.Email),
		Token:    rawToken,
	}, nil
}

// errInvalidToken is the sentinel for any token-validation failure. It is not
// surfaced verbatim to clients (see the 401 handler).
var errInvalidToken = errors.New("invalid token")

// Authenticate is middleware that requires a valid bearer token. It extracts the
// Identity into the request context or responds 401.
func (s *Server) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.validator == nil {
			// Fail closed: without a configured validator we cannot authenticate.
			writeError(w, http.StatusUnauthorized, "unauthorized", "authentication unavailable")
			return
		}
		raw := bearerToken(r)
		if raw == "" {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		id, err := s.validator.Validate(r.Context(), raw)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
	})
}

// bearerToken extracts the token from the Authorization header.
func bearerToken(r *http.Request) string {
	h := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return strings.TrimSpace(h[len(prefix):])
	}
	return ""
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// --- JWKS-backed KeySet ----------------------------------------------------

// jwksKeySet fetches and caches a JWKS from a URL discovered via OIDC metadata.
// It is safe for concurrent use and refreshes on cache miss (key rotation).
type jwksKeySet struct {
	jwksURI string
	client  *http.Client
	ttl     time.Duration
	now     func() time.Time

	mu        sync.RWMutex
	keys      map[string][]jose.JSONWebKey
	fetchedAt time.Time
}

// DiscoverKeySet builds a JWKS-backed KeySet by resolving the issuer's
// OpenID configuration to find its jwks_uri, then lazily fetching keys.
func DiscoverKeySet(ctx context.Context, issuer string, client *http.Client, cacheTTL time.Duration) (KeySet, error) {
	if client == nil {
		client = http.DefaultClient
	}
	jwksURI, err := discoverJWKSURI(ctx, issuer, client)
	if err != nil {
		return nil, err
	}
	if cacheTTL == 0 {
		cacheTTL = time.Hour
	}
	return &jwksKeySet{
		jwksURI: jwksURI,
		client:  client,
		ttl:     cacheTTL,
		now:     time.Now,
	}, nil
}

// NewJWKSKeySet builds a JWKS-backed KeySet from an explicit JWKS URI, skipping
// discovery. Useful for tests and deployments that pin the JWKS endpoint.
func NewJWKSKeySet(jwksURI string, client *http.Client, cacheTTL time.Duration) KeySet {
	if client == nil {
		client = http.DefaultClient
	}
	if cacheTTL == 0 {
		cacheTTL = time.Hour
	}
	return &jwksKeySet{jwksURI: jwksURI, client: client, ttl: cacheTTL, now: time.Now}
}

func discoverJWKSURI(ctx context.Context, issuer string, client *http.Client) (string, error) {
	metaURL := strings.TrimRight(issuer, "/") + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metaURL, nil)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("oidc discovery: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("oidc discovery: status %d", resp.StatusCode)
	}
	var meta struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return "", fmt.Errorf("oidc discovery: decode: %w", err)
	}
	if meta.JWKSURI == "" {
		return "", errors.New("oidc discovery: no jwks_uri")
	}
	return meta.JWKSURI, nil
}

// Key returns the keys matching keyID, fetching/refreshing the JWKS as needed.
func (k *jwksKeySet) Key(ctx context.Context, keyID string) ([]jose.JSONWebKey, error) {
	if keys, ok := k.cached(keyID); ok {
		return keys, nil
	}
	if err := k.refresh(ctx); err != nil {
		return nil, err
	}
	keys, _ := k.cached(keyID)
	return keys, nil
}

func (k *jwksKeySet) cached(keyID string) ([]jose.JSONWebKey, bool) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	if k.keys == nil || k.now().Sub(k.fetchedAt) > k.ttl {
		return nil, false
	}
	keys, ok := k.keys[keyID]
	return keys, ok
}

func (k *jwksKeySet) refresh(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.jwksURI, nil)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	resp, err := k.client.Do(req)
	if err != nil {
		return fmt.Errorf("jwks fetch: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("jwks fetch: status %d", resp.StatusCode)
	}
	var set jose.JSONWebKeySet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return fmt.Errorf("jwks decode: %w", err)
	}
	byKID := make(map[string][]jose.JSONWebKey, len(set.Keys))
	for _, key := range set.Keys {
		byKID[key.KeyID] = append(byKID[key.KeyID], key)
	}
	k.mu.Lock()
	k.keys = byKID
	k.fetchedAt = k.now()
	k.mu.Unlock()
	return nil
}
