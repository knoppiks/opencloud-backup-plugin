package keys

import (
	"errors"
	"testing"
)

// floorArgon is exactly MinArgonParams: the cheapest envelope the service will
// take. Tests that need an *acceptable* envelope use it so they cost as little
// as policy allows.
var floorArgon = MinArgonParams

func TestCheckRecoveryEnvelopeAcceptsTheFloor(t *testing.T) {
	dk := mustDK(t)
	w, err := WrapWithRK(dk, []byte("recovery-key"), floorArgon)
	if err != nil {
		t.Fatalf("WrapWithRK: %v", err)
	}

	info, err := CheckRecoveryEnvelope(w.Blob)
	if err != nil {
		t.Fatalf("CheckRecoveryEnvelope: %v", err)
	}
	if info.Kind != WrapRK {
		t.Fatalf("kind = %v, want %v", info.Kind, WrapRK)
	}
	if info.Version != EnvelopeVersion {
		t.Fatalf("version = %d, want %d", info.Version, EnvelopeVersion)
	}
}

func TestCheckRecoveryEnvelopeRejectsWeakParameters(t *testing.T) {
	dk := mustDK(t)

	weaker := map[string]ArgonParams{
		"too few passes": {
			Time: MinArgonParams.Time - 1, MemoryKiB: MinArgonParams.MemoryKiB,
			Lanes: MinArgonParams.Lanes, SaltLen: MinArgonParams.SaltLen,
		},
		"too little memory": {
			Time: MinArgonParams.Time, MemoryKiB: MinArgonParams.MemoryKiB - 1,
			Lanes: MinArgonParams.Lanes, SaltLen: MinArgonParams.SaltLen,
		},
		"salt too short": {
			Time: MinArgonParams.Time, MemoryKiB: MinArgonParams.MemoryKiB,
			Lanes: MinArgonParams.Lanes, SaltLen: MinArgonParams.SaltLen - 1,
		},
	}
	for name, params := range weaker {
		t.Run(name, func(t *testing.T) {
			w, err := WrapWithRK(dk, []byte("recovery-key"), params)
			if err != nil {
				t.Fatalf("WrapWithRK: %v", err)
			}
			if _, err := CheckRecoveryEnvelope(w.Blob); !errors.Is(err, ErrWeakEnvelope) {
				t.Fatalf("err = %v, want ErrWeakEnvelope", err)
			}
		})
	}
}

func TestCheckRecoveryEnvelopeRejectsNonRecoveryEnvelopes(t *testing.T) {
	dk := mustDK(t)
	srwKey := mustSRWKey(t)

	// An SRW envelope is well-formed and unwrappable — by the server, with a key
	// the user does not have. Accepting one as a recovery envelope would leave
	// the user with no way back in at all.
	w, err := WrapWithSRW(dk, srwKey)
	if err != nil {
		t.Fatalf("WrapWithSRW: %v", err)
	}
	if _, err := CheckRecoveryEnvelope(w.Blob); !errors.Is(err, ErrBadEnvelope) {
		t.Fatalf("err = %v, want ErrBadEnvelope", err)
	}
}

func TestCheckRecoveryEnvelopeRejectsMalformedBlobs(t *testing.T) {
	for name, blob := range map[string][]byte{
		"empty":   {},
		"garbage": []byte("not an envelope at all"),
		"bad magic": append([]byte("XXXXX"),
			make([]byte, envelopeHeaderMin)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CheckRecoveryEnvelope(blob); err == nil {
				t.Fatal("accepted a malformed blob")
			}
		})
	}
}

func TestMinArgonParamsIsWithinTheFormatBounds(t *testing.T) {
	// The floor and the ceiling are set independently; a floor above the DoS
	// ceiling would reject every envelope, including the ones we produce.
	if err := validateArgonParams(MinArgonParams); err != nil {
		t.Fatalf("MinArgonParams is not a valid envelope parameter set: %v", err)
	}
	if err := checkArgonFloor(DefaultArgonParams); err != nil {
		t.Fatalf("DefaultArgonParams is below the floor: %v", err)
	}
}
