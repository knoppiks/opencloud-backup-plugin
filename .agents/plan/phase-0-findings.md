# Phase 0 — Spike Findings (Spikes 1 & 2)

Results of the OpenCloud-independent spikes. Spikes 3 (CS3 read path) and 4 (Web
extension) are **not yet run**. Fixtures produced here are kept; spike drivers
under `/spikes/` are throwaway.

## Pinned versions

| Component | Version | Notes |
|---|---|---|
| Garage image | `dxflrs/garage:v2.3.0` | `--single-node --default-bucket` needs >= v2.3.0 |
| kopia (library) | `github.com/kopia/kopia v0.23.1` | importable; no CLI used |
| AWS SDK (tests only) | `aws-sdk-go-v2` (config/credentials/s3/manager) | Garage verification + plaintext scan |
| testcontainers-go | `v0.43.0` | brings up ephemeral Garage |

---

## Spike 1 — Garage test fixture ✅

**Outcome:** validated. Reusable Go helper lives at `internal/testutil/`
(canonical Phase-1 home), consumed behind the `integration` build tag.

- `internal/testutil/garage.go` — `StartGarage(ctx, t)` brings up a clean,
  ephemeral single-node Garage with a default bucket + access key already
  provisioned. Data in container `/tmp`, no volumes → naturally pristine per
  test. Cleanup via `t.Cleanup`.
- `internal/testutil/s3client.go` — `(*Garage).S3Client(ctx, t)` returns an AWS
  SDK v2 S3 client wired for Garage: static creds, region `garage`,
  `BaseEndpoint`, **path-style** addressing.
- `internal/testutil/garage_integration_test.go` — proves the exit criteria.

**Confirmed behaviours:**

- **S3 round-trip** (put/get/list) works, path-style + SigV4.
- **Multipart** works (24 MiB, 5 MiB parts, checksum-verified round-trip).
- **Object Lock** → `PutObjectLockConfiguration` returns **`NotImplemented`**.
- **Bucket versioning** → `PutBucketVersioning` returns **`NotImplemented`**;
  `GetBucketVersioning` returns empty (unversioned). Confirms decisions.md #8.

**Gotchas recorded (for Phase 1 / all integration tests):**

1. Garage container **entrypoint is the `/garage` binary**; run
   `server --single-node --default-bucket` and override `Entrypoint`.
2. Garage **requires a config file** even in single-node mode. Mandatory fields
   we hit: `rpc_bind_addr`, `rpc_secret`, `metadata_dir`, `data_dir`,
   `db_engine`, `replication_factor`, plus `[s3_api]` and `[admin]`. The helper
   injects a minimal `/etc/garage.toml` via `ContainerFile`.
