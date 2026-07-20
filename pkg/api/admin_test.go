package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// fakeValidator is a TokenValidator that maps a raw token straight to a subject,
// so admin/handler tests do not need real JWT signing.
type fakeValidator struct {
	// tokens maps raw token -> subject. Unknown tokens are rejected.
	tokens map[string]string
}

func (f fakeValidator) Validate(_ context.Context, raw string) (Identity, error) {
	sub, ok := f.tokens[raw]
	if !ok {
		return Identity{}, errInvalidToken
	}
	return Identity{Subject: sub, Username: sub, Token: raw}, nil
}

func TestAllowlistAdminResolver(t *testing.T) {
	r := NewAllowlistAdminResolver([]string{"admin-sub", " spaced ", ""})
	if ok, _ := r.IsAdmin(context.Background(), Identity{Subject: "admin-sub"}); !ok {
		t.Fatal("admin-sub should be admin")
	}
	if ok, _ := r.IsAdmin(context.Background(), Identity{Subject: "spaced"}); !ok {
		t.Fatal("trimmed subject should be admin")
	}
	if ok, _ := r.IsAdmin(context.Background(), Identity{Subject: "user-sub"}); ok {
		t.Fatal("user-sub must not be admin")
	}
}

func TestGraphAdminResolver(t *testing.T) {
	// Fake graph /me endpoint returning role assignments based on bearer token.
	mux := http.NewServeMux()
	mux.HandleFunc("/graph/v1.0/me", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		switch auth {
		case "Bearer admin-token":
			_, _ = w.Write([]byte(`{"appRoleAssignments":[{"appRoleId":"` + DefaultAdminAppRoleID + `"}]}`))
		case "Bearer user-token":
			_, _ = w.Write([]byte(`{"appRoleAssignments":[{"appRoleId":"d7beeea8-8ff4-406b-8fb6-ab2dd81e6b11"}]}`))
		default:
			w.WriteHeader(http.StatusUnauthorized)
		}
	})
	ts := httptest.NewServer(mux)
	defer ts.Close()

	r := NewGraphAdminResolver(ts.URL, "", ts.Client())

	if ok, err := r.IsAdmin(context.Background(), Identity{Token: "admin-token"}); err != nil || !ok {
		t.Fatalf("admin-token should resolve admin: ok=%v err=%v", ok, err)
	}
	if ok, err := r.IsAdmin(context.Background(), Identity{Token: "user-token"}); err != nil || ok {
		t.Fatalf("user-token must not be admin: ok=%v err=%v", ok, err)
	}
	if _, err := r.IsAdmin(context.Background(), Identity{Token: "bogus"}); err == nil {
		t.Fatal("graph 401 should surface as error (fail closed)")
	}
	if _, err := r.IsAdmin(context.Background(), Identity{}); err == nil {
		t.Fatal("missing token should error")
	}
}

func TestAdminGate(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{
		"admin-token": "admin-sub",
		"user-token":  "user-sub",
	}}
	resolver := AdminResolverFunc(func(_ context.Context, id Identity) (bool, error) {
		return id.Subject == "admin-sub", nil
	})
	srv := NewServer(WithTokenValidator(val), WithAdminResolver(resolver))

	cases := []struct {
		name  string
		token string
		want  int
	}{
		{"no token", "", http.StatusUnauthorized},
		{"non-admin", "user-token", http.StatusForbidden},
		{"admin passes gate", "admin-token", http.StatusNotFound}, // gate passed; body is not-implemented
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/targets", nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			srv.Handler().ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s: want %d, got %d (body=%s)", tc.name, tc.want, rec.Code, rec.Body.String())
			}
		})
	}
}

func TestResolveAdminFailsClosedOnError(t *testing.T) {
	val := fakeValidator{tokens: map[string]string{"tok": "sub"}}
	resolver := AdminResolverFunc(func(_ context.Context, _ Identity) (bool, error) {
		return true, context.DeadlineExceeded // error must not grant admin
	})
	srv := NewServer(WithTokenValidator(val), WithAdminResolver(resolver))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/targets", nil)
	req.Header.Set("Authorization", "Bearer tok")
	srv.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("resolver error must fail closed to 403, got %d", rec.Code)
	}
}
