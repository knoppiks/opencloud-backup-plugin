# Phase 4 — Snapshot Pipeline (backup)

**Goal:** the core: `[CS3 read] -> [kopia snapshot/encrypt/dedup] -> [Garage]`.
One kopia repo per Space, DK as repo password.

**Depends on:** Phase 0 Spike 2 (kopia API notes), Phase 2 (CS3 reader),
Phase 3 (keys).

**Status: implemented.** All exit criteria met, including the live-OpenCloud
end-to-end run. Outcomes and deviations are recorded below and, where they change
a locked decision's implementation, in `decisions.md`.

## Design

### Feeding CS3 into kopia — **Option B chosen (virtual FS)**

Two options were considered:

- **Option A (staging):** stream the Space via CS3 into a temp dir, point
  kopia's local-FS source at it. Simple, correct mtimes; costs disk (needs
  space for the largest Space) and an extra copy.
- **Option B (virtual FS):** implement kopia's `fs.Entry`/`fs.Directory`
  interfaces backed directly by CS3 (stream on demand, no staging).

**Option B was implemented** (`pkg/snapshot/kopiafs.go`). kopia's uploader is
built around the `fs` abstraction and the interface surface turned out to be
small and stable enough to wrap thinly. Nothing is staged on disk, so the
largest Space no longer sets a disk-sizing requirement.

Two implementation details that matter:

- **Files are exposed as `fs.File`, not `fs.StreamingFile`.** kopia's cache-hit
  check (`snapshot/upload.findCachedEntry`) compares *size* for `fs.File` but
  deliberately skips size for `fs.StreamingFile`. Using `fs.File` keeps the same
  change-detection strength kopia gives a local filesystem source; with
  `fs.StreamingFile` a content change that preserved mtime would be missed.
  The cost is implementing `fs.Reader` (which requires `io.Seeker`); the reader
  is lazy, so a `Seek` issued right after `Open` costs no request, and seeking
  after reading reopens the stream at the new offset.
