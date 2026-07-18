# Phase 0 — Decisions & Spikes

**Goal:** validate the four riskiest assumptions with throwaway experiments
before writing production code. Output is knowledge + reusable test fixtures,
not features.

**Order (by risk and dependency):**

1. Garage test fixture (fast, unblocks spike 2)
2. kopia-as-library (no OpenCloud needed)
3. CS3 read path (hardest unknown, needs test oCIS)
4. Web extension mechanism (independent, can run in parallel)

Spikes 1+2 are fully OpenCloud-independent — start immediately.

---

## Spike 1: Garage test fixture

**Question:** can we run Garage as an ephemeral single-node S3 target for CI and
local dev?

**Tasks:**
- Pin an image tag (`dxflrs/garage:v2.3.0` or newer; `--single-node
  --default-bucket` requires >= v2.3.0).
- Script: start container with `GARAGE_DEFAULT_ACCESS_KEY` /
  `GARAGE_DEFAULT_SECRET_KEY` / `GARAGE_DEFAULT_BUCKET` env vars, data in
  `/tmp`, no volumes (naturally ephemeral).
- Verify with any S3 client: put/get/list/multipart against
  `http://localhost:3900`, region `garage`, SigV4, path-style.
- Wrap as a Go test helper (testcontainers-go or plain `os/exec` + readiness
  poll) — this becomes the integration-test fixture for all later phases.

**Exit criteria:**
- [x] One command/helper brings up a clean Garage, S3 round-trip passes.
      (`internal/testutil.StartGarage`)
- [x] Multipart upload works. (24 MiB, checksum-verified)
- [x] Object-Lock / versioning calls confirmed failing — both return
      `NotImplemented`; recorded in `phase-0-findings.md`.

**Risk:** low. Research says trivial; confirm once.

**Status: DONE.** See `phase-0-findings.md`. Fixture: `internal/testutil/`.
Gotcha worth flagging for Phase 1: AWS SDK v2 default checksum validation breaks
multipart GET against Garage — set request/response checksum to `WhenRequired`.

---

## Spike 2: kopia as a library against Garage

**Question:** does kopia's Go API (`github.com/kopia/kopia/repo`, `/snapshot`)
expose everything we depend on — outside its CLI?

**Tasks (throwaway `main.go` or Go test):**
1. Create a kopia repo on the Garage bucket (S3 storage backend, repo password =
   stand-in for DK).
2. Snapshot a local directory (varied content: nested dirs, non-ASCII names,
   a larger file for multipart, mtimes).
3. Inspect bucket: only encrypted chunks + obfuscated paths; no plaintext
   filenames or content leak.
4. Second snapshot after small change → verify dedup (marginal upload size).
5. Full restore into an empty dir → byte-identical + mtime preserved.
6. **Single-file restore** (pin the exact API — gates the backlog feature).
7. Prune with **time-based** retention; verify old snapshot data removed after
   maintenance.
8. Note API pain points: repo connect lifecycle, password handling, maintenance
   ownership, context requirements, logging hooks.

**Exit criteria:**
- [x] All 7 steps pass using only importable packages (no kopia CLI).
      (`spikes/kopia/`, `go test -tags integration ./spikes/kopia/...`)
- [x] Written API-surface notes: see `phase-0-findings.md` (§ API surface to
      wrap in `/pkg/snapshot`).
- [x] Dedup confirmed working over Garage. (~25 MB snapshot 1 → ~9.8 KB
      snapshot 2 after a 1-line change; 24 MiB binary not re-uploaded.)

