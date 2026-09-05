// Backup key setup endpoints (phase-3 deliverable 2 & 3).
//
// Trust invariants enforced here (decisions.md, AGENTS.md):
//   - The plaintext Recovery Key NEVER crosses the network. The browser
//     generates the RK, generates the DK, wraps the DK under the RK, and sends
//     only the RK-wrapped envelope plus the DK sealed for the server.
//   - The server adds the SRW wrap so unattended runs are possible
//     (decisions.md #1) and persists both envelopes. It stores no plaintext.
//   - Key material never appears in a response, an error message, or a log line.
//     Responses carry status metadata only.
//   - Every space-scoped route checks CS3 membership server-side; a non-member
//     gets 403 and learns nothing about the space.
package api

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"

	"opencloud-backup-plugin/pkg/keys"
)

// maxKeyRequestBytes caps setup payloads; envelopes are a few hundred bytes.
const maxKeyRequestBytes = 16 << 10

// setupRequest is the client's contribution to the key ceremony.
//
// The browser has already: generated the RK, generated the DK, and wrapped the
// DK under the RK (Argon2id + AEAD, same envelope format as the server). It
// sends the RK-wrapped envelope for storage, and the raw DK **only** so the
// server can add the SRW wrap that unattended runs need.
//
// Sending the DK is what decisions.md #1 explicitly accepts ("SRW is not pure
// zero-knowledge"): the server must be able to reconstruct the DK for scheduled
// runs. The RK itself is never sent, so the user's recovery path stays
// exclusively theirs.
type setupRequest struct {
	// WrappedDKRK is the base64 RK-wrapped DK envelope, produced client-side.
	WrappedDKRK string `json:"wrapped_dk_rk"`
	// DataKey is the base64 raw 256-bit DK, used server-side only to produce the
	// SRW wrap. It is zeroized immediately after wrapping and never stored.
	DataKey string `json:"data_key"`
}

// keyStatusResponse is the key-material-free setup summary.
type keyStatusResponse struct {
	SpaceID    string `json:"space_id"`
	Configured bool   `json:"configured"`
	HasRK      bool   `json:"has_recovery_wrap"`
	HasSRW     bool   `json:"has_server_wrap"`
	RKVersion  int    `json:"recovery_wrap_version,omitempty"`
	SRWVersion int    `json:"server_wrap_version,omitempty"`
	CreatedAt  string `json:"created_at,omitempty"`
	UpdatedAt  string `json:"updated_at,omitempty"`
}

// recoveryBlobResponse carries the RK-wrapped DK envelope to a space member
// (phase-3 deliverable 3). This is ciphertext: without the user's Recovery Key
// it is useless, so serving it to members upholds decisions.md #7 (a shared
// space's RK is retrievable by any member) without weakening confidentiality.
type recoveryBlobResponse struct {
	SpaceID string `json:"space_id"`
	// Envelope is the base64 RK-wrapped DK blob. Ciphertext only.
	Envelope string `json:"envelope"`
	Version  int    `json:"version"`
	// KDF and Argon parameters let an offline tool derive the KEK. They are
	// public parameters, not secrets, and are also embedded in the envelope.
	KDF            string `json:"kdf"`
	ArgonTime      uint32 `json:"argon_time,omitempty"`
	ArgonMemoryKiB uint32 `json:"argon_memory_kib,omitempty"`
	ArgonLanes     uint8  `json:"argon_lanes,omitempty"`
}

// handleKeySetup completes the key ceremony for a space: it stores the
// client-produced RK envelope and adds the server's SRW wrap.
func (s *Server) handleKeySetup(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
		return
	}
	spaceID := r.PathValue("id")
	if spaceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing space id")
		return
	}
	if s.keyStore == nil || s.srw == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}
	if !s.assertMember(w, r, id.Subject, spaceID) {
		return
	}

	var req setupRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxKeyRequestBytes))
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_request", "malformed request body")
		return
	}

	rkBlob, err := base64.StdEncoding.DecodeString(req.WrappedDKRK)
	if err != nil || len(rkBlob) == 0 {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid recovery envelope encoding")
		return
	}
	dk, err := base64.StdEncoding.DecodeString(req.DataKey)
	if err != nil || len(dk) != keys.DKSize {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid data key")
		return
	}
	// The DK is transient server-side: wrap it, then wipe it.
	defer keys.Zeroize(dk)

	// Validate the client envelope is a well-formed RK wrap before storing it —
	// a malformed blob would strand the user's only recovery path.
	info, err := keys.Inspect(rkBlob)
	if err != nil || info.Kind != keys.WrapRK {
		writeError(w, http.StatusBadRequest, "bad_request", "invalid recovery envelope")
		return
	}

	srwWrapped, err := s.srw.WrapSRW(dk)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not complete key setup")
		return
	}

	if err := s.keyStore.PutRK(spaceID, keys.WrappedDK{
		Version: info.Version, Kind: keys.WrapRK, Blob: rkBlob,
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store recovery envelope")
		return
	}
	if err := s.keyStore.PutSRW(spaceID, srwWrapped); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not store server envelope")
		return
	}

	st, err := s.keyStore.Status(spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read key status")
		return
	}
	writeJSON(w, http.StatusCreated, toKeyStatusResponse(st))
}

