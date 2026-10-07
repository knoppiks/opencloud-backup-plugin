package cs3

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	rpc "github.com/cs3org/go-cs3apis/cs3/rpc/v1beta1"
)

// countingAuth mints a distinct token per call and counts the calls.
type countingAuth struct {
	calls atomic.Int64
	// ttl is how long the minted tokens claim to be valid; zero mints a token
	// that carries no expiry at all.
	ttl time.Duration
	now time.Time
	err error
}

func (a *countingAuth) Token(context.Context) (string, error) {
	n := a.calls.Add(1)
	if a.err != nil {
		return "", a.err
	}
	if a.ttl == 0 {
		return fmt.Sprintf("opaque-token-%d", n), nil
	}
	return jwtWithExpiry(a.now.Add(a.ttl)), nil
}

// jwtWithExpiry builds an unsigned token carrying only an exp claim. Nothing in
// this package verifies a token, so a signature would prove nothing.
func jwtWithExpiry(exp time.Time) string {
	payload, _ := json.Marshal(map[string]int64{"exp": exp.Unix()})
	return "header." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}

func TestCachedAuth_ReusesATokenUntilItNearlyExpires(t *testing.T) {
	now := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	inner := &countingAuth{ttl: 10 * time.Minute, now: now}
	auth := NewCachedAuth(inner)
	auth.now = func() time.Time { return now }

	first, err := auth.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	for i := 0; i < 100; i++ {
		again, err := auth.Token(context.Background())
		if err != nil || again != first {
			t.Fatalf("token changed while still valid: %q vs %q (%v)", again, first, err)
		}
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("minted %d tokens, want 1", got)
	}

	// Inside the refresh margin the token is replaced, so no call ever leaves
	// with one that expires in flight.
	now = now.Add(10*time.Minute - tokenRefreshMargin)
	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("minted %d tokens, want 2", got)
	}
}

// A token whose expiry cannot be read is still worth reusing briefly, but only
// briefly: the alternative is guessing how long it lives.
func TestCachedAuth_FallsBackToAShortWindow(t *testing.T) {
	now := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	inner := &countingAuth{}
	auth := NewCachedAuth(inner)
	auth.now = func() time.Time { return now }

	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := inner.calls.Load(); got != 1 {
		t.Fatalf("minted %d tokens, want the opaque one reused", got)
	}

	now = now.Add(tokenFallbackTTL)
	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("minted %d tokens, want the fallback window to have expired", got)
	}
}

func TestCachedAuth_InvalidateDropsTheToken(t *testing.T) {
	now := time.Date(2026, 9, 8, 2, 0, 0, 0, time.UTC)
	inner := &countingAuth{ttl: time.Hour, now: now}
	auth := NewCachedAuth(inner)
	auth.now = func() time.Time { return now }

	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	auth.Invalidate()
	if _, err := auth.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("minted %d tokens, want the invalidated one replaced", got)
	}
}

func TestCachedAuth_PropagatesFailures(t *testing.T) {
	auth := NewCachedAuth(&countingAuth{err: errors.New("gateway down")})
	if _, err := auth.Token(context.Background()); err == nil {
		t.Fatal("a failure to mint must not be cached as success")
	}
}

// A token reva has rejected must not be presented again until it happens to
// expire; without this the cache would turn one bad token into an outage.
func TestClient_DropsACachedTokenRevaRejected(t *testing.T) {
	now := time.Now()
	inner := &countingAuth{ttl: time.Hour, now: now}
	auth := NewCachedAuth(inner)

	fg := &fakeGateway{spaceStatus: rpc.Code_CODE_UNAUTHENTICATED}
	c := NewClient(fg, auth)

	if _, err := c.ListSpaces(context.Background()); err == nil {
		t.Fatal("an unauthenticated status must error")
	}
	if _, err := c.ListSpaces(context.Background()); err == nil {
		t.Fatal("an unauthenticated status must error")
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("minted %d tokens, want the rejected one dropped each time", got)
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Unix(1_800_000_000, 0).UTC()
	if got, ok := tokenExpiry(jwtWithExpiry(exp)); !ok || !got.Equal(exp) {
		t.Fatalf("tokenExpiry = %v, %v; want %v", got, ok, exp)
	}

	for name, token := range map[string]string{
		"not a jwt":     "plain-opaque-token",
		"bad base64":    "header.!!!.signature",
		"not json":      "header." + base64.RawURLEncoding.EncodeToString([]byte("nope")) + ".sig",
		"no exp":        "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"sub":"x"}`)) + ".sig",
		"negative exp":  "header." + base64.RawURLEncoding.EncodeToString([]byte(`{"exp":-1}`)) + ".sig",
		"missing parts": "header.payload",
	} {
		if _, ok := tokenExpiry(token); ok {
			t.Fatalf("%s: expected no expiry", name)
		}
	}
}

// The write path drops a rejected token too: a restore that starts failing must
// not keep failing because of a token nobody re-minted.
func TestClient_DropsACachedTokenOnAnUnauthenticatedWrite(t *testing.T) {
	inner := &countingAuth{ttl: time.Hour, now: time.Now()}
	auth := NewCachedAuth(inner)
	fg := &fakeGateway{createStatus: rpc.Code_CODE_UNAUTHENTICATED}
	c := NewClient(fg, auth)

	space := Space{ID: "s1", Root: ResourceID{StorageID: "st", SpaceID: "sp", OpaqueID: "root"}}
	if err := c.MakeDir(context.Background(), space, "Restore"); err == nil {
		t.Fatal("an unauthenticated status must error")
	}
	if err := c.MakeDir(context.Background(), space, "Restore"); err == nil {
		t.Fatal("an unauthenticated status must error")
	}
	if got := inner.calls.Load(); got != 2 {
		t.Fatalf("minted %d tokens, want the rejected one dropped each time", got)
	}
}
