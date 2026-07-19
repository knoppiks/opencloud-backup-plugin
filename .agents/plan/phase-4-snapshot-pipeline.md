# Phase 4 — Snapshot Pipeline (backup)

**Goal:** the core: `[CS3 read] -> [kopia snapshot/encrypt/dedup] -> [Garage]`.
One kopia repo per Space, DK as repo password.

**Depends on:** Phase 0 Spike 2 (kopia API notes), Phase 2 (CS3 reader),
Phase 3 (keys).

## Design

### Feeding CS3 into kopia

Two options — decide early against Spike 2 notes:

- **Option A (staging):** stream the Space via CS3 into a temp dir, point
  kopia's local-FS source at it. Simple, correct mtimes; costs disk (needs
  space for the largest Space) and an extra copy.
- **Option B (virtual FS):** implement kopia's `fs.Entry`/`fs.Directory`
  interfaces backed directly by CS3 (stream on demand, no staging).
  No disk cost, true streaming — more code against kopia internals.

**Recommendation:** try B first (kopia's snapshot walker is designed around the
`fs` abstraction; spike it against Spike-2 notes); fall back to A if the
interface surface is unstable. Wrap either behind `snapshot.Source` so the
choice is swappable.

### Run flow (`/pkg/snapshot` + orchestration)

1. Resolve Space + check backup configured (keys exist).
1b. Resolve the Space's **target** and open its credentials in memory:
    `targets.Store.GetTarget` → `targets.CredSealer.Open` (TW-unwrap;
    decisions.md #12/#14). Plaintext credentials live only in memory for this run
    and are zeroized after use; never logged.
2. Unwrap DK via SRW (in memory only).
3. Connect/open kopia repo on the resolved target,
   `s3://<bucket>/<prefix>/spaces/<space-id>/` (create on first run; repo
   password = DK).
4. Snapshot the Space source (mtime + structure preserved; files only —
   decisions.md #4 scope).
5. Apply retention: **time-based `keep-within`** policy (configurable, deep
   default, e.g. 90d) — but **prune/maintenance does not run here** (separate
   job, Phase 6/7 — Tier 1 separation).
6. Record run result in job store (Phase 6 fleshes this out; minimal record
   now).
7. Zeroize DK.

### Consistency stance

oCIS spaces are live; we snapshot file-by-file (no global point-in-time fence).
A file changing mid-read is the same exposure any file-level backup has.
Document it; optionally re-stat + retry per file. Etag/mtime recorded at walk
time. Good enough for the family scenario — noted explicitly, not hidden.

## Deliverables

- `snapshot.Engine` implementation (kopia) with `Snapshot(ctx, space, dk)`.
- `snapshot.Source` (CS3-backed, Option A or B).
- Repo layout + naming convention documented (`spaces/<space-id>/`).
- Manual trigger endpoint for testing: `POST /api/v1/spaces/{id}/backup/run`
  (auth: space member; becomes the UI's "backup now").
- Config: bucket, prefix, retention window, parallelism, bandwidth cap
  (optional).

## Testing

- Unit: engine against fake `Source` + kopia on local-FS storage backend (fast,
  no Garage).
- Integration (Garage fixture): full run against compose oCIS space fixture;
  verify:
  - [ ] bucket contains only encrypted chunks/obfuscated paths (no filename or
        content substring findable),
  - [ ] second run after 1-file change uploads ~only the delta (dedup),
  - [ ] kopia restore (library call) round-trips byte-identical + mtime,
  - [ ] large file (> multipart threshold) round-trips,
  - [ ] concurrent runs for the same Space are prevented (lock in job store).
- Failure injection: Garage down mid-run → run marked failed, no corrupt repo
  (kopia handles atomicity; verify next run succeeds).

## Exit criteria

- [ ] End-to-end: seeded oCIS Space → encrypted snapshot on Garage → verified
      restore round-trip (library-level).
- [ ] Dedup + encryption assertions green in CI.
- [ ] Failed-run recovery test green.
- [ ] Success criterion 2 achieved (decisions.md).

## Risks

- Option B couples us to kopia's `fs` interfaces (not a stability-guaranteed
  API) — pin kopia version, wrap thinly.
- Very large spaces + staging (Option A) need disk sizing guidance in deploy
  docs.
