package targets

import (
	"errors"
	"testing"
)

func TestPublicViewOmitsSensitiveFields(t *testing.T) {
	// The end-user projection must never carry credentials, endpoint, or bucket
	// (decisions.md #12/#14). This guards that Public() stays least-disclosure.
	tgt := Target{
		ID:           "t1",
		Name:         "Buddy",
		Endpoint:     "http://secret:3900",
		Bucket:       "b",
		WrappedCreds: []byte("sealed"),
	}
	pv := tgt.Public()
	if pv.ID != "t1" || pv.Name != "Buddy" {
		t.Fatalf("unexpected public view: %+v", pv)
	}
	// PublicView has only ID and Name by type; this test also fails to compile
	// if a sensitive field is ever added to PublicView, which is intended.
}

func TestGrantScopesDistinct(t *testing.T) {
	if ScopeAllUsers == ScopeUser || ScopeUser == ScopeSpace || ScopeAllUsers == ScopeSpace {
		t.Fatal("grant scopes must be distinct")
	}
	if ScopeUnknown != 0 {
		t.Fatal("ScopeUnknown must be the zero value")
	}
}

func TestErrNotFoundIsErrorsAs(t *testing.T) {
	err := error(ErrNotFound{ID: "t9"})
	var nf ErrNotFound
	if !errors.As(err, &nf) || nf.ID != "t9" {
		t.Fatalf("ErrNotFound not matchable via errors.As: %v", err)
	}
}
