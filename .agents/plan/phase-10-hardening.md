# Phase 10 — Hardening & Go Hygiene

**Goal:** fix the silent-failure bugs the October review found, make the
service diagnosable from its own output, protect the parse-forever formats
against the future, and settle the module layout before anything is released
under it.

**Depends on:** Phase 9 (so fixes are tested on the full matrix).
**Findings:** review-2026-10.md F1–F7, G1–G11.
**Decision:** #26 (module path and `internal/` layout).

Split into sub-PRs in the order below. The layout change (10.7) is
deliberately late — one mechanical diff after the behavioural ones, so review
of the fixes is not buried in a rename — and before the lint expansion
(10.8), whose `depguard` rules name the final paths.

## 10.1 Bugs

| Finding | Fix | Test |
|---|---|---|
| F1 startup leaves the instance record live | `serve` calls the returned cleanup on every exit path; restructure as `run() error` with `main` doing the single `os.Exit` | unit: a `buildService` that fails after the claim releases it (fake guard); re-start within TTL succeeds |
| F2 transient read error drops a record | `Versions.Latest` and the legacy listing skip **not-found only**; any other error is returned (or collected and reported like `unreadable`, owner to choose in PR); `targets/state.go` keeps `unreadable` | unit: fake store returning a transient error makes `spacecfg.List` fail/report, not shrink |
| F4 manifest paths | `ReadManifest` rejects non-local `RepoDir`/`EnvelopeRef` (`filepath.IsLocal`) as `ErrCorrupt`; new `ErrNewerTakeOut` sentinel, `decrypt` explains it | unit + fuzz (10.4) |
| F5 IMDS fallback | `objstore.NewS3` takes static credentials only; empty → error before any request. `takeout` says which env var is missing | unit: empty creds fail with no network call |
| F6 stdout pollution | logs go to **stderr**; stdout is reserved for command output | test: `provision-state-space` stdout is exactly the id |
| F7 background work | manual runs registered with a service-level `WaitGroup`/context so SIGTERM cancels and drains them; `recover()` at each goroutine boundary (run, scheduler tick, HTTP handler) converts a panic to a failed run + ERROR log; SMTP via a dialer with deadline; drain budget ≥ outcome-write worst case, derived from the same constants | unit per item; panic test proves the scheduler survives |

**Outcome (10.1, #70).** All six fixed, each with a test that fails on the
old code. Where it differs from the table:

- **F1.** `buildService` itself releases what it acquired when it fails
  (named result + deferred cleanup), so the caller's contract is "on error,
  nothing to clean up". `main` is `os.Exit(run(args, stdout, stderr))`;
  `serve` returns an error and every exit path runs its defers. The test
  shares one state store across two `buildService` calls (an injected
  `startupDeps.openState`, not a fake guard), which is the restarted pod
  finding the same state Space.
- **F2.** *Confirmed (owner, 2026-10-12):* any read error other than not-found
  or malformed **fails the listing** (`Versions.Latest` and `Documents.All`,
  one helper), rather than being collected next to `unreadable`. A failed
  tick is logged and the next tick retries; collecting would have reported a
  blinking backend as "corrupt record". *Also confirmed:* the targets store's
  `ListTargets` now returns `ErrUnreadableTargets` when a record cannot be
  decoded, instead of dropping it — its callers are TW rotation (would leave
  that target sealed under the retiring key) and bootstrap (would take a
  store whose only record is corrupt for an empty one), the same reasoning as
  the admin API's existing "unreadable configuration is an error". The
  member-facing `VisibleTargets` still skips it, so one corrupt record does
  not take the target list from every member.
- **F4.** As planned. The fuzz target for `ReadManifest` is 10.4's.
- **F5.** Also applied to kopia's S3 driver (`snapshot.S3Opener`, since
  10.6 `s3repo.Opener`), which has the same fallback (env, then IAM/IMDS) —
  the server-side path the review mentions. That check is local to the
  opener so the decrypt path does not import `objstore` (10.6). The admin connection check reports missing
  credentials as `auth_failed`.
- **F6.** As planned; the browser E2E setup now reads the id as the whole of
  stdout instead of its last line.
- **F7.** `jobs.Background` owns manual backups and restores (cancelled at
  SIGTERM, refused afterwards with a 503, waited for). A panic inside a run
  becomes a failed run through `jobs.Recover`; the scheduler recovers per run
  goroutine and per tick; `api.RecoverPanics` wraps the HTTP handler. Drain =
  `jobs.OutcomeWorstCase` (34 s) + 5 s; with the 10 s HTTP shutdown that is
  49 s, under the manifest's 60 s grace period — a test holds the two
  together. SMTP is a `net/smtp` client over a dialer with a 30 s deadline
  for the whole conversation. **Not covered:** a panic in a goroutine kopia
  starts itself still ends the process; only kopia can recover there. The
  HTTP recovery logs method and path only; the request id comes with 10.2.

