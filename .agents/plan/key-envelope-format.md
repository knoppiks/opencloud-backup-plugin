# Key Envelope Format v1 — Compatibility Specification

**Status: STABLE. This is a long-term compatibility promise.**

The standalone `decrypt` CLI (Phase 5) must be able to parse **every** version of
this format forever, using only the user's Recovery Key and the bytes of the
envelope. Nothing outside the blob is required to unwrap it — no database, no
server, no OpenCloud.

Implementation: `pkg/keys/envelope.go` (format), `pkg/keys/service.go`
(lifecycle), `pkg/keys/recoverykey.go` (RK encoding).

---

## 1. Cryptographic choices

| Concern | Choice | Rationale |
|---|---|---|
| AEAD | **XChaCha20-Poly1305** (`golang.org/x/crypto/chacha20poly1305`) | 24-byte nonce → random nonces are safe without counter state; no per-wrap bookkeeping. |
| KDF (Recovery Key) | **Argon2id** (`golang.org/x/crypto/argon2`) | Memory-hard stretching for a human-transportable secret. Parameters travel in the envelope. |
| KDF (SRW / TW keys) | **none** — key used directly | Those keys are already 256 bits of uniform randomness from a K8s secret / KMS; stretching adds nothing. |
| DK | 256-bit `crypto/rand` | Doubles as the kopia repo password (decisions.md #5/#6). |

No cryptography is hand-rolled (AGENTS.md hard constraint). Data-at-rest
encryption and dedup remain kopia's responsibility; this layer only wraps keys.

---

## 2. Byte layout

All integers big-endian. Offsets in bytes.

```
offset  size    field
0       5       magic          "OCBKE"  (OpenCloud BacKup Envelope)
5       1       version        format version (currently 1)
6       1       kind           1 = RK, 2 = SRW, 3 = TW
7       1       kdf            0 = none (direct 32-byte key), 1 = Argon2id
8       4       argonTime      Argon2id passes          (0 when kdf = none)
12      4       argonMemoryKiB Argon2id memory in KiB   (0 when kdf = none)
16      1       argonLanes     Argon2id parallelism     (0 when kdf = none)
17      1       saltLen        KDF salt length          (0 when kdf = none)
18      saltLen salt           KDF salt
18+s    24      nonce          XChaCha20-Poly1305 nonce
42+s    rest    ciphertext     AEAD output (plaintext + 16-byte tag)
```

`s` = `saltLen`. Minimum header size is 18 bytes.

### Additional authenticated data

The **entire header** — bytes `0 .. 18+saltLen`, i.e. everything up to but not
including the nonce — is passed to the AEAD as additional authenticated data.

Consequence: version, kind, and all KDF parameters are cryptographically bound
to the ciphertext. An attacker cannot downgrade the version, swap the wrap kind,
or weaken the Argon2id cost without the unwrap failing.

### Parameter bounds (denial-of-service protection)

Because the envelope carries its own KDF costs, a hostile blob could otherwise
demand unbounded CPU/RAM *before* authentication can fail. Parameters are
therefore range-checked **at parse time, before any derivation**:

| Parameter | Bound |
|---|---|
| `argonTime` | `1 .. 16` |
| `argonMemoryKiB` | `1 .. 1048576` (1 GiB) |
| `argonLanes` | `1 .. 16` |

Values outside these ranges are rejected as a malformed envelope. The ceilings
sit far above any legitimate setting, so `DefaultArgonParams` can be raised
without a format change.

### Current default Argon2id parameters

```
time = 3, memory = 65536 KiB (64 MiB), lanes = 4, saltLen = 16
```

Chosen so the same derivation is feasible in-browser (Phase 8 runs Argon2id in
WebAssembly). **Raising these is always safe**: new envelopes record the new
costs; old envelopes keep unwrapping with the costs stored inside them.

---

## 3. Unwrap procedure (what the decrypt CLI does)

1. Read the blob; verify `magic == "OCBKE"`.
2. Read `version`; refuse versions newer than the tool understands.
3. Read `kind`; for the offline recovery path require `kind == 1` (RK).
4. Read `kdf` and the Argon2id parameters; validate against the bounds above.
5. Read `salt` (`saltLen` bytes), then the 24-byte `nonce`; the remainder is
   ciphertext.
6. Decode the user's Recovery Key to its 20 raw entropy bytes (section 4).
7. Derive the KEK: `Argon2id(rkEntropy, salt, time, memoryKiB, lanes, 32)`.
8. `XChaCha20Poly1305(KEK).Open(nonce, ciphertext, aad = header)`.
9. The plaintext is the 32-byte Data Key = the kopia repository password.

A failure at step 8 means *either* a wrong Recovery Key *or* a tampered
envelope. The two are deliberately indistinguishable.

---

## 4. Recovery Key encoding

```
ocbk1-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX-XXXXX
```

- `ocbk1` — versioned prefix. A future incompatible format uses `ocbk2`.
- Payload — 160 bits of entropy + 8-bit checksum = 168 bits → **35 Crockford
  base32 characters**, in 7 groups of 5.
- **Crockford base32** alphabet `0123456789ABCDEFGHJKMNPQRSTVWXYZ` excludes
  `I`, `L`, `O`, `U` so the key survives handwriting and retyping.
- **Checksum** = first byte of `SHA-256(entropy)`. It catches typos *before* the
  expensive Argon2id derivation, letting the UI say "mistyped" instead of
  "wrong key". It is an integrity aid, not a security control.

Decoding is deliberately tolerant, because users retype these by hand:

- case-insensitive;
- dashes and whitespace ignored;
- Crockford look-alikes mapped: `I`, `L` → `1`; `O` → `0`;
- the `ocbk1` prefix is optional on input.

The wrapping secret is the **20 raw entropy bytes**, not the display string.

---

## 5. Trust invariants (binding)

- **The plaintext Recovery Key never crosses the network.** It is generated
  client-side, shown once, and stored by the user. There is no server-side RK
  escrow (decisions.md).
- **Server-held material:** the SRW-wrapped DK (so unattended runs work,
  decisions.md #1) and the RK-wrapped DK (ciphertext, for Take-Out). The server
  never stores a plaintext DK or RK.
- **The RK-wrapped blob is safe to hand to a space member** — it is ciphertext,
  useless without the RK. That is what makes shared-space recovery work
  (decisions.md #7).
- **Rotation re-wraps the same DK**, so snapshot data is never rewritten.
  Rotating the SRW key or the RK invalidates only the old envelope.
- **Key material is never logged**, never returned by an API, and never shown in
  the admin UI. Enforced by tests in `pkg/keys/noleak_test.go`.

---

## 6. Reuse for target credentials (TW)

Per decisions.md #14, S3 target credentials use this **same** envelope with
`kind = 3 (TW)` and `kdf = none` under the cluster/KMS Target-Wrap key. The
plaintext is a small JSON document:

```json
{"access_key_id":"...","secret_access_key":"..."}
```

Implementation: `pkg/keys/credsealer.go` + the `pkg/targets` adapter. Because
`kind` is authenticated, a DK envelope can never be opened as a credential blob
(or vice versa) even under the same key.

---

## 7. Versioning rules

- Bump `version` only for a change that a v1 parser cannot handle.
- **Never** reuse or repurpose a `kind` or `kdf` identifier.
- Every historical version must remain parseable by the decrypt CLI; add a new
  branch rather than replacing the old one.
- `EnvelopeVersion` is pinned by a test — changing it is a deliberate decision,
  not an accident.