3. Secret key must be **>= ~40 chars** or Garage rejects the default key.
4. **AWS SDK v2 checksum default breaks multipart GET on Garage.** The SDK's
   default `RequestChecksumCalculation`/`ResponseChecksumValidation` =
   `WhenSupported` makes `GetObject` fail with a CRC32 mismatch on multipart
   objects (Garage returns a composite checksum the SDK can't verify). Fix:
   set both to **`WhenRequired`** on the client (done in `s3client.go`). Any real
   S3 client we build in `/pkg/s3target` must apply the same setting.

---

## Spike 2 — kopia as a library ✅

**Outcome:** validated. All 7 steps pass using only importable kopia packages
(no CLI). Driver: `spikes/kopia/` (throwaway).

**Dedup measured (real numbers):**

- Snapshot 1: 4 files, 25,165,914 logical bytes → **~25.2 MB uploaded** to Garage.
- Snapshot 2 (after appending one line to a small text file): **~9.8 KB
  uploaded**. The unchanged 24 MiB binary was **not** re-uploaded. Dedup over
  Garage confirmed.

**No plaintext at rest confirmed:** listing objects directly from Garage (via
the S3 client, bypassing kopia) and scanning keys + bodies found **no** source
filenames (`readme.txt`, `notes.txt`, `große-datei`, `café`, `фото`) and **no**
content markers. Content encrypted, paths obfuscated.

**Full restore:** byte-identical, mtime preserved (second granularity).
**Single-file restore:** works via `snapshotfs.GetNestedEntry`.

### API surface to wrap in `/pkg/snapshot`

Lifecycle (repo-per-Space; repo password = DK):

- Storage: `repo/blob/s3.New(ctx, *s3.Options, isCreate)` → `blob.Storage`.
  - Endpoint is **host:port with no scheme**; set `DoNotUseTLS` for plain HTTP.
    Use `Prefix` to namespace within a bucket if we share one across Spaces.
- Create: `repo.Initialize(ctx, st, &repo.NewRepositoryOptions{}, password)` then
  `repo.Connect(ctx, configFile, st, password, &repo.ConnectOptions{})`.
  - kopia keeps a **local config file + cache**; connect writes it. We must
    manage this per-Space (temp dir per run is fine for a stateless worker).
- Open: `repo.Open(ctx, configFile, password, &repo.Options{})` → `Repository`.
  For maintenance we type-assert to `repo.DirectRepository`.

Snapshot (inside `repo.WriteSession`):

- `localfs.NewEntry(dir)` → source `fs.Entry`.
- `policy.TreeForSource(ctx, w, si)` → `*policy.Tree`.
- `upload.NewUploader(w).Upload(ctx, src, policyTree, si, prev...)` →
  `*snapshot.Manifest`. Pass previous manifests
  (`snapshot.ListSnapshots`) to enable the hash cache.
- Persist with `snapshot.SaveSnapshot(ctx, w, man)`.
- `snapshot.SourceInfo{Host, UserName, Path}` identifies the source; for us
  Host/UserName/Path will encode the Space identity deterministically.

Restore:

- `snapshotfs.SnapshotRoot(rep, man)` → root `fs.Entry`.
- Full: `restore.Entry(ctx, rep, out, root, restore.Options{...})`.
- Single file/subtree: `snapshotfs.GetNestedEntry(ctx, root, []string{...})`
  then `restore.Entry` on that entry.
- **Two mandatory settings or restore silently misbehaves / panics:**
  1. `restore.FilesystemOutput` must have **`Init(ctx)` called** before
     `restore.Entry`, otherwise `WriteFile` nil-derefs (its stream copier is
     unset). This is **not** done by `restore.Entry`.
  2. `restore.Options.RestoreDirEntryAtDepth` defaults to `0`, which writes
     **shallow `.kopia-entry` placeholders** instead of real files. Set it to
     `math.MaxInt32` for a materialised full restore.

Pruning / retention — **important divergence to record:**

- kopia's `policy.RetentionPolicy` is **count-based only**
  (`KeepLatest/Hourly/Daily/Weekly/Monthly/Annual`). There is **no native
  `keep-within` / time-based field.** decisions.md #10 mandates time-based
  retention, so **we implement keep-within ourselves**:
  1. `snapshot.ListSnapshots` → filter manifests older than `now - window`.
  2. `RepositoryWriter.DeleteManifest(ctx, id)` for each expired manifest
     (inside a write session).
  3. GC unreferenced content: obtain a `DirectRepositoryWriter`
     (`repo.DirectWriteSession`), ensure maintenance ownership
     (`maintenance.GetParams` / `SetParams` with
     `dw.ClientOptions().UsernameAtHost()` as `Owner`), then
     `maintenance.RunExclusive(ctx, dw, maintenance.ModeFull, force,
     func(...) { return maintenance.Run(ctx, rp, safety) })`.
- **Safety window:** `maintenance.SafetyFull` keeps recently-unreferenced
  content (safe for concurrent snapshotting); `SafetyNone` deletes immediately.
  Production prune runs as a **separate job** (decisions.md #9 Tier 1), so
  `SafetyFull` is the right default; the spike used `SafetyNone` only to make
  deletion observable in one run.
- Verified: after deleting snapshot-1's manifest + full GC, **snapshot-1 is gone
  from the manifest list** and **snapshot-2 still restores byte-identically**.
- **Blob count is not a prune signal.** GC compacts indexes and writes
  maintenance/log blobs, so object count can rise even as old content is
  dropped. Assert on *snapshot restorability* and *manifest presence*, not raw
  object counts.

### Pain points / lifecycle notes for the wrapper

- **Repo connect is stateful on local disk** (config + cache). A stateless
  server worker should create a throwaway config/cache dir per run (or manage a
  per-Space cache deliberately for speed). `snapshot.Engine` should own this
  lifecycle and clean up.
- **Maintenance ownership is a real concept.** Only the owner runs
  `RunExclusive`. For us, prune runs from a "separate trusted context"
  (decisions.md #9 Tier 2) — that context must be (or claim) the maintenance
  owner. Design the key/owner split with this in mind.
- **Context + logging:** kopia uses `context.Context` throughout and its own
  zap-based logging; no key material is logged by our code, but we should route
  kopia's logger and ensure we never log the repo password (= DK).
- **Object Lock path:** `repo.NewRepositoryOptions` exposes `RetentionMode` /
  `RetentionPeriod` and maintenance has `ExtendObjectLocks` — capability-flag
  these for a future Garage with Object Lock (decisions.md #5, #8). Not usable
  today.

### Fallback status

restic subprocess fallback (decisions.md #5) **not needed** — kopia's library
API covers everything we depend on.
