# Phase 3 — Key Setup & Envelope

**Goal:** the key service implementing the trust model from
[decisions.md](decisions.md): per-Space Data Key, user-held Recovery Key,
Server Runtime Wrap.

**Depends on:** Phase 1 (layout). Independent of CS3 — pure crypto/storage,
fully unit-testable.

## Key material & formats

- **DK (Data Key):** 256-bit random (crypto/rand). Doubles as the kopia repo
  password for that Space's repo. Exists in plaintext **only in memory** during
  a run.
- **RK (Recovery Key):** high-entropy human-transportable string, generated
  **client-side** (browser, Phase 8) — server never sees it in plaintext.
  Format: word-list or base32 with checksum (decide: BIP39-style 12 words vs.
  crockford-base32; must be typable from a password manager). Versioned prefix
  (`ocbk1-...`) for future format changes.
- **Wraps:** standard AEAD envelope — RK/SRW-derived KEK wraps DK.
  - RK → KEK via Argon2id (RK is human-transportable; parameters recorded in
    the envelope header).
  - SRW KEK: 256-bit random key from K8s secret / KMS; wraps DK directly.
  - AEAD: XChaCha20-Poly1305 or AES-256-GCM via a maintained library
    (e.g. filippo.io/age internals or x/crypto directly — **wrap format only**;
    data crypto stays kopia's).
- **Stored per Space:** `{space_id, dk_wrapped_rk, dk_wrapped_srw, argon_params,
  created_at, version}`. No plaintext, ever.

## Deliverables

1. `/pkg/keys`:
   - `GenerateDK()`, `WrapWithRK(dk, rk)`, `WrapWithSRW(dk, srwKey)`,
     `UnwrapRK(...)`, `UnwrapSRW(...)`, `Rotate(...)` (re-wrap DK under new
     RK/SRW without touching snapshot data).
   - `keys.Store` interface + implementation (file/DB per Phase 1 decision).
   - Zeroize DK buffers after use where Go allows; never log key material
     (lint rule / review checklist).
2. **API endpoints:**
   - `POST /api/v1/spaces/{id}/backup/setup` — browser sends the **RK-wrapped
     DK** (client did: generate RK, generate-or-receive DK, wrap). Server adds
     SRW wrap, persists both. Exact split of client/server duties to be finessed
     with Phase 8; invariant: **plaintext RK never crosses the wire.**
   - `GET /api/v1/spaces/{id}/backup/keystatus` — configured? created when?
     which wrap versions? (No key material in response.)
3. **Shared-space RK retrieval** (decisions.md #7): endpoint returning the
   RK-wrapped-DK blob to **space members only** (membership via CS3 from
   Phase 2). Members can re-wrap for themselves; server still never sees RK
   plaintext.
4. **Recovery-Key ceremony contract** (for Phase 8): RK displayed once,
   "I saved it" confirmation gate, no server-side RK escrow.

## Testing

- Round-trip: wrap/unwrap under RK and SRW; tampered ciphertext fails; wrong
  RK fails cleanly.
- Rotation: new RK / new SRW key; old wraps invalidated; DK unchanged.
- Argon2id parameter versioning: old envelopes still unwrap after a parameter
  bump.
- Property test: no two generated DKs/RKs collide; encodings round-trip.
- Negative API tests: non-member cannot fetch shared-space blob; keystatus
  leaks no material.

## Exit criteria

- [ ] Full wrap/unwrap/rotate round-trip tests green (`-race`).
- [ ] Setup + keystatus + member-retrieval endpoints implemented and tested.
- [ ] Grep-audit: no key material in logs or error messages.
- [ ] Envelope format documented (self-contained enough for the Phase 5
      standalone decrypt CLI).

## Risks / notes

- **The envelope format is a long-term compatibility promise** — the standalone
  decrypt CLI must parse it forever. Version it from day one.
- Browser-side generation needs WebCrypto + Argon2 (wasm) in Phase 8; contract
  fixed here, implementation there.
