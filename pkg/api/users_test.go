package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// fakeGraphMe answers /graph/v1.0/me with the user id mapped to the bearer
// token, and counts the calls.
func fakeGraphMe(t *testing.T, ids map[string]string) (*httptest.Server, *atomic.Int32, *string) {
	t.Helper()
	var calls atomic.Int32
	var query string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		query = r.URL.RawQuery
		if r.URL.Path != "/graph/v1.0/me" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		id, ok := ids[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "displayName": "someone"})
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &query
}

func TestGraphUserResolver_ReturnsTheGraphIdNotTheSubject(t *testing.T) {
	srv, _, query := fakeGraphMe(t, map[string]string{"tok": "8b51fb37-user"})
	r := NewGraphUserResolver(srv.URL, srv.Client())

	got, err := r.UserID(context.Background(), Identity{
		Subject: "opaque-idp-subject", Token: "tok", Expiry: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "8b51fb37-user" {
		t.Fatalf("user id = %q, want the graph id", got)
	}
	if *query != "" {
		t.Fatalf("the bare /me document is enough; got query %q", *query)
	}
}

func TestGraphUserResolver_CachesPerTokenUntilExpiry(t *testing.T) {
	srv, calls, _ := fakeGraphMe(t, map[string]string{"tok-a": "user-a", "tok-b": "user-b"})
	r := NewGraphUserResolver(srv.URL, srv.Client())
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	r.now = func() time.Time { return now }

	a := Identity{Token: "tok-a", Expiry: now.Add(5 * time.Minute)}
	b := Identity{Token: "tok-b", Expiry: now.Add(5 * time.Minute)}
	for range 3 {
		if got, _ := r.UserID(context.Background(), a); got != "user-a" {
			t.Fatalf("a = %q", got)
		}
		if got, _ := r.UserID(context.Background(), b); got != "user-b" {
			t.Fatalf("b = %q", got)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("graph calls = %d, want one per token", n)
	}

	now = now.Add(5 * time.Minute)
	if _, err := r.UserID(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("graph calls = %d, want a fresh lookup once the token has expired", n)
	}
}

func TestGraphUserResolver_DoesNotCacheWithoutExpiry(t *testing.T) {
	srv, calls, _ := fakeGraphMe(t, map[string]string{"tok": "user"})
	r := NewGraphUserResolver(srv.URL, srv.Client())
	for range 2 {
		if _, err := r.UserID(context.Background(), Identity{Token: "tok"}); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 2 {
		t.Fatalf("graph calls = %d, want no caching without an expiry", n)
	}
}

func TestGraphUserResolver_CacheHoldsNoToken(t *testing.T) {
	srv, _, _ := fakeGraphMe(t, map[string]string{"secret-bearer-token": "user"})
	r := NewGraphUserResolver(srv.URL, srv.Client())
	if _, err := r.UserID(context.Background(), Identity{
		Token: "secret-bearer-token", Expiry: time.Now().Add(time.Minute),
	}); err != nil {
		t.Fatal(err)
	}
	for k, v := range r.cache {
		if strings.Contains(string(k[:]), "secret-bearer-token") || strings.Contains(v.userID, "secret") {
			t.Fatal("the cache holds the bearer token")
		}
	}
}

func TestGraphUserResolver_CacheIsBounded(t *testing.T) {
	ids := map[string]string{}
	for i := range maxCachedUsers + 10 {
		ids["tok-"+itoa(i)] = "user-" + itoa(i)
	}
	srv, _, _ := fakeGraphMe(t, ids)
	r := NewGraphUserResolver(srv.URL, srv.Client())
	exp := time.Now().Add(time.Hour)
	for tok := range ids {
		if _, err := r.UserID(context.Background(), Identity{Token: tok, Expiry: exp}); err != nil {
			t.Fatal(err)
		}
	}
	if len(r.cache) > maxCachedUsers {
		t.Fatalf("cache holds %d entries, bound is %d", len(r.cache), maxCachedUsers)
	}
}

func TestGraphUserResolver_Errors(t *testing.T) {
	srv, _, _ := fakeGraphMe(t, map[string]string{"no-id": ""})
	r := NewGraphUserResolver(srv.URL, srv.Client())
	exp := time.Now().Add(time.Minute)

	if _, err := r.UserID(context.Background(), Identity{Token: "unknown", Expiry: exp}); err == nil {
		t.Fatal("a refused token must be an error, not a user")
	}
	if _, err := r.UserID(context.Background(), Identity{Token: "no-id", Expiry: exp}); !errors.Is(err, errNoUserID) {
		t.Fatalf("an answer without an id = %v, want errNoUserID", err)
	}
	if _, err := r.UserID(context.Background(), Identity{Expiry: exp}); err == nil {
		t.Fatal("a caller without a token must be an error")
	}
}

// subjectOnlyValidator stands in for the real OIDC validator: it knows the
// token's subject and nothing about the OpenCloud user id.
type subjectOnlyValidator struct{}

func (subjectOnlyValidator) Validate(_ context.Context, raw string) (Identity, error) {
	if raw == "" {
		return Identity{}, errInvalidToken
	}
	return Identity{Subject: "opaque:" + raw, Token: raw, Expiry: time.Now().Add(time.Minute)}, nil
}

func authenticatedUserID(t *testing.T, srv *Server) (int, string) {
	t.Helper()
	var seen string
	h := srv.Authenticate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, _ := IdentityFrom(r.Context())
		seen = id.UserID
	}))
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(rec, req)
	return rec.Code, seen
}

func TestAuthenticate_ResolvesTheUserID(t *testing.T) {
	srv := NewServer(
		WithTokenValidator(subjectOnlyValidator{}),
		WithUserResolver(UserResolverFunc(func(_ context.Context, id Identity) (string, error) {
			if id.Token != "tok" {
				t.Errorf("resolver got token %q", id.Token)
			}
			return "graph-id", nil
		})),
	)
	code, seen := authenticatedUserID(t, srv)
	if code != http.StatusOK || seen != "graph-id" {
		t.Fatalf("code=%d userID=%q, want 200 and the resolved id", code, seen)
	}
}

// Falling back to the subject would deny every member of every Space and
// match no user grant, silently. The request stops instead.
func TestAuthenticate_RefusesWhenTheUserIDCannotBeResolved(t *testing.T) {
	failing := UserResolverFunc(func(context.Context, Identity) (string, error) {
		return "", errors.New("graph down")
	})
	empty := UserResolverFunc(func(context.Context, Identity) (string, error) { return "", nil })

	for name, srv := range map[string]*Server{
		"no resolver":    NewServer(WithTokenValidator(subjectOnlyValidator{})),
		"resolver fails": NewServer(WithTokenValidator(subjectOnlyValidator{}), WithUserResolver(failing)),
		"empty answer":   NewServer(WithTokenValidator(subjectOnlyValidator{}), WithUserResolver(empty)),
	} {
		code, seen := authenticatedUserID(t, srv)
		if code != http.StatusServiceUnavailable || seen != "" {
			t.Errorf("%s: code=%d userID=%q, want 503 and no handler call", name, code, seen)
		}
	}
}

func TestAuthenticate_KeepsAUserIDTheValidatorSupplied(t *testing.T) {
	srv := NewServer(
		WithTokenValidator(fakeValidator{tokens: map[string]string{"tok": "known-id"}}),
		WithUserResolver(UserResolverFunc(func(context.Context, Identity) (string, error) {
			t.Error("the resolver must not be asked when the id is already known")
			return "", nil
		})),
	)
	if code, seen := authenticatedUserID(t, srv); code != http.StatusOK || seen != "known-id" {
		t.Fatalf("code=%d userID=%q", code, seen)
	}
}

func itoa(i int) string {
	b, _ := json.Marshal(i)
	return string(b)
}
