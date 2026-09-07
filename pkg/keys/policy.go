package keys

// What the service accepts from a client, as opposed to what the format allows.
//
// envelope.go range-checks Argon2id costs to stop a crafted header from turning
// an unwrap attempt into a resource-exhaustion attack. Those are ceilings: they
// say nothing about whether an envelope is worth the protection it claims.
//
// This file is the other half — a floor. An envelope arriving over the API was
// produced by a browser we do not control, and it is the user's only recovery
// path: it will sit in a state Space, be published to an S3 target, and be
// handed to whoever ends up holding a Take-Out. If the Recovery Key protecting
// it was stretched with parameters cheap enough to brute-force, none of the rest
// of the design matters. The server cannot see the Recovery Key, so the work
// factor is the only thing about the client's ceremony it can actually check.

import (
	"errors"
	"fmt"
)

// MinArgonParams is the weakest Argon2id work factor the service accepts on a
// client-produced Recovery Key envelope.
//
// 19 MiB / 2 passes / 1 lane is OWASP's low-memory Argon2id baseline. It is
// deliberately below DefaultArgonParams: the floor exists to reject envelopes
// that are trivially crackable, not to force every client onto the server's
// preferred costs. A device too slow for the default may legitimately tune down;
// one that tunes below this is producing an envelope not worth storing.
//
// Raising this is a compatibility decision, not a free one: envelopes already
// stored keep unwrapping (the floor is only checked on the way in), but a client
// that was accepted yesterday can be refused tomorrow.
var MinArgonParams = ArgonParams{
	Time:      2,
	MemoryKiB: 19 * 1024,
	Lanes:     1,
	SaltLen:   16,
}

// ErrWeakEnvelope indicates an envelope whose key derivation is below
// MinArgonParams. It is distinct from ErrBadEnvelope because the two need
// different answers: a malformed blob is a bug, a weak one is a client that must
// redo the ceremony with stronger parameters.
var ErrWeakEnvelope = errors.New("keys: recovery envelope key derivation is below the accepted minimum")

// CheckRecoveryEnvelope validates a client-produced RK envelope against service
// policy and returns its metadata. It performs no decryption and needs no key.
//
// The checks, in the order a caller cares about them:
//   - the blob parses as an envelope of this format;
//   - it is an RK wrap, not an SRW or TW one — storing anything else as a
//     Space's recovery envelope would strand the user's only way back in;
//   - it is Argon2id-derived, because an RK is a human-transportable secret and
//     a direct-key envelope would mean the client used something else entirely;
//   - its work factor meets MinArgonParams.
//
// What it deliberately cannot check is whether the envelope actually wraps the
// Data Key the caller claims: that would need the Recovery Key, which never
// reaches the server. See the client invariant in key-envelope-format.md §5.
func CheckRecoveryEnvelope(blob []byte) (EnvelopeInfo, error) {
	info, err := Inspect(blob)
	if err != nil {
		return EnvelopeInfo{}, err
	}
	if info.Kind != WrapRK {
		return EnvelopeInfo{}, fmt.Errorf("%w: not a recovery envelope", ErrBadEnvelope)
	}
	if info.KDF != "argon2id" {
		return EnvelopeInfo{}, fmt.Errorf("%w: recovery envelope must be key-derived", ErrBadEnvelope)
	}
	if err := checkArgonFloor(info.Argon); err != nil {
		return EnvelopeInfo{}, err
	}
	return info, nil
}

// checkArgonFloor reports whether costs meet MinArgonParams. Every dimension is
// checked independently: trading memory for passes is not a trade this floor
// accepts, because the cheap direction is the one an attacker exploits.
func checkArgonFloor(p ArgonParams) error {
	switch {
	case p.Time < MinArgonParams.Time:
		return fmt.Errorf("%w: too few passes", ErrWeakEnvelope)
	case p.MemoryKiB < MinArgonParams.MemoryKiB:
		return fmt.Errorf("%w: too little memory", ErrWeakEnvelope)
	case p.Lanes < MinArgonParams.Lanes:
		return fmt.Errorf("%w: too little parallelism", ErrWeakEnvelope)
	case p.SaltLen < MinArgonParams.SaltLen:
		return fmt.Errorf("%w: salt too short", ErrWeakEnvelope)
	}
	return nil
}
