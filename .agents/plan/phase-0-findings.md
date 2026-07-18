# Phase 0 — Spike Findings

Results of all four Phase-0 spikes. **All four are validated.** Fixtures are
kept; spike drivers under `/spikes/` are throwaway.

## Pinned versions

| Component | Version | Notes |
|---|---|---|
| Garage image | `dxflrs/garage:v2.3.0` | `--single-node --default-bucket` needs >= v2.3.0 |
| kopia (library) | `github.com/kopia/kopia v0.23.1` | importable; no CLI used |
| AWS SDK (tests only) | `aws-sdk-go-v2` (config/credentials/s3/manager) | Garage verification + plaintext scan |
| testcontainers-go | `v0.43.0` | brings up ephemeral Garage |
| OpenCloud image | `opencloudeu/opencloud-rolling:7.3.0` | single-container all-services; init + server |
| go-cs3apis | `v0.0.0-20260424072047-8d9ef7076ae9` | matches OpenCloud 7.3.0's pin |
| Web extension SDK | `@opencloud-eu/extension-sdk ^7.0.0` (+ `web-pkg`, `web-client` ^7.0.0) | Vite 8 / module federation build |

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

---

## Spike 3 — CS3 read path ✅ (the highest-risk item — validated)

**Outcome:** validated end-to-end against a live OpenCloud 7.3.0. A headless
worker authenticated with a **service account**, listed spaces, walked a space,
initiated a download and **streamed the file bytes with a matching sha256**.

Fixture (kept): `test/fixtures/opencloud/` — `docker-compose.yml`, `up.sh`
(init + start + secret extraction → `fixture.env`), `seed.sh` (seed a known
file + checksum), `down.sh [--purge]`. Driver (throwaway): `spikes/cs3/`
(`go test -tags integration ./spikes/cs3/...`, reads config from env).

### Worker credential: **service account** (RESOLVED — was open in decisions.md)

OpenCloud offers two interservice mechanisms; the choice was not a guess, it is
in the source:

- `auth-machine` (type `"machine"`): interservice auth **when impersonating a
  specific user**. Needs `OC_MACHINE_AUTH_API_KEY`; you mint a token *as a named
  user*.
- `auth-service` (type `"serviceaccounts"`): interservice auth **using service
  accounts**. Needs `OC_SERVICE_ACCOUNT_ID` / `OC_SERVICE_ACCOUNT_SECRET`.

Reva's serviceaccounts auth manager grants an **owner scope** and
`USER_TYPE_SERVICE`, and the auth-service docs state *"all service users can
stat all files on all spaces."* For an unattended, cross-space backup worker
this is the right fit: **one credential, reads any space, no per-user
impersonation.** Machine-auth would force us to resolve+impersonate each space's
owner, coupling the worker to user identity for no benefit. **Decision: the
unattended worker uses a service account.**

- The gateway's authregistry maps auth `type` → provider; the service-account
  type string is literally **`"serviceaccounts"`** (also `"basic"`, `"machine"`).
- The service account ID/secret are generated by `opencloud init` into
  `opencloud.yaml` (`service_account_id` / `service_account_secret`), i.e. the
  same secret every service shares. In production this is provisioned/rotated by
  the operator via `OC_SERVICE_ACCOUNT_ID`/`SECRET`.

### The read flow (endpoints, tokens, gotchas)

1. **Dial** the CS3 gateway gRPC. Default bind is `127.0.0.1:9142` (localhost
   only); set **`OC_GATEWAY_GRPC_ADDR=0.0.0.0:9142`** and publish the port so an
   out-of-cluster worker can reach it. In-cluster this is the internal gateway
   address; TLS off on the internal gRPC in this setup (plaintext).
2. **Authenticate**: `Gateway.Authenticate{Type:"serviceaccounts", ClientId:ID,
   ClientSecret:SECRET}` → returns a **reva JWT access token** (~397 bytes here).
   Token lifetime is reva's transfer/token TTL (short-lived, minted per session;
   re-authenticate per run — cheap).
3. **Carry the token** on every subsequent gRPC call as metadata header
   **`x-access-token`** (reva `pkg/ctx.TokenHeader`).
4. `ListStorageSpaces` (optionally filtered by `TYPE_OWNER` for a user, or
   `TYPE_ID` for one space). Returns `StorageSpace` incl. `Root` (`ResourceId`),
   `SpaceType` (`personal`/`project`/…), `Owner`, and an **`Opaque`** map.
5. Walk with `ListContainer{Ref:{ResourceId: space.Root}}` and `Stat`.
6. `InitiateFileDownload` → `Protocols[]` each with `Protocol`
   (`simple`/`spaces`), a **`DownloadEndpoint`** URL (here
   `https://localhost:9200/data`, the reva **data gateway** on the HTTP proxy),
   and a **transfer token**.