## 10.2 Diagnosability (the minimum; metrics are Phase 14)

- `api.Server` gets a logger. Every 5xx logs the cause server-side with a
  request id; the client still gets the safe message. Never key material,
  never credentials (existing `noleak` tests extended to the new log lines).
- Middleware: request id (also returned as a response header so a user can
  quote it), access log at INFO without query strings, panic recovery.
- `http.Server.ErrorLog` bridged to slog.
- `keys.Store` takes a `context.Context` (G5).
- Consistent snake_case log keys (lint-enforced in 10.8).

**Outcome (10.2, #72).** All five done. Where it differs from the plan:

- **One line per request, not an access line plus an error line.** The
  observer (`pkg/api/observe.go`) writes the access line when the request
  ends; a handler answering 5xx goes through `serverError`, which keeps the
  cause on the request, and the access line is raised to WARN/ERROR and
  carries `code` and `err`. A panic is folded in the same way (panic + stack
  on the line), so `api.RecoverPanics` is gone and "exactly one line per 5xx"
  holds by construction. A source-scan test
  (`TestServerErrorsGoThroughServerError`) fails if any 5xx is answered
  without a cause; "not configured" 503s log their message as the cause.
- **Levels, *confirmed (owner, 2026-10-12)*:** 503 is WARN ("not now": a
  dependency not wired or not up yet, shutting down); every other 5xx and
  every panic is ERROR; the health probes log at DEBUG while they pass, since
  the kubelet calls them every few seconds; everything else INFO.
- **Request id (owner decision, 2026-10-08):** an incoming `X-Request-Id`
  of 1–128 characters from `[A-Za-z0-9._:-]` is kept, from any caller, so an
  ingress's or the browser's id reaches this log; otherwise `backupd` mints
  16 hex characters from `crypto/rand`. Returned as `X-Request-Id` only; it
  is not in the JSON error body and the web UI does not show it (owner: not
  now). The extension sending its own id is a separate PR.
- **Logged path** comes from the request line, so a `BACKUPD_BASE_PATH`
  prefix stripped before the router is still in the log; never the query.
- **Causes kept elsewhere, as F3 listed:** `keys.StateStore` wraps the state
  error under its sentinel instead of replacing it (the state store names
  keys and transports, never document contents). The SMTP sink names the
  step that failed and the local cause (connect, TLS, timeout); a *server
  reply* is reported by its numeric code only, because a reply to AUTH can
  quote the credentials.
- **G5:** every `keys.Store` method takes a `context.Context`; the state
  store still bounds each operation by its 30 s `storeTimeout`, now below
  the caller's deadline instead of beside it. `rotate.SRW` takes one too.
- **Log keys:** the three camelCase keys left (`basePath`, `adminAppRoleId`,
  `maxConcurrent`) are snake_case; errors stay under `err`, Spaces under
  `space`, as everywhere else.
- `http.Server.ErrorLog` is `slog.NewLogLogger` at WARN.

## 10.3 Configuration

- Configuration parsed and validated in one place at startup, with defaults
  declared beside the field. All ~48 variables. (Planned as one struct; see
  the outcome: declared per component instead.)
- Strict booleans (`true`/`false` only; anything else refused at startup).
- The declarations are the source for the **environment reference** the docs
  site publishes (Phase 13): a generate step writes it, a test fails when it
  is stale.
- Effective-config log line at startup with secrets redacted by type, not by
  name list.
- The `REPLACE_ME` placeholder check reads the struct, so `configPrefixes`
  stops being a hand-kept list.
- Service-account credentials checked in serve mode (today only in
  `provision`).

**Outcome (10.3, #74).** All six done. Where it differs from the plan:

- **Declared per component, not one struct (owner, 2026-10-09; see
  `phase-10-3-config-per-component.md` and decisions.md).** `internal/config`
  is machinery only: `Load` (parse, placeholders, then every section's
  `Validate`, all errors joined), `Secret`, `Describe`/`Names`/`Effective`,
  `Reference`, and `configtest` for the checks every binary runs. Components
  declare their own sections: `api.Env` (Authentication, OpenCloud),
  `cs3.Env`, `scheduler.Env`, `notify.SMTPEnv`, `targets.BootstrapEnv`.
  `cmd/backupd/env.go` declares the binary's own (listener, state, custody
  keys, work directory) and composes `serviceEnv`; `cmd/takeout/env.go`
  declares the extractor's two S3 credentials. `run` loads once and hands
  each consumer its section (`dialCS3(cfg.CS3)`, `provision(cfg.CS3,
  cfg.State)`, `rotate(cfg.Keys, cfg.CS3, cfg.State)`); no `os.Getenv` is left
  under `cmd/`.
- **Defaults** are the values in the struct before loading, built from the
  components' own constants (`scheduler.DefaultEnv()`, `api.DefaultEnv()`,
  `cs3state.DefaultPrefix`). There is no `default` tag and no defaults-sync
  test.
