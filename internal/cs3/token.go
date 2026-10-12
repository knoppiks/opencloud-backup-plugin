package cs3

// Reusing the worker's access token.
//
// Every gateway call needs a reva access token, and minting one is a round trip
// of its own. Uncached, a backup of a Space with a thousand files makes a
// thousand extra Authenticate calls, and an idle service still makes two per
// minute per Space just to read its schedule — traffic aimed at the very
// OpenCloud instance this service is supposed to sit quietly beside.
//
// The token says when it expires, so that is what the cache honours: it is
// reused until shortly before its own expiry and dropped the moment reva
// rejects it. Nothing here inspects the token for any other purpose, and
// nothing here trusts it: it is this service's own credential, not a caller's,
// and it is never used to make an authorization decision.

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"
	"time"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
)

const (
	// tokenRefreshMargin is how long before its expiry a token is replaced, so
	// a call never leaves with a token that expires in flight.
	tokenRefreshMargin = 30 * time.Second
	// tokenFallbackTTL is how long a token whose expiry cannot be read is
	// reused. Short, because the alternative is guessing.
	tokenFallbackTTL = 30 * time.Second
)

// CachedAuth reuses an Authenticator's token until it is about to expire.
type CachedAuth struct {
	inner Authenticator
	now   func() time.Time

	mu      sync.Mutex
	token   string
	expires time.Time
}

var _ Authenticator = (*CachedAuth)(nil)

// NewCachedAuth wraps an authenticator with token reuse.
func NewCachedAuth(inner Authenticator) *CachedAuth {
	return &CachedAuth{inner: inner, now: time.Now}
}

// Token returns the cached token, minting a new one when there is none or the
// one held is about to expire.
func (a *CachedAuth) Token(ctx context.Context) (string, error) {
	if tok, ok := a.cached(); ok {
		return tok, nil
	}

	tok, err := a.inner.Token(ctx)
	if err != nil {
		return "", err
	}

	now := a.now()
	expires := now.Add(tokenFallbackTTL)
	if exp, ok := tokenExpiry(tok); ok {
		expires = exp.Add(-tokenRefreshMargin)
	}

	a.mu.Lock()
	a.token, a.expires = tok, expires
	a.mu.Unlock()
	return tok, nil
}

// Invalidate drops the cached token. It is called when reva rejects one: a
// token the server will not accept must not be presented again until it happens
// to expire.
func (a *CachedAuth) Invalidate() {
	a.mu.Lock()
	a.token, a.expires = "", time.Time{}
	a.mu.Unlock()
}

// cached returns the held token while it is still safely usable.
func (a *CachedAuth) cached() (string, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.token == "" || !a.now().Before(a.expires) {
		return "", false
	}
	return a.token, true
}

// tokenExpiry reads the "exp" claim of a JWT.
//
// The token is not verified and is not being trusted: this is the service's own
// credential, obtained over the gateway connection a moment ago, and the claim
// is used for one thing only — deciding when to ask for a new one. An
// unreadable token is not an error here; the caller falls back to a short reuse
// window.
func tokenExpiry(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(claims.Exp, 0).UTC(), true
}

// invalidator is implemented by authenticators that hold a token. A token reva
// has rejected is dropped rather than presented again.
type invalidator interface{ Invalidate() }

// forgetToken tells the authenticator its token was refused.
func (c *Client) forgetToken() {
	if inv, ok := c.auth.(invalidator); ok {
		inv.Invalidate()
	}
}

// status turns a CS3 response status into an error, dropping the cached token
// first when the answer was "who are you".
func (c *Client) status(st *rpc.Status, op string) error {
	c.noteStatus(st)
	return statusErr(st, op)
}

// noteStatus drops the cached token when a call was refused for lack of one. It
// exists separately for the call sites that map status codes themselves.
func (c *Client) noteStatus(st *rpc.Status) {
	if st.GetCode() == rpc.Code_CODE_UNAUTHENTICATED {
		c.forgetToken()
	}
}