7. **Stream** an HTTP GET to `DownloadEndpoint` with headers **`x-access-token`**
   (worker token) and **`X-Reva-Transfer`** (the transfer token). Body = file
   bytes. Checksum-verified.

**Critical gotcha:** `InitiateFileDownload` must be given a **space-relative
reference** — `{ResourceId: space.Root, Path: "./name"}` — *not* a bare
`{ResourceId: fileID}`. A bare id-only ref resolves to path `"/"` on the
storage-users data server and fails with `invalid reference path:"/". resource_id
must be set` (HTTP 500). Build references from the space root + relative path.

### Shared-space membership (decisions.md #7 — RESOLVED)

Membership/roles are exposed on the **space's `Opaque` map** returned by
`ListStorageSpaces`. Relevant keys:

- **`grants`** — map of principal (user) grants → roles/permissions on the space.
- **`groups`** — group grants.
- **`grants_expirations`** — expiry per grant.

For a personal space these are `{}` (owner only). For a project/shared space
they enumerate the members and their roles — this is the query that answers
*"who may retrieve a shared space's RK"* (any member, per decisions.md #7). No
extra sharing API is needed for the RK-retrieval check; read the space grants.

### Fixture notes (for Phases 2/4/5/8)

- `opencloud init` is **interactive by default** — pass `--insecure true
  --admin-password admin -f` to script it. Secrets land in
  `config/opencloud.yaml`; `up.sh` extracts them into `fixture.env`.
- HTTP basic auth is **off by default** (OIDC only). The fixture sets
  `PROXY_ENABLE_BASIC_AUTH=true` *for tests only* so data can be seeded via
  WebDAV without an OIDC dance. The worker path does **not** use basic auth.
- Personal-space WebDAV path: `/dav/spaces/<driveId>/<file>`; drive id from
  `GET /graph/v1.0/me/drives` (`driveType:"personal"`).

---

## Spike 4 — OpenCloud Web extension ✅

**Outcome:** validated. A hello-world extension was built, deployed into the
running fixture, **discovered by OpenCloud, injected into `config.json`, and
served to the SPA** (entrypoint + chunks all HTTP 200). Nav entry + view are
registered via `defineWebApplication`.

Spike app (throwaway): `spikes/webext/` (Vite + `@opencloud-eu/extension-sdk`).

### Packaging mechanism

- An app is authored in **Vue 3 + TypeScript**, entrypoint `src/index.ts`
  exporting `defineWebApplication({ setup() { return { appInfo, routes,
  extensions } } })` from `@opencloud-eu/web-pkg`.
- Build with **Vite 8** via `@opencloud-eu/extension-sdk`'s `defineConfig({ name
  })`. Output is a **module-federation** bundle under `dist/` plus a generated
  **`dist/manifest.json`** → `{"entrypoint": "js/remoteEntry-*.mjs"}`.
- Toolchain: the skeleton uses **pnpm**, but plain **npm works** (all deps are
  public on npmjs). Node 24 used here. Design system = `@opencloud-eu/web-pkg` +
  extension-sdk Tailwind (`ext:` utility prefix).

### Deployment / loading mechanism

- Drop the built app as a directory under the web assets apps path:
  **`$OC_DATA_DIR/web/assets/apps/<id>/`** (must contain `manifest.json` + the
  bundle; a `dist/` subdir is also accepted). The fixture mounts host
  `./apps` → that path. Env override: `WEB_ASSET_APPS_PATH`.
- The `web` service scans the apps dir on start, reads each `manifest.json`, and
  injects the apps as **`external_apps`** into the dynamically-rendered
  **`config.json`** (`{id, path:/assets/apps/<id>/js/remoteEntry-*.mjs}`). The
  SPA then loads them. **Restart required** to pick up newly added apps.
- Verified: `GET /config.json` lists our `external_apps` entry and the
  entrypoint `.mjs` + a lazy view chunk both serve `200 text/javascript`.

### Auth / backend-URL forwarding (for Phase 8)

- The app gets the **backend URL** from `config.json` `server`
  (`https://…/`) — same origin as the SPA.
- **Auth is the user's OIDC browser session** (config `openIdConnect`,
  `client_id:"web"`, PKCE code flow). The extension inherits it; `web-pkg`
  composables (client service / auth store) provide the **access token** and an
  API client. Our Phase-8 UI therefore calls the backup backend **as the logged-
  in user with their bearer token** — no separate login, "session forwarding" is
  implicit. This aligns with decisions.md #2 (Path B is authorized by the user's
  own session).

### Phase-8 build/tooling summary

Vite 8 + `@opencloud-eu/extension-sdk` (module federation), Vue 3 + TS,
vue-router 5, vue3-gettext, design system from `@opencloud-eu/web-pkg`. Ship the
built `dist/` into `web/assets/apps/opencloud-backup/`.
