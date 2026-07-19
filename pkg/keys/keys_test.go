package keys

import (
	"errors"
	"testing"
)

func TestEnvelopeVersionPinned(t *testing.T) {
	// The envelope version is a compatibility promise; a change here is a
	// deliberate format decision, not an accident.
	if EnvelopeVersion != 1 {
		t.Fatalf("EnvelopeVersion changed to %d; update decrypt CLI compatibility", EnvelopeVersion)
	}
}

func TestErrNotFoundIsErrorsAs(t *testing.T) {
	err := error(ErrNotFound{SpaceID: "s1"})
	var nf ErrNotFound
	if !errors.As(err, &nf) || nf.SpaceID != "s1" {
		t.Fatalf("ErrNotFound not matchable via errors.As: %v", err)
	}
}
