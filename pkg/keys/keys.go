// Package keys defines the key service: the Data Key (DK), Recovery Key (RK),
// and Server Runtime Wrap (SRW) model from decisions.md ("Trust & key model").
//
// Hard rules (decisions.md, AGENTS.md):
//   - Never log key material (DK, RK, SRW blobs) or expose it in the admin UI.
//   - The plaintext RK is generated/used client-side only and never crosses the
//     network.
//   - The envelope format is a versioned, long-term compatibility promise; the
//     standalone decrypt CLI must parse it forever.
//
// No cryptography is implemented here (Phase 3). This file defines the boundary
// interfaces and the versioned envelope shape only. We do not hand-roll crypto;
// Phase 3 uses maintained libraries for the key-envelope layer.
package keys

// EnvelopeVersion is the current key-envelope format version. It is part of a
// long-term compatibility promise (decisions.md): bump only additively and keep
// the decrypt CLI able to parse every historical version.
const EnvelopeVersion = 1

// WrappedDK is a Data Key wrapped for storage. The plaintext DK never appears
// in a persisted WrappedDK. Which KEK wrapped it is recorded by Kind.
type WrappedDK struct {
	// Version is the envelope format version (see EnvelopeVersion).
	Version int
	// Kind distinguishes an RK-wrapped envelope from an SRW-wrapped one.
	Kind WrapKind
	// Blob is the opaque wrapped key material. Never logged.
	Blob []byte
}

// WrapKind records which key wrapped a DK.
type WrapKind int

const (
	// WrapUnknown is the zero value and must not be persisted.
	WrapUnknown WrapKind = iota
	// WrapRK is a DK wrapped by the user's Recovery Key (client-side).
	WrapRK
	// WrapSRW is a DK wrapped by the Server Runtime Wrap key (cluster secret/KMS).
	WrapSRW
)

// Store persists and retrieves wrapped Data Keys per Space. It never stores or
// returns plaintext key material.
type Store interface {
	// GetSRW returns the SRW-wrapped DK for a space, or ErrNotFound.
	GetSRW(spaceID string) (WrappedDK, error)
	// PutSRW stores the SRW-wrapped DK for a space.
	PutSRW(spaceID string, w WrappedDK) error
	// GetRK returns the RK-wrapped DK for a space (for Take-Out export), or
	// ErrNotFound.
	GetRK(spaceID string) (WrappedDK, error)
	// PutRK stores the RK-wrapped DK for a space.
	PutRK(spaceID string, w WrappedDK) error
}

// Wrapper unwraps a Data Key for use inside a single run. The plaintext DK it
// returns lives only in memory and must never be logged or persisted.
type Wrapper interface {
	// UnwrapSRW recovers the plaintext DK from an SRW envelope using the
	// server-held SRW key. Used by the unattended worker.
	UnwrapSRW(w WrappedDK) (dk []byte, err error)
}

// ErrNotFound is returned by Store when no envelope exists for a space.
type ErrNotFound struct{ SpaceID string }

func (e ErrNotFound) Error() string { return "keys: no envelope for space " + e.SpaceID }