**Fallback if blocked:** restic as subprocess (documented decision change —
would ripple into decisions.md #5). **Not needed** — kopia library API suffices.

**Status: DONE.** Key finding that amends the plan: kopia retention is
**count-based only**; time-based `keep-within` (decisions.md #10) must be
implemented by us (list → `DeleteManifest` older than window → full maintenance
GC). Two restore gotchas: call `FilesystemOutput.Init` and set
`RestoreDirEntryAtDepth = MaxInt32`. Details in `phase-0-findings.md`.

---

## Spike 3: CS3 read path against a test OpenCloud

**Question:** can we list Spaces and stream file bytes out of OpenCloud via CS3
gRPC + HTTP data provider, with a token suitable for a server-side worker?

**Hardest unknown in the whole plan.** Assumption to verify: gRPC gives
metadata + download refs; bytes flow over the HTTP data provider.

**Tasks:**
- Stand up a test OpenCloud instance (docker compose; document exact setup —
  it becomes the future integration environment).
- Using `github.com/cs3org/go-cs3apis`:
  - Authenticate (user token first; then investigate service/impersonation
    tokens for the unattended worker — reva machine auth / service accounts).
  - `ListStorageSpaces` for a user context.
  - Walk a space: `ListContainer` / stat.
  - `InitiateFileDownload` → follow the data-provider URL → stream bytes.
- **Shared-space membership:** determine how CS3 exposes space members/roles
  (needed for "who may retrieve a shared space's RK", decisions.md #7).
- Record: endpoints, token types + lifetimes, TLS/config needed inside the
  cluster, pagination behavior on large folders.

**Exit criteria:**
- [x] Spaces listed for a test user. (`ListStorageSpaces` via service account)
- [x] One file streamed end-to-end and checksum-verified.
      (`InitiateFileDownload` → data gateway HTTP GET → sha256 match)
- [x] Documented answer: the **unattended** worker uses a **service account**
      (`auth-service`, type `"serviceaccounts"`, owner scope, reads any space).
      See `phase-0-findings.md`.
- [x] Documented answer: space membership is read from the space's `Opaque`
      map (`grants` / `groups` / `grants_expirations`) on `ListStorageSpaces`.

**Risk:** high. If server-side impersonation is not feasible as assumed, the
scheduler design (Phase 6) and SRW model need revisiting.

**Status: DONE — the high-risk assumption holds.** Server-side unattended read
works via a service account (no impersonation needed), so the Phase 6 scheduler
/ SRW model stand. Fixture: `test/fixtures/opencloud/` (kept for Phases 2/4/5/8).
Gotcha: `InitiateFileDownload` needs a space-relative reference
(`{ResourceId: space.Root, Path: "./name"}`), not a bare resource id; expose the
gateway with `OC_GATEWAY_GRPC_ADDR=0.0.0.0:9142`.

---

## Spike 4: OpenCloud Web extension mechanism

**Question:** how is a Web extension actually packaged and loaded by the
OpenCloud Web UI?

**Tasks (research + minimal hello-world, no real code):**
- Read current OpenCloud/oCIS Web extension docs; identify the mechanism
  (web app bundle, `apps.yaml` / `web.config`, AMD/ESM module, manifest fields).
- Load a minimal "hello world" app into the test instance from Spike 3:
  navigation entry appears, app renders.
- Record: build tooling expected (Vite? pnpm?), Vue version, design-system
  package (OpenCloud design system), how the app gets its backend URL + auth
  token (session forwarding).

**Exit criteria:**
- [x] Hello-world extension visible in the test instance. Built with Vite +
      `@opencloud-eu/extension-sdk`, deployed to `web/assets/apps/backup-spike/`,
      injected into `config.json` as an `external_apps` entry, entrypoint +
      chunks served `200`. (`spikes/webext/`)
- [x] Documented packaging + auth-forwarding mechanism for Phase 8.
      See `phase-0-findings.md`.

**Risk:** medium; independent of the backend track.

**Status: DONE.** Packaging = Vue 3 + TS, Vite 8 module-federation via
extension-sdk → `manifest.json` + `remoteEntry`. Loading = drop under
`web/assets/apps/<id>/`, OpenCloud scans + injects into `config.json` (restart
required). Auth = user's OIDC browser session (`client_id: web`), token via
`web-pkg` composables — implicit session forwarding.

---

## Phase output

- "Still open" verification items resolved (or decision changes logged in
  decisions.md).
- Garage test helper kept (fixture for Phases 1–7).
- Test OpenCloud compose setup kept (fixture for Phases 2, 4, 5, 8).
- Spike code itself is throwaway; learnings land in these docs.