- **Reference**: one file per binary, `docs/reference/environment-backupd.md`
  and `environment-takeout.md`, written by `TestEnvironmentReference -update`
  in each `cmd` package (`make generate`); the same test fails when a file is
  stale.
- **Every problem in one error.** Type errors and every section's checks are
  collected and reported together, so one restart shows them all. An
  unreplaced placeholder is reported alone, as before, because everything
  after it is a symptom.
- **Strictness, *confirmed (owner, 2026-10-12)*:** booleans are exactly `true`
  or `false` (not `1`, `yes`, `True`); `STATE_BACKEND` refuses anything but
  `memory` (case-insensitive, as before) instead of reading a typo as
  "durable". Both used to be silently accepted. The shipped manifests only
  use `"true"`/`"false"`.
- **Redaction by type, *confirmed (owner, 2026-10-12)*:** `config.Secret`
  holds its value behind an unexported pointer and prints `[redacted]` (or
  nothing when unset) through `String`, `GoString`, `LogValue` and
  `MarshalText`, so even `%d` or reflection over the struct shows no value.
  The rule for what is a Secret: whatever the manifest takes from a
  Kubernetes Secret, which includes `OC_SERVICE_ACCOUNT_ID` and the S3
  access key ids. `configtest` fails if a credential-named variable is
  declared as a plain string, or if a set secret reaches the configuration
  line, a formatted root, JSON, or a load error.
- **Effective-config line:** `msg=configuration`, one attribute per variable
  under `config`, keyed by the lower-cased variable name, unset ones
  included so defaults are visible. Logged by `serve` before anything can
  fail; the commands do not log it.
- **Service account, *confirmed (owner, 2026-10-12)*:** required whenever
  `CS3_GATEWAY_ADDR` is set, in every mode (service, rotation, provision),
  since every CS3 call is made as it.
- **Checked at load:** the base-path rules, the TLS pair, the OIDC
  prerequisites, the timezone, the wrapping keys' shape and SRW ≠ TW (the
  `_OLD` keys too), `CS3_DATA_SERVER_URL`, and, *confirmed (owner,
  2026-10-12)*, an enabled
  bootstrap's completeness and its need for `TW_KEY`. **Left at startup:**
  "`STATE_SPACE_ID` required" (the provisioning command needs it *unset*)
  and the work directory's filesystem.
- **Placeholder check** reads the declared variables, so a placeholder in a
  name the service does not read (even one with a familiar prefix) is no
  longer refused. In exchange a new test fails if a shipped manifest names
  a variable the service does not read.

## 10.4 Formats that must parse forever

- **Fuzz targets**, seeded from `pkg/keys/testdata/vectors.json`:
  envelope `Inspect`/parse (+ open with a fixed key), `DecodeRecoveryKey` and
  encode↔decode round trip, `takeout.ReadManifest`, `state.UnescapeSegment`,
  `cs3.ParseDataServerOrigin`. A short fuzz run in CI (seconds per target);
  long runs nightly.
- **Frozen Take-Out fixture**: a tiny Take-Out written by today's `takeout`
  with a throwaway Recovery Key generated for it, committed under
  `pkg/takeout/testdata/` and allowlisted by path in `.gitleaks.toml` with a
  reason. A test opens it with `decrypt`. Every future format version adds
  its own frozen fixture; none is ever deleted. This is what catches a kopia
  upgrade that cannot read an old repository.
- Same for the TW credential blob versions (frozen sealed blob + key).

