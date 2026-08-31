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

   **Pinned contract (implemented in Phase 3, consumed by Phase 8):**

   The browser performs, in this order:

   1. `rk = GenerateRecoveryKey()` → 160 bits of entropy, rendered as
      `ocbk1-XXXXX-…` (Crockford base32 + checksum; see
      [key-envelope-format.md](key-envelope-format.md) §4).
   2. `dk = random(32)` via WebCrypto.
   3. `wrappedRK = seal(dk, KEK=Argon2id(rkEntropy, salt, params), kind=RK)`
      producing a v1 envelope (§2 of the format doc; Argon2id via wasm).
   4. `POST /api/v1/spaces/{id}/backup/setup` with
      `{"wrapped_dk_rk": base64(envelope), "data_key": base64(dk)}`.
   5. Display the RK **once**, with a copy button and a confirmation gate
      (re-enter part of it) before step 4 is allowed to have "succeeded" in the
      UI. Then discard `dk` and `rk` from browser memory.

   The server adds the SRW wrap and persists both envelopes. It returns only
   key-status metadata.

   **Why the DK is sent but the RK is not:** unattended scheduled runs require
   the server to reconstruct the DK (decisions.md #1 explicitly accepts this —
   SRW is not pure zero-knowledge). The **Recovery Key** is what stays
   exclusively with the user, which is what preserves the admin-blind recovery
   property (decisions.md #2/#15).

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

## Outcome

**Implemented.** Format specification:
[key-envelope-format.md](key-envelope-format.md) — the binding compatibility
document for the Phase-5 decrypt CLI.

Decisions taken while implementing (previously left open in this doc):

- **RK encoding: Crockford base32 + checksum**, `ocbk1-` prefixed, 160 bits of
  entropy in 7 groups of 5 characters. Chosen over a BIP39-style word list
  because it needs no 2048-word list shipped to both the browser and the CLI, is
  case-insensitive, excludes ambiguous characters, and is trivial to generate
  with WebCrypto alone.
- **AEAD: XChaCha20-Poly1305** — its 24-byte nonce makes random nonces safe with
  no counter state to persist.
- **The envelope primitive is shared:** target credentials (decisions.md #14)
  reuse the identical envelope with `kind = TW`, so there is exactly one
  crypto path to audit (`pkg/keys/credsealer.go` + a `pkg/targets` adapter).
- **Argon2id parameters are range-checked before derivation.** Because the
  envelope carries its own costs, an unbounded value would let a crafted blob
  burn CPU/RAM before authentication could fail. Bounds: time ≤ 16, memory ≤ 1
  GiB, lanes ≤ 16. (Found by a test that hung — see the format doc §2.)

## Risks / notes

- **The envelope format is a long-term compatibility promise** — the standalone
  decrypt CLI must parse it forever. Version it from day one.
- Browser-side generation needs WebCrypto + Argon2 (wasm) in Phase 8; contract
  fixed here, implementation there.
