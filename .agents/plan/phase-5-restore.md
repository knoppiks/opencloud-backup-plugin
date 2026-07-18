# Phase 5 — Restore (Path A first — acceptance-critical)

**Goal:** both restore paths from decisions.md. Path A is the acceptance-critical
disaster-recovery path: OpenCloud fully down, only S3 + Recovery Key.

**Depends on:** Phase 4 (snapshots exist), Phase 3 (envelope format).

## Path A — Admin Take-Out + user-side decrypt

### `cmd/takeout` (admin/operator CLI)

- Input: S3 endpoint+credentials, space id, optional snapshot selector
  (default: latest).
- Output: a **self-contained Take-Out** — either
  - (a) a directory/archive mirroring the kopia repo objects for that space
    **plus** the RK-wrapped-DK envelope and a manifest, or
  - (b) simply the full `spaces/<space-id>/` prefix synced out + envelope.
- **Ciphertext only.** The tool has no key input at all — it *cannot* decrypt.
- No OpenCloud dependency; only S3 access.
- Decide in implementation: single-snapshot filtering vs. full-repo copy
  (full-repo copy is simpler and always correct; size is the trade-off —
  start with full-repo copy, optimize later).

### `cmd/decrypt` (user-side standalone CLI)

- Input: Take-Out blob/dir + **Recovery Key** (prompt, never CLI arg — shell
  history), output dir, optional snapshot selector.
- Flow: parse envelope → Argon2id(RK) → unwrap DK → open kopia repo (local
  filesystem storage pointing into the Take-Out) → restore snapshot to disk.
- Zero network. Zero OpenCloud. Must run on a normal user machine
  (linux/mac/windows builds).
- Clear errors: wrong RK ("key does not match"), corrupt blob, unknown
  envelope version.

## Path B — User restore into OpenCloud

- `POST /api/v1/spaces/{id}/restore` `{snapshot_id}` — auth: space member
  (Phase 2 membership check). Admin role explicitly rejected unless also a
  member.
- Flow: unwrap DK via SRW → kopia restore stream → **write into the space** via
  CS3 upload (TUS/data provider — inverse of the Phase 4 read path) under
  `Restore/<snapshot-ts>/`. Never overwrite live data.
- `GET /api/v1/spaces/{id}/snapshots` — list snapshots (ts, size, file count)
  for the picker UI.
- Runs as a job (job store, progress) like a backup run.

## Testing

- **Path A acceptance test (CI, scripted):**
  1. Phase-4 pipeline backs up seeded space to Garage.
  2. **Stop the oCIS containers** (OpenCloud is down).
  3. `takeout` extracts from Garage.
  4. `decrypt` with the test RK restores to a temp dir.
  5. Diff against original seed: byte-identical, mtimes preserved.
- Wrong-RK test: decrypt fails cleanly, no partial plaintext output left.
- `takeout` never touches key material: audit + test (no RK/DK flags exist).
- Path B integration: restore lands in `Restore/<ts>/`, live files untouched;
  non-member 403; admin-nonmember 403.
- Round-trip property: backup → restore → backup produces ~no new chunks
  (dedup proves fidelity).

## Exit criteria

- [ ] Path A acceptance test green **with OpenCloud stopped** (success
      metric 3).
- [ ] `decrypt` binaries build for linux/darwin/windows.
- [ ] Path B restore green incl. authorization negatives (success metric 4).
- [ ] Envelope-version compatibility test (v1 blob restores with current CLI).

## Risks / notes

- `decrypt` re-uses `/pkg/keys` + kopia-open-from-local-dir; keep it thin so
  it stays maintainable **forever** (it is the family's last resort).
- CS3 *upload* path (Path B) is new (Phase 4 only reads) — small spike inside
  this phase for TUS upload via data provider.
- Document the operator runbook: how the admin runs `takeout`, how the user
  runs `decrypt` (plain-language README section — the audience is "family").