**Outcome (10.4, #76).** All three done. Where it differs from the plan:

- **Seven fuzz targets**: `FuzzEnvelope` (Inspect, `CheckRecoveryEnvelope`
  and open with fixed keys taken from the vectors, plus an RK seed with tiny
  Argon2id costs so mutations reach the AEAD; inputs asking for more than
  256 KiB / 2 passes are inspected but not opened), `FuzzDecodeRecoveryKey`,
  `FuzzRecoveryKeyRoundTrip`, `FuzzReadManifest` (on an unexported
  `parseManifest`, so no file is written per input), `FuzzEscapeSegment` and
  `FuzzUnescapeSegment`, `FuzzParseDataServerOrigin` (also applies the
  origin and checks only scheme and host change). Besides "no panic" each
  states its parser's promises: errors are the package's coarse sentinels, a
  Recovery Key error carries nothing of the input, a parsed envelope header
  re-serialises to the same bytes, an accepted manifest names nothing outside
  its directory.
- **Two findings, fixed:** `state.UnescapeSegment` took `%+1`, `% 1` and
  `%0X0` as escapes (it used `fmt.Sscanf`), so two keys could name one
  record. It now takes exactly two hex digits *and* only the canonical form
  `EscapeSegment` writes: lowercase hex (`%2e`), an escaped safe byte (`%61`)
  and a raw unsafe byte are refused too, since each also gave one record a
  second key. `ReadManifest` accepted a
  blob id of `.` (and would have taken `../x`, which `Verify` reads as a
  file); it now refuses blob ids that are not flat names, and a manifest
  version below 1. The crashers are committed under `testdata/fuzz/`.
- **`make fuzz`** finds the targets by name (`FUZZTIME`, default 10 s each)
  and runs all of them even after one fails, listing the failures at the
  end. CI runs it on every PR (job `fuzz (short)`), `fuzz.yml` nightly at
  5 min each. `make fuzz-crashers` copies only the new failing inputs (the
  untracked files under `testdata/fuzz/`) to `dist/fuzz-crashers/`, which
  is what both upload. The nightly run opens one `fuzz`-labelled issue only
  when such an input exists; a run that fails for another reason (setup,
  timeout) stays red without an issue.
- **Frozen Take-Out** `pkg/takeout/testdata/takeout-v1/` (21 KB of
  repository) plus `takeout-v1.json` (Recovery Key display form, Space id,
  two snapshots newest first with every file's size, SHA-256 and mtime, and
  the Go/kopia/format versions that wrote it). Two snapshots so listing and
  choosing an older one are pinned too; an empty file and a non-ASCII path
  are in it. The test (`pkg/takeout/decrypt/frozen_test.go`) copies it to a
  temporary directory first, runs `Verify`, decodes the key, lists and
  restores both snapshots. The generator is a test behind `-freeze <name>`
  that refuses to overwrite. `frozenTakeOuts` is append-only and a test
  fails when a listed fixture is missing or a fixture is not listed; another
  fails when no fixture has today's Take-Out format: manifest version,
  envelope version *and* the kopia repository format (format and index
  version, hash, encryption, ECC, splitter), read by opening each fixture's
  repository and a freshly written one.
- **Frozen credentials** `pkg/targets/testdata/sealed-credentials-v1.json`:
  throwaway TW key and both payload shapes written so far (backup pair only,
  backup + maintenance), opened by a test; same append-only list, generator
  and "current version covered" test.
- **gitleaks:** both fixtures allowlisted by exact name pattern
  (`takeout-v<N>/`, `takeout-v<N>.json`, `sealed-credentials-v<N>.json`)
  with the reason; the rest of `testdata/` (fuzz corpora included) is
  scanned as usual.
- *Confirmed (owner, 2026-10-12):* a new fixture is required per Take-Out
  **format** (manifest version, envelope version, or kopia's on-disk
  repository format), not per kopia release: the existing one is what
  proves a kopia upgrade still reads old repositories, and a kopia that
  starts writing a new repository format gets a fixture of its own. *Also
  confirmed:* the manifest and state-key tightening above (no `takeout` ever
  wrote a version below 1 or a blob id with a separator; `EscapeSegment`
  has only ever written the canonical form), and the nightly fuzz run
  opening an issue.
- **For 10.7:** moving `pkg/targets` to `internal/` moves its fixture; the
  `.gitleaks.toml` path and the doc references above follow it.

## 10.5 CLI

- `version` subcommand / `-version` flag on all three binaries (value injected
  in Phase 11; this phase adds the plumbing and a `dev` default).
- `-h` exits 0; usage errors exit 2; runtime errors exit 1. Documented.
- `backupd help` lists the subcommands.
- `takeout -insecure` renamed to `-plain-http` (old flag kept with a
  deprecation warning for one MINOR, policy §2).
- `decrypt` and `takeout` take `io.Reader`/`io.Writer`, so their list,
  extract and decrypt paths become unit-testable; coverage target ≥ 80 %.

**Outcome (10.5, #78).** All five done. Where it differs from the plan:

- **Version plumbing in `internal/buildinfo`**, the package 11.2 names:
  `Version`, `Commit`, `Date` set with `-ldflags -X`, falling back to what
  the toolchain recorded (`go install …@vX` module version, `vcs.revision`,
  `vcs.time`, `vcs.modified`), `dev` otherwise. `-version` prints
  `<program> <version> (<commit>, <go>, <os/arch>)`. Nothing stamps it yet
  (11.2); a test builds `decrypt` with `-X` and runs it, so the path is
  proven, and it reads the package path from the type so 10.7's rename
  cannot leave it stale. `backupd` also logs `starting backupd` with the
  version before its configuration line (11.2 lists that line too; it was
  one statement).
- **Exit status in `internal/cli`**: 0 success and help, 1 failure, 2 wrong
  usage, the same for all three; documented in each `-h` and in README
  ("Versions, help and exit status"). One `cli.Parse` for every flag set:
  help goes to **stdout** (it is the output asked for), parse errors and
  usage to stderr. *Confirmed (owner, 2026-10-12):* what counts as usage (2) is
  anything decidable from the command line alone: unknown flag or command,
  a missing required flag (`-in`, `-out`, `-bucket`, `-space`), a **stray
  positional argument** (silently ignored by `takeout` and `decrypt`
  before), and `rotate-*` without `-service-stopped`. Everything from the
  environment (missing S3 credentials, an invalid configuration) is a
  failure (1).
- **`backupd`**: `help`/`-h`/`--help` list the commands, `help <command>`
  shows its flags; `version`/`-version`/`--version`. A command's flags are
  now read **before** the configuration, so help, version and usage errors
  work with an empty or broken environment; an unknown command no longer
  needs a valid configuration to be refused. Usage errors are plain text on
  stderr, command failures stay JSON log lines.
- **`-plain-http`**, with `-insecure` removed outright rather than
  deprecated (the plan said one MINOR of warning): nothing has been
  released, so the deprecation would protect no one (policy §1, 0.x).
  `-insecure` is now an unknown flag (exit 2). *Confirmed (owner,
  2026-10-12),* together with policy §2's new flag rules from 1.0.0: `decrypt`
  flags are permanent, `takeout`/`backupd` flags get one MINOR of
  deprecation. README and the E2E helper use the new name.
- **Coverage** (unit, `go test -cover`): `cmd/decrypt` 92.8 %, `cmd/takeout`
  97.6 %. `decrypt`'s tests drive the whole program over the frozen
  Take-Out (verify, list, restore newest and a chosen snapshot, wrong key,
  mistyped key, unknown snapshot, bad `-envelope`); `takeout`'s copy a
  bucket laid out from the same fixture through an injected target and
  `Verify` the result. The Recovery Key reader takes any `io.Reader` and
  uses the no-echo prompt only when it is a terminal.

## 10.6 Slim `decrypt`

Break `pkg/takeout/decrypt`'s dependency on the S3 side: the read-only kopia
open of a filesystem repository and the envelope unwrap are all it needs.
Target: no AWS SDK, no Azure, no Prometheus in `go list -deps ./cmd/decrypt`,
asserted by a dependency-graph test like the existing one for `takeout`.

**Outcome (10.6, #80).** No AWS SDK, no S3 client, no S3 storage driver in
`decrypt`; **Azure and Prometheus stay**. Where it differs from the plan:

- **Azure and Prometheus cannot go without replacing kopia's repository
  reader.** They are not pulled in by a storage driver but by kopia's core:
  `kopia/repo/blob` (every storage) imports the Azure SDK's blob error types,
  and `kopia/repo` imports `kopia/internal/metrics`, which is Prometheus
  (gRPC comes the same way, through `kopia/internal/grpcapi`). Opening a
  repository at all needs both packages. *Confirmed by the owner
  (2026-10-12):* accept them, rather than patch or fork kopia; hand-rolling
  the reader is ruled out by AGENTS.md. The test therefore forbids kopia's
  storage *drivers* (S3, Azure, GCS, B2, gdrive, SFTP, WebDAV, rclone) but
  not the two modules. The re-check on each kopia upgrade is a test: the
  third-party modules `decrypt` is built from are pinned (without
  versions), so an upgrade that adds or drops one fails until the list is
  updated deliberately.
- **Two packages split off**, names *confirmed by the owner (2026-10-12)*:
  - `pkg/snapshot/s3repo` — the S3 `StorageOpener` (was
    `snapshot.S3Opener`, now `s3repo.Opener`, with its
    `ErrMissingCredentials`). `pkg/snapshot` keeps the engine, the
    filesystem openers and the key layout, which decrypt uses.
  - `pkg/takeout/remote` — `Extract` and envelope publishing (`Publisher`,
    `S3Publisher`, `PublishTo`), the target-facing half. `pkg/takeout` keeps
    the format: manifest, layout, sentinels, and `Verify`, which both CLIs
    run. Its tests use the frozen v1 Take-Out instead of extracting one.
  Both are on the move-to-`internal/` list of 10.7; decision #26's public
  set (`pkg/keys`, `pkg/takeout`, `pkg/takeout/decrypt`) is unchanged.
- **Three tests in `cmd/decrypt`:** one forbids the target-facing import
  paths above (with a control that `takeout` still links them, so a rename
  cannot make it vacuous); one pins the exact list of this module's
  packages `decrypt` is built from, so a new one is a reviewed decision
  rather than the side effect of an import further down; one pins the
  third-party modules (above). The `go list -deps` helpers (`PackageDeps`,
  `ModuleDeps`) live in `internal/testutil`, and `cmd/takeout`'s audit uses
  them too.
- **For 10.7:** `decrypt` links `pkg/state` only because `pkg/keys`
  declares `NewStateStore(state.Store, …)` (`pkg/keys/state.go`). `pkg/keys`
  stays public (#26) and `pkg/state` moves to `internal/`, which would leave
  a public function taking a type no importer can name. Move the
  state-store adapter out of `pkg/keys` (e.g. `internal/keystore`) as part
  of 10.7; `ownPackages` in `cmd/decrypt/deps_test.go` then loses
  `pkg/state`. *(Done in 10.7, see its outcome.)*
- **Effect:** third-party and module packages in `go list -deps` 451 → 326;
  stripped linux/amd64 binary 21.8 MB → 19.1 MB.

## 10.7 Module path and layout (decision #26)

1. `go.mod`: `module github.com/knoppiks/opencloud-backup-plugin`. Update
   imports, `.golangci.yml`, and the package strings in
   `cmd/takeout/main_test.go`.
2. Move every package to `internal/` **except** the reference
   implementations of the long-term formats:
   - `pkg/keys` — envelope, Recovery Key, TW blob
   - `pkg/takeout` — Take-Out layout and manifest
   - `pkg/takeout/decrypt` — the offline decrypt library
3. `internal/testutil` stays.
4. Add `toolchain go1.26.x` (current patch) to `go.mod` (G3); bump CI and
   Dockerfile to the same; Renovate keeps them aligned from Phase 11.
5. Update `phase-1-scaffolding.md`'s layout block and AGENTS.md
   ("all logic in `/pkg/*`" becomes "`/internal/*`, with `/pkg` reserved for
   the format reference implementations").
6. `spikes/`: delete. Their findings are in `phase-0-findings.md`, and the
   kopia spike starts a Garage container on every integration run for
   nothing. (`git` keeps them.)
7. Verify: `go install github.com/knoppiks/opencloud-backup-plugin/cmd/decrypt@<branch>`
   works from a clean `GOPATH`.

**Outcome (10.7, #82).** All seven done. Where it differs from the plan:

- **Moved:** every former `pkg/` package except the three format packages,
  with its tests and `testdata/`: `api`, `backup`, `cs3`, `cs3state`,
  `instance`, `jobs`, `notify`, `objstore`, `ocversion` (with
  `versions.yaml`), `restore`, `rotate`, `scheduler`, `snapshot` (+
  `s3repo`), `spacecfg`, `state` (+ `statetest`), `targets`, and
  `pkg/takeout/remote` → `internal/takeout/remote`. Path references in the
  Makefile, the workflows, the fixture scripts, `.gitleaks.toml`,
  `.gitattributes`, README, web comments and the planning docs that describe
  the current layout follow. Older phase docs and the reviews keep the paths
  they were written against.
- **The frozen credentials file is untouched**, including its `_comment`,
  which still says `go test ./pkg/targets …`: it is a frozen fixture, never
  edited (10.4). The generator's flag is unchanged, only the package path is.
  `.gitleaks.toml` allowlists both its new and its old path, since the
  history scan still meets it under `pkg/targets/`.
- **The public packages name no internal type.** Two exported APIs did
  after the move, which an importer outside the module could not use:
  - `keys.NewStateStore(state.Store, …)`: the durable key store adapts the
    service's storage and is not part of the key format, so it moved to
    `internal/keystore` (`keystore.New`, `*keystore.Store`). What it shared
    with the in-memory store became two small methods on existing public
    types, `WrappedDK.Clone()` and `SpaceKeys.Status()`. The Store contract
    test moved with it and still runs both implementations.
  - `takeout.Manifest.Blobs []snapshot.BlobRef`: the blob record is part of
    the manifest's JSON, so it is now `takeout.BlobRef`. `internal/snapshot`
    keeps its own untagged `BlobRef` (it cannot import `pkg/takeout`, which
    imports it for `VerifyRepoDir`); `Verify` and `remote.Extract` convert.
    The manifest's bytes are unchanged; the frozen Take-Out still verifies.

  `internal/publicapi` holds a test that walks every exported identifier
  under `pkg/` (signatures, exported fields and methods, element and type
  arguments) through the compiler's export data and fails on any type
  declared under `internal/`; a control asserts it does find the ones
  `internal/takeout/remote` uses.
- **`cmd/decrypt`'s `ownPackages`** is now the full module paths, sorted,
  and has seven packages: `internal/state` is gone with the key store.
- **Toolchain:** `toolchain go1.26.9` (the current 1.26 patch; first written as 1.26.8,
  bumped in review); CI, the
  nightly fuzz and canary workflows and the Dockerfile's build stage (by
  digest) name the same. `go 1.26` stays the language version.
  `internal/buildinfo/toolchain_test.go` fails when the five places disagree,
  until Renovate (Phase 11) keeps them aligned.
- **`spikes/` deleted** with its `.golangci.yml`, `.dockerignore` and
  `.gitignore` entries.
- README's decrypt section names `go install …/cmd/decrypt@latest` next to
  `make decrypt-release`.
- **Verified** after pushing: `go install …/cmd/decrypt@<branch>` with an
  empty `GOPATH` and module cache built a working binary (`-version`).

## 10.8 Lint and test plumbing

- `.golangci.yml` adds: errorlint, contextcheck, gocritic, gosec (G115
  tuned), noctx, exhaustive (`default-signifies-exhaustive`), sloglint
  (snake_case, kv-only), forcetypeassert, nilerr, usestdlibvars, unparam,
  errname, gocognit (~40, with `buildService` split to get under it).
- **depguard / forbidigo encode the trust boundaries**:
  - nothing under `cmd/takeout` or the admin API imports
    `pkg/takeout/decrypt` or calls the unwrap APIs;
  - nothing on the `decrypt` path imports AWS/S3/network packages;
  - no `fmt.Print*`, `os.Exit`, `log.*` under `internal/`.
- revive `exported` on for `pkg/` (the public format packages).
- `-race` on the integration jobs.
- Go coverage with `-coverpkg=./...`, reported in the job summary; no gate
  yet (gate in Phase 14 once the number is stable).
- `go mod tidy -diff` and `go mod verify` in CI.
- One JSON-decode helper for the API (size limit, `DisallowUnknownFields`,
  no trailing data) replacing the seven copies; `Cache-Control: no-store` and
  `X-Content-Type-Options: nosniff` on every API response.

Split into three PRs (owner, 2026-10-12), in this order: C (API decode
helper and headers), A (linters and trust boundaries), B (CI plumbing).

**Outcome (10.8 C, #90).** Done. Where it differs from the plan:

- **Eight copies, not seven:** setup, recovery-key rotation, PUT and PATCH
  backup config, PUT schedule, restore, the admin target body (create,
  check, update share one) and admin grants. `decodeJSONBody` in
  `internal/api/decode.go` replaces them; each endpoint keeps its own limit.
  An oversized body is **413 `payload_too_large`**, anything else 400
  "malformed request body"; neither echoes the input (owner).
- **Tests:** the helper's table (unknown field at depth, second value,
  trailing garbage, empty, over the limit by value and by trailing bytes,
  no echo); every body route driven through the full handler with each
  refusal, the message telling the helper's answer from the handler's own
  400; a test that the route table lists every POST/PUT/PATCH route that
  takes a body; a source scan that nothing but `decode.go` reads `r.Body`.
  Three tests that asserted an extra field was *ignored* (a client-chosen
  target id, a grant naming another target, a restore destination) now
  assert it is refused and nothing happens.
- **Headers on every response of `backupd` (owner):**
  `api.WithResponseHeaders` is outermost on `Server.Handler`, so a
  recovered panic's 500 carries them, and wraps `mountBasePath`'s mux, whose
  own 404 for a path outside the prefix is not the API's.
- **Grants (owner):** a grants body without the list, or with `null`, is
  refused; only an explicit `[]` revokes the target from everyone. `{}` used
  to do that.
- **Web (owner):** `payload_too_large` is a known code with its own title
  and advice (German included); a 413 without the envelope maps to it too,
  since an ingress may refuse the body first. Not "may have landed".
- **Not done here:** requiring `Content-Type: application/json` (owner: own
  PR, #92).

**Outcome (10.8, unit test speed, #94).** Not in the plan; added by the
owner before A, because every later PR waits on the unit job.

- Measured: the unit job took ~8 min, of which `go test -race` 390 s; lint
  9 s (cached). The time was kopia's scrypt key derivation on every test
  repository, slowed further by the race detector. With kopia's `testing`
  tag `internal/snapshot` went from 44 s to 4 s locally without `-race`, and
  the whole `make test` (with `-race`) runs in under 40 s on 16 cores.
- `make test` sets the tag; CI's `unit tests (race)` runs `make test`.
  `build / vet / lint` is its own job. The integration jobs are unchanged
  and keep the release derivation.
- `pkg/takeout/decrypt`: `TestFreezeTakeOut` refuses under the tag;
  `TestFrozenTakeOutsCoverTheReleaseKeyDerivation` (built only without it)
  fails when no fixture's `kopia.repository.f` uses kopia's release
  default.
- Not done: `t.Parallel()` in the kopia-heavy packages; not needed at this
  speed.
- The first CI run hit a flaky test unrelated to the tag:
  `TestReadRecoveryKeyToleratesUserTypingAndRejectsTypos` changed one
  character of a random Recovery Key and expected the 8-bit checksum to
  catch it, which fails one time in 256. It and
  `TestRecoveryKeyChecksumCatchesTypos` now do what the web tests already
  did: draw until a caught typo is found, or assert that nearly all typos
  are caught and none decodes to the mistyped key.

**Outcome (10.8 A, #84).** Done; started from the overnight WIP's
`.golangci.yml` (tag `wip/10.8-lint`, not rebased: it predated the 10.4–10.7
squashes). 57 findings fixed; `golangci-lint run ./...` is clean.

- **Linters** as planned. gocognit 40: `buildService` (50) split into
  `authOptions`, `buildCustody` and `buildPipelines` (`cmd/backupd/wiring.go`),
  order and conditions unchanged. gosec runs with defaults; G115 kept (see
  decisions.md). `buildStateStore` takes the caller's context.
- **Trust boundaries:** depguard `decrypt-is-offline-only` (only
  `cmd/decrypt` links `pkg/takeout/decrypt`), `decrypt-path-has-no-network`
  (the packages `cmd/decrypt` is built from may not import `net`, gRPC,
  CS3, S3 clients, kopia's network drivers or the target-facing packages),
  and, beyond the plan, `test-support-in-tests-only`. forbidigo: no
  `keys.UnwrapRK`/`UnwrapSRW`, `SRWWrapper.UnwrapSRW`, `TWSealer.Open` or
  `targets.CredSealer.Open` under `internal/api`, `internal/takeout` or
  `cmd/takeout`; no `fmt.Print*`, `os.Exit`, `log.*` under `internal/`.
  Each rule was checked by adding a violating file and seeing the build
  fail; there is no permanent test of the lint config itself.
- **revive `exported`** on `pkg/` only: two undocumented constants in
  `pkg/takeout` fixed.
- **Renames:** error types `Err*` → `*Error` (decisions.md).
- **`-race` on the integration jobs** moved to B (#93) with the rest of the
  CI plumbing.

## Exit criteria

- [x] F1–F7 fixed, each with a test that fails on the old code.
      (F1, F2, F4–F7 done in 10.1 (#70); F3 in 10.2 (#72).)
- [x] Every 5xx leaves exactly one server-side log line with a request id;
      `noleak` tests cover the new lines. (10.2, #72)
- [x] Configuration read and validated once; environment reference
      generated from it. (10.3, #74: declared per component, composed per
      binary; machinery in `internal/config`.)
- [x] Fuzz targets run in CI; frozen Take-Out and TW-blob fixtures opened by
      tests; gitleaks clean. (10.4, #76)
- [x] Every binary reports its version; exit status 0/1/2 documented;
      `-insecure` renamed `-plain-http`; `decrypt` and `takeout`
      unit coverage ≥ 80 %. (10.5, #78)
- [x] `decrypt` dependency test passes (no S3/AWS/Azure).
      (10.6, #80: no S3 client or storage driver; the Azure SDK and
      Prometheus modules stay because kopia's core imports them.)
- [x] Module renamed; only `pkg/keys`, `pkg/takeout`, `pkg/takeout/decrypt`
      public; `go install …/cmd/decrypt` works; AGENTS.md and phase-1 updated.
      (10.7, #82)
- [ ] Extended lint set clean; depguard boundaries in place; integration
      tests under `-race`.
