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

## 10.2 Diagnosability (the minimum; metrics are Phase 14)

- `api.Server` gets a logger. Every 5xx logs the cause server-side with a
  request id; the client still gets the safe message. Never key material,
  never credentials (existing `noleak` tests extended to the new log lines).
- Middleware: request id (also returned as a response header so a user can
  quote it), access log at INFO without query strings, panic recovery.
- `http.Server.ErrorLog` bridged to slog.
- `keys.Store` takes a `context.Context` (G5).
- Consistent snake_case log keys (lint-enforced in 10.8).

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

- [ ] F1–F7 fixed, each with a test that fails on the old code.
- [ ] Every 5xx leaves exactly one server-side log line with a request id;
      `noleak` tests cover the new lines.
- [ ] One config struct; environment reference generated from it.
- [ ] Fuzz targets run in CI; frozen Take-Out and TW-blob fixtures opened by
      tests; gitleaks clean.
- [ ] `decrypt` dependency test passes (no S3/AWS/Azure).
- [ ] Module renamed; only `pkg/keys`, `pkg/takeout`, `pkg/takeout/decrypt`
      public; `go install …/cmd/decrypt` works; AGENTS.md and phase-1 updated.
- [ ] Extended lint set clean; depguard boundaries in place; integration
      tests under `-race`.