- **Mode, owner and device are constants.** They take part in kopia's cache-hit
  comparison, so they must not drift between runs (and permissions are out of
  backup scope anyway — decisions.md #4). Owners are skipped on restore, since
  the source carries no real uid/gid.

The choice is swappable behind `snapshot.Source`, a deliberately kopia-free
interface (`Name`, `List(dir)`, `Open(path, offset)`), so a staging
implementation could be dropped in without touching the engine. The CS3-backed
implementation lives in `pkg/backup/source.go`.

### Run flow (`/pkg/backup` orchestration)

Implemented in `backup.Runner`:

1. Take the Space's **run lock** (`jobs.Locker`) — no two runs per Space.
2. Resolve the Space via `cs3.SpaceReader.ListSpaces` and open a job record.
3. Resolve the Space's backup configuration (`spacecfg.Store`) → target id and
   retention window.
4. Resolve the **target** and open its credentials in memory:
   `targets.Store.GetTarget` → `targets.CredSealer.Open` (TW-unwrap;
   decisions.md #12/#14). Plaintext credentials live only in memory for this run
   and are never logged.
5. Unwrap DK via SRW (`keys.Wrapper`, in memory only).
6. Connect/open the kopia repo on the resolved target,
   `s3://<bucket>/<prefix>spaces/<space-id>/` (created on first run; repo
   password = DK).
7. Snapshot the Space source (mtime + structure preserved; files only —
   decisions.md #4 scope).
8. Record the run result in the job store.
9. Zeroize the DK.

Retention is **configured** here (`spacecfg.Config.RetentionWindow`, time-based
`keep-within`, deep default 90d) but never **applied** here: prune and
maintenance are a separate job kind, scheduled on its own cadence (decisions.md
#9 Tier 1; implemented in R7, issue #24). What Phase 4 *does* guarantee is that
kopia never expires anything on its own — see below.

### Consistency stance

OpenCloud Spaces are live; we snapshot file-by-file (no global point-in-time
fence). A file changing mid-read is the same exposure any file-level backup has.
Structure, size and mtime are recorded at walk time. Documented in
`pkg/backup/source.go`, not hidden.

## Deliverables — all implemented

- `snapshot.Engine` implementation (`snapshot.KopiaEngine`) with
  `Snapshot / RestoreAll / RestoreFile / Prune / List`.
- `snapshot.Source` (kopia-free) + CS3-backed implementation (Option B).
- `snapshot.StorageOpener` with an S3 implementation (production) and a
  filesystem implementation, so the whole engine is unit-testable with no
  container.
- Repo layout convention: `s3://<bucket>/<prefix>spaces/<space-id>/`
  (`snapshot.RepoPrefix`).
- `pkg/spacecfg`: per-Space backup configuration (target binding + retention
  window). User-owned, separate from the admin-owned target store.
- `jobs.MemoryStore`: job records plus the per-Space run lock.
- HTTP API (all space-member gated, all grant checks server-side):
  - `POST /api/v1/spaces/{id}/backup/run` — accepts a run, executes it in the
    background, returns the job id (202). Becomes the UI's "backup now".
  - `GET /api/v1/spaces/{id}/backup/runs` — run history.
  - `GET|PUT /api/v1/spaces/{id}/backup/config` — target binding and retention.
    A client-named target is only accepted if `targets.Authorizer.MayUse` allows
    it; "not granted" and "no such target" give the same answer.
- Optional first-start seeding of a single default target
  (`targets.Bootstrap`, `BOOTSTRAP_*`), so a fresh deploy is usable before the
  admin UI exists. Skipped whenever any target already exists.
- Config (env): `TW_KEY`, `BACKUP_PARALLELISM`, `BACKUP_WORK_DIR`,
  `BACKUP_UPLOAD_BYTES_PER_SECOND`, `BACKUP_DOWNLOAD_BYTES_PER_SECOND`,
  plus the bucket/prefix carried by the target record.

### Phase-2 debt paid here

The Phase-2 CS3 client left the data path stubbed. Phase 4 completed it:

- `cs3.Space` now carries the space **root `ResourceId`** verbatim; the previous
  placeholder duplicated the composite space id into all three fields. A
  composite-id split (`storageid$spaceid!opaqueid`) is the fallback.
- `OpenFile` streams for real: `InitiateFileDownload` → data-gateway HTTP GET
  with `x-access-token` + `X-Reva-Transfer`, with `Range` support (and a
  read-and-discard fallback when the gateway ignores it).
- `ListDir` was added and `Walk` fixed: paths are now space-relative and
  slash-separated with no `./` prefix, and references use reva's relative form
  (`.` for the root, `./sub/file` otherwise).

## Testing — implemented

- **Unit:** engine against a fake `Source` and kopia's filesystem blob backend
  (fast, no Garage); `fs` adapter; CS3 data path against a fake gateway plus an
  `httptest` data gateway; runner against in-memory stores; API handlers.
- **Pipeline (no build tag):** fake CS3 → real kopia → local filesystem backend
  → restore round-trip, so the whole wiring runs in every `go test`.
- **Integration (Garage fixture, `-tags integration`):**
  - [x] bucket contains only encrypted chunks/obfuscated paths (no filename or
        content substring findable), and every object lives under the Space's
        repo prefix,
  - [x] second run after a 1-file change uploads ~only the delta (dedup), and
        the unchanged 24 MiB file is not even re-read from the Space,
  - [x] kopia restore (library call) round-trips byte-identical + mtime,
  - [x] large file (> multipart threshold) round-trips,
  - [x] single-file restore,
  - [x] concurrent runs for the same Space are prevented (1 of 4 wins),
  - [x] time-based prune keeps the newest snapshot and it still restores.
- **Failure injection:** target unreachable mid-run → run fails, repository
  stays intact, previous snapshot still restores, next run succeeds. Source read
  failure → run recorded as failed, **no manifest persisted**, next run succeeds.
- **Live OpenCloud (env-guarded):** `TestIntegration_OpenCloudEndToEnd` skips
  unless `test/fixtures/opencloud/fixture.env` is sourced. `seed.sh` now also
  seeds a nested, non-ASCII path (`ordner-фото/café.bin`) so directory traversal
  against real reva references is covered.

## Exit criteria

- [x] End-to-end: seeded OpenCloud Space → encrypted snapshot on Garage →
      verified restore round-trip (library-level). Run against OpenCloud 7.3.0:
      2 files restored with matching sha256, including the nested non-ASCII path.
- [x] Dedup + encryption assertions green.
- [x] Failed-run recovery test green.
- [x] Success criterion 2 achieved (decisions.md).

## Amendments from the September 2026 review — R4 (kopia correctness)

- **Two kopia defaults could turn a failed or empty backup into a reported
  success**, and both were invisible to this phase's tests because no test Space
  contained an ignore marker and no test run lasted 45 minutes.
- **Ignore conventions are off** (`Uploader.DisableIgnoreRules`). A
  `.kopiaignore` or a `CACHEDIR.TAG` that reaches a Space — synced from a laptop,
  restored with a project folder, planted — must not decide what is backed up.
  The markers themselves are backed up as ordinary files.
- **Incomplete manifests are internal.** kopia's mid-upload checkpoints are
  saved as snapshot manifests; nothing distinguished them afterwards, so they
  were listed, restorable, selectable by the offline `decrypt` tool, and — worst
  — counted as "the newest snapshot" that prune protects. They are now filtered
  out of every path that serves a snapshot, and deleted when a run ends,
  successful or failed. See `decisions.md`, "Amendments … R4".
- **`EngineOptions.CheckpointInterval`** exists so the checkpoint behaviour is
  reachable in a test in seconds rather than in kopia's 45 minutes. Production
  has no reason to set it; values above kopia's maximum are refused at
  construction rather than mid-run.

## Risks — status

- Option B couples us to kopia's `fs` interfaces (not a stability-guaranteed
  API). **Mitigated:** kopia is pinned to v0.23.1, the adapter is ~230 lines in
  one file, and its behavioural assumptions (cache-hit comparison, lazy reader,
  constant mode/owner) are asserted by tests that will fail loudly on an upgrade.
- Very large spaces + staging (Option A). **Not applicable** — Option B stages
  nothing. The only local disk requirement is kopia's per-run config/cache under
  `BACKUP_WORK_DIR` (`/tmp` by default, `emptyDir` in the manifest).
- **New:** a per-run kopia cache means the repository format key is re-derived
  (scrypt) on every run — roughly half a second per run. Deliberate: a persistent
  cache would keep derived key material on local disk between runs.
- **New:** the run lock is process-local (`jobs.MemoryStore`). Correct for the
  single-replica deployment; scaling out needs a shared lock. Flagged for
  Phase 6.
