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
- **F2.** *Proposed, owner to confirm:* any read error other than not-found
  or malformed **fails the listing** (`Versions.Latest` and `Documents.All`,
  one helper), rather than being collected next to `unreadable`. A failed
  tick is logged and the next tick retries; collecting would have reported a
  blinking backend as "corrupt record". *Also proposed:* the targets store's
  `ListTargets` now returns `ErrUnreadableTargets` when a record cannot be
  decoded, instead of dropping it — its callers are TW rotation (would leave
  that target sealed under the retiring key) and bootstrap (would take a
  store whose only record is corrupt for an empty one), the same reasoning as
  the admin API's existing "unreadable configuration is an error". The
  member-facing `VisibleTargets` still skips it, so one corrupt record does
  not take the target list from every member.
- **F4.** As planned. The fuzz target for `ReadManifest` is 10.4's.
- **F5.** Also applied to kopia's S3 driver (`snapshot.S3Opener`), which has
  the same fallback (env, then IAM/IMDS) — the server-side path the review
  mentions. That check is local to `pkg/snapshot` so the decrypt path does
  not import `objstore` (10.6). The admin connection check reports missing
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
- **Levels, *proposed, owner to confirm*:** 503 is WARN ("not now": a
  dependency not wired or not up yet, shutting down); every other 5xx and
  every panic is ERROR; the health probes log at DEBUG while they pass, since
  the kubelet calls them every few seconds; everything else INFO.
- **Request id, *proposed, owner to confirm*:** always minted by `backupd`
  (16 hex characters from `crypto/rand`), never taken from an incoming
  `X-Request-Id`, so a client cannot choose what the operator's log says.
  Returned as `X-Request-Id` only, as planned; it is not in the JSON error
  body and the web UI does not show it yet (backlog candidate).
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

- One `config` package (under `internal/`), one struct, parsed and validated
  in one place, with defaults declared beside the field. All ~48 variables.
- Strict booleans (`true`/`false` only; anything else refused at startup).
- The struct is the source for the **environment reference** the docs site
  publishes (Phase 13): a `go generate` step writes it, a test fails when it
  is stale.
- Effective-config log line at startup with secrets redacted by type, not by
  name list.
- The `REPLACE_ME` placeholder check reads the struct, so `configPrefixes`
  stops being a hand-kept list.
- Service-account credentials checked in serve mode (today only in
  `provision`).

**Outcome (10.3, #74).** All six done, in `internal/config` from the start
(10.7 does not have to move it). Where it differs from the plan:

- **One struct per binary**: `config.Backupd` (the service and its operator
  commands, 48 variables in ten sections) and `config.Takeout` (its two S3
  credentials). Each field carries `env`, `default` and `doc` tags; `run`
  loads the configuration once and hands it down, so no `os.Getenv` is left
  under `cmd/`.
- **Reference** at `docs/reference/environment.md` (13.1 puts the docs site's
  source under `docs/`). `go generate ./internal/config` (or `make
  generate`) writes it; `TestReferenceIsCurrent` fails when it is stale.
  Defaults are declared as real values (`2`, `365`, `24`, the admin role id,
  the state prefix) and a test holds them equal to the packages' own
  fallbacks.
- **Every problem in one error.** Type errors and cross-field checks are
  collected and reported together, so one restart shows them all. An
  unreplaced placeholder is reported alone, as before, because everything
  after it is a symptom.
- **Strictness, *proposed, owner to confirm*:** booleans are exactly `true`
  or `false` (not `1`, `yes`, `True`); `STATE_BACKEND` refuses anything but
  `memory` (case-insensitive, as before) instead of reading a typo as
  "durable". Both used to be silently accepted. The shipped manifests only
  use `"true"`/`"false"`.
- **Redaction by type, *proposed, owner to confirm*:** `config.Secret`
  holds its value behind an unexported pointer and prints `[redacted]` (or
  nothing when unset) through `String`, `GoString`, `LogValue` and
  `MarshalText`, so even `%d` or reflection over the struct shows no value.
  The rule for what is a Secret: whatever the manifest takes from a
  Kubernetes Secret, which includes `OC_SERVICE_ACCOUNT_ID` and the S3
  access key ids. A test fails if a credential-named variable is declared as
  a plain string.
- **Effective-config line:** `msg=configuration`, one attribute per variable
  under `config`, keyed by the lower-cased variable name, unset ones
  included so defaults are visible. Logged by `serve` before anything can
  fail; the commands do not log it.
- **Service account, *proposed, owner to confirm*:** required whenever
  `CS3_GATEWAY_ADDR` is set, in every mode (service, rotation, provision),
  since every CS3 call is made as it.
- **Moved into config:** the base-path rules, the TLS pair, the OIDC
  prerequisites, the timezone, the wrapping keys' shape and SRW ≠ TW (the
  `_OLD` keys are now shape-checked at load too). **Left with the
  consumer:** `CS3_DATA_SERVER_URL` parsing (`pkg/cs3`; config stays free of
  the gRPC stack `takeout` would then link), "`STATE_SPACE_ID` required" (the
  provisioning command needs it *unset*), the work directory's filesystem,
  and the bootstrap target's completeness (`targets.Bootstrap`).
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

## 10.5 CLI

- `version` subcommand / `-version` flag on all three binaries (value injected
  in Phase 11; this phase adds the plumbing and a `dev` default).
- `-h` exits 0; usage errors exit 2; runtime errors exit 1. Documented.
- `backupd help` lists the subcommands.
- `takeout -insecure` renamed to `-plain-http` (old flag kept with a
  deprecation warning for one MINOR, policy §2).
- `decrypt` and `takeout` take `io.Reader`/`io.Writer`, so their list,
  extract and decrypt paths become unit-testable; coverage target ≥ 80 %.

## 10.6 Slim `decrypt`

Break `pkg/takeout/decrypt`'s dependency on the S3 side: the read-only kopia
open of a filesystem repository and the envelope unwrap are all it needs.
Target: no AWS SDK, no Azure, no Prometheus in `go list -deps ./cmd/decrypt`,
asserted by a dependency-graph test like the existing one for `takeout`.

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

## Exit criteria

- [x] F1–F7 fixed, each with a test that fails on the old code.
      (F1, F2, F4–F7 done in 10.1 (#70); F3 in 10.2 (#72).)
- [x] Every 5xx leaves exactly one server-side log line with a request id;
      `noleak` tests cover the new lines. (10.2, #72)
- [x] One config struct; environment reference generated from it.
      (10.3, #74: one per binary, `internal/config`.)
- [ ] Fuzz targets run in CI; frozen Take-Out and TW-blob fixtures opened by
      tests; gitleaks clean.
- [ ] `decrypt` dependency test passes (no S3/AWS/Azure).
- [ ] Module renamed; only `pkg/keys`, `pkg/takeout`, `pkg/takeout/decrypt`
      public; `go install …/cmd/decrypt` works; AGENTS.md and phase-1 updated.
- [ ] Extended lint set clean; depguard boundaries in place; integration
      tests under `-race`.