// handleKeyStatus reports whether a space is set up. It returns no key material.
func (s *Server) handleKeyStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
		return
	}
	spaceID := r.PathValue("id")
	if spaceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing space id")
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}
	if !s.assertMember(w, r, id.Subject, spaceID) {
		return
	}

	st, err := s.keyStore.Status(spaceID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read key status")
		return
	}
	writeJSON(w, http.StatusOK, toKeyStatusResponse(st))
}

// handleRecoveryEnvelope serves the RK-wrapped DK blob to space members
// (decisions.md #7). The response is ciphertext plus public KDF parameters; the
// server neither holds nor learns the Recovery Key.
func (s *Server) handleRecoveryEnvelope(w http.ResponseWriter, r *http.Request) {
	id, ok := IdentityFrom(r.Context())
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthorized", "missing identity")
		return
	}
	spaceID := r.PathValue("id")
	if spaceID == "" {
		writeError(w, http.StatusBadRequest, "bad_request", "missing space id")
		return
	}
	if s.keyStore == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "key service not configured")
		return
	}
	if !s.assertMember(w, r, id.Subject, spaceID) {
		return
	}

	wrapped, err := s.keyStore.GetRK(spaceID)
	if err != nil {
		var nf keys.ErrNotFound
		if errors.As(err, &nf) {
			writeError(w, http.StatusNotFound, "not_found", "backup not configured for this space")
			return
		}
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read recovery envelope")
		return
	}
	info, err := keys.Inspect(wrapped.Blob)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", "could not read recovery envelope")
		return
	}

	writeJSON(w, http.StatusOK, recoveryBlobResponse{
		SpaceID:        spaceID,
		Envelope:       base64.StdEncoding.EncodeToString(wrapped.Blob),
		Version:        info.Version,
		KDF:            info.KDF,
		ArgonTime:      info.Argon.Time,
		ArgonMemoryKiB: info.Argon.MemoryKiB,
		ArgonLanes:     info.Argon.Lanes,
	})
}

// assertMember enforces server-side CS3 membership for a space-scoped route. It
// writes the error response and returns false when access is denied, so a
// non-member cannot distinguish "not a member" from "no such space".
func (s *Server) assertMember(w http.ResponseWriter, r *http.Request, subject, spaceID string) bool {
	if s.spaces == nil {
		writeError(w, http.StatusServiceUnavailable, "unavailable", "space backend not configured")
		return false
	}
	all, err := s.spaces.ListSpaces(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, "upstream_error", "could not verify space membership")
		return false
	}
	for _, sp := range all {
		if sp.ID == spaceID {
			if isMember(sp, subject) {
				return true
			}
			break
		}
	}
	// Same response whether the space is foreign or absent — no enumeration.
	writeError(w, http.StatusForbidden, "forbidden", "not a member of this space")
	return false
}

func toKeyStatusResponse(st keys.Status) keyStatusResponse {
	out := keyStatusResponse{
		SpaceID:    st.SpaceID,
		Configured: st.Configured,
		HasRK:      st.HasRK,
		HasSRW:     st.HasSRW,
		RKVersion:  st.RKVersion,
		SRWVersion: st.SRWVersion,
	}
	if !st.CreatedAt.IsZero() {
		out.CreatedAt = st.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	if !st.UpdatedAt.IsZero() {
		out.UpdatedAt = st.UpdatedAt.UTC().Format("2006-01-02T15:04:05Z")
	}
	return out
}

// srwWrapper is the subset of the SRW key holder the API needs. It is satisfied
// by *keys.SRWWrapper; the interface keeps the server testable without holding
// real key material in tests.
type srwWrapper interface {
	WrapSRW(dk []byte) (keys.WrappedDK, error)
}

// compile-time guard that the concrete wrapper satisfies the API's needs.
var _ srwWrapper = (*keys.SRWWrapper)(nil)
