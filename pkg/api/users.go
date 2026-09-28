// Resolving the caller's OpenCloud user id.
//
// The OIDC `sub` claim is NOT the OpenCloud user id. Measured on the 7.3.0
// fixture with a real browser login: `sub` is an opaque identifier minted by
// the built-in IdP, while graph `/me` answers with the user's OpenCloud id —
// the id CS3 grants, Space ownership and the graph user search all use. An
// external IdP (Keycloak, Pocket ID, ...) has its own `sub` again, mapped by
// OpenCloud's proxy to the same OpenCloud user.
//
// So every authorization decision that names a user — Space membership, target
// grants, the admin allow-list — is taken on the id graph `/me` returns, asked
// with the caller's own token. OpenCloud has already done the claim mapping for
// its own purposes; asking it is the one answer that agrees with it for every
// IdP it supports.
package api

import (
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"sync"
	"time"
)

// UserResolver maps an authenticated caller to their OpenCloud user id.
// Implementations must derive it from OpenCloud with the caller's own
// credential — never from client input.
type UserResolver interface {
	UserID(ctx context.Context, id Identity) (string, error)
}

// UserResolverFunc adapts a function to UserResolver.
type UserResolverFunc func(ctx context.Context, id Identity) (string, error)

// UserID implements UserResolver.
func (f UserResolverFunc) UserID(ctx context.Context, id Identity) (string, error) {
	return f(ctx, id)
}

// errNoUserID is a graph answer without an id: OpenCloud accepted the token and
// named nobody. Treated like an outage, never like a user.
var errNoUserID = errors.New("api: graph /me returned no user id")

// maxCachedUsers bounds the resolver's cache. A household has a handful of
// users with a few live tokens each; the bound is there so that a flood of
// valid tokens cannot grow the process without limit.
const maxCachedUsers = 4096

// GraphUserResolver resolves the caller's OpenCloud user id from graph /me,
// once per token. The answer is cached until the token expires: a token names
// one user for its whole life, and OpenCloud's short-lived access tokens keep
// the cost at one graph call per user every few minutes.
type GraphUserResolver struct {
	graph graphMeFetcher
	now   func() time.Time

	mu    sync.Mutex
	cache map[[sha256.Size]byte]cachedUser
}

type cachedUser struct {
	userID  string
	expires time.Time
}

// NewGraphUserResolver builds a graph-backed user resolver.
func NewGraphUserResolver(baseURL string, client *http.Client) *GraphUserResolver {
	return &GraphUserResolver{
		graph: newGraphMeFetcher(baseURL, client),
		now:   time.Now,
		cache: make(map[[sha256.Size]byte]cachedUser),
	}
}

// UserID returns the caller's OpenCloud user id. The cache key is a hash of
// the token, so the cache holds no bearer credential.
func (g *GraphUserResolver) UserID(ctx context.Context, id Identity) (string, error) {
	key := sha256.Sum256([]byte(id.Token))
	if userID, ok := g.cached(key); ok {
		return userID, nil
	}
	me, err := g.graph.fetch(ctx, "graph user resolve", id, "")
	if err != nil {
		return "", err
	}
	if me.ID == "" {
		return "", errNoUserID
	}
	g.store(key, me.ID, id.Expiry)
	return me.ID, nil
}

func (g *GraphUserResolver) cached(key [sha256.Size]byte) (string, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	c, ok := g.cache[key]
	if !ok || !g.now().Before(c.expires) {
		return "", false
	}
	return c.userID, true
}

// store caches an answer until the token's expiry. A token without a known
// expiry is not cached: there is no moment at which the entry could be said
// to have ended.
func (g *GraphUserResolver) store(key [sha256.Size]byte, userID string, expires time.Time) {
	if expires.IsZero() {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.cache) >= maxCachedUsers {
		g.evictExpiredLocked()
	}
	if len(g.cache) >= maxCachedUsers {
		return
	}
	g.cache[key] = cachedUser{userID: userID, expires: expires}
}

func (g *GraphUserResolver) evictExpiredLocked() {
	now := g.now()
	for k, c := range g.cache {
		if !now.Before(c.expires) {
			delete(g.cache, k)
		}
	}
}
