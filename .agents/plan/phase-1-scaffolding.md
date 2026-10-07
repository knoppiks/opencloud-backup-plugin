# Phase 1 — Scaffolding

**Goal:** repository structure, CI, and deploy skeleton so every later phase
lands in a tested, buildable frame. Can start in parallel with Phase 0 (module
layout does not depend on spike outcomes; the `/internal/snapshot` internals do).

## Deliverables

1. **Go module layout** — module `github.com/knoppiks/opencloud-backup-plugin`
   (decision #26, amended in phase 10.7)

   ```
   /cmd/backupd/              # main service (API + worker + scheduler)
   /cmd/takeout/              # admin Take-Out extractor CLI   (Phase 5)
   /cmd/decrypt/              # user-side standalone decrypt CLI (Phase 5)

   # Public: the reference implementations of the long-term formats.
   /pkg/keys/                 # envelope, Recovery Key, TW blob; DK/RK/SRW key service
   /pkg/takeout/              # Path A: Take-Out format (manifest, layout, verify)
   /pkg/takeout/decrypt/      # Path A: the user-side offline decrypt (own package
                              # on purpose — see its doc comment)

   # Private to the module: everything else.
   /internal/api/             # HTTP handlers, DTOs (stdlib mux)
   /internal/cs3/             # CS3 gateway client wrapper (interface + impl)
   /internal/rotate/          # SRW/TW key rotation (driven by the backupd CLI)
   /internal/targets/         # admin-managed S3 targets, grants, TW cred sealing
   /internal/spacecfg/        # per-Space backup config (target binding, retention)
   /internal/snapshot/        # kopia wrapper (repo-per-space lifecycle)
   /internal/snapshot/s3repo/ # kopia's S3 storage for it (kept out of decrypt, 10.6)
   /internal/objstore/        # object boundary for what sits beside a repository
   /internal/backup/          # run orchestration (CS3 -> snapshot -> target), prune
   /internal/restore/         # Path B: snapshot -> CS3, into Restore/<ts>/
   /internal/takeout/remote/  # Path A: admin extract + envelope publishing (S3 side)
   /internal/scheduler/       # per-space cron scheduling, prune cadence
   /internal/jobs/            # job/state store + per-Space run lock
   /internal/state/           # durable document store (interface + memory impl)
   /internal/cs3state/        # state store backed by an OpenCloud Space
   /internal/instance/        # single-instance guard
   /internal/notify/          # member and operator notifications
   /internal/ocversion/       # supported OpenCloud versions (pins, window, monitor)
   /internal/config/          # the environment, parsed and validated (10.3)
   /internal/buildinfo/       # version reporting (10.5)
   /internal/cli/             # shared flag parsing and exit status (10.5)
   /internal/testutil/        # Garage fixture, clock, dependency audits
   ```

   Until phase 10.7 every package lived under `/pkg`. `/pkg` is now reserved
   for the formats a Take-Out must be readable with for years and that others
   may implement against; everything else is free to change and therefore not
   importable from outside the module. A new public package is a decision.

   `/internal/targets` was added in Phase 2 (decisions.md #12–#15);
   `/internal/spacecfg` and `/internal/backup` in Phase 4; `/internal/restore`
   and `/pkg/takeout` in Phase 5; `/internal/state`, `/internal/cs3state`,
   `/internal/jobs` and `/internal/notify` in Phase 6; `/internal/rotate` and
   `/internal/instance` during the September 2026 remediation.
   `/internal/spacecfg` is deliberately separate from `/internal/targets`:
   targets are admin-owned, the binding is user-owned.

   `/cmd` binaries stay thin; all logic in packages, wired via constructor
   injection (testability rule).

2. **Interfaces at boundaries** (defined now, implemented later):
   - `cs3.SpaceReader` (list spaces, walk, open file stream)
   - `keys.Store` + `keys.Wrapper`
   - `snapshot.Engine` (Snapshot / RestoreAll / RestoreFile / Prune)
   - `jobs.Store`
   - `Clock` (scheduler tests)

3. **CI (GitHub Actions)**
   - `go build ./...`, `go vet`, `golangci-lint`, `go test -race ./...`.
   - Integration job: Garage fixture (from Phase 0) behind a build tag
     (`-tags integration`).
   - Static binary build (`CGO_ENABLED=0`) as artifact — success metric 1.

4. **K8s manifests (skeleton)** under `/deploy/`:
   - Deployment for `backupd`.
   - Secret: SRW- and TW-wrapping keys (placeholders, generation documented).
     Target S3 credentials are **not** here — they are admin-managed and stored
     TW-wrapped in the app (decisions.md #12/#14).
   - ConfigMap: optional bootstrap/default target metadata (non-secret only;
     `BOOTSTRAP_ENABLE` gates first-start seeding).
   - No ingress/auth details yet (Phase 2).

5. **Dev environment**
   - `docker-compose.dev.yml`: Garage + (once Spike 3 lands) test OpenCloud.
   - `Makefile` or `Taskfile`: `build`, `test`, `test-integration`, `lint`,
     `dev-up`, `dev-down`.

## Exit criteria

- [ ] `go build ./...` and `go test ./...` green in CI on a clean clone.
- [ ] Integration test skeleton runs against ephemeral Garage in CI.
- [ ] Static binary artifact produced.
- [ ] `kubectl apply --dry-run=client` clean on manifests.

## Notes

- Pin toolchain in `go.mod` (`go 1.26`, `toolchain go1.26.8` since 10.7; CI
  and the Dockerfile name the same patch, a test holds them together); pin
  Garage image tag once chosen in Phase 0.
- Add `.gitignore` (spike leftovers, `/tmp`, IDE dirs — `.idea/` exists).
- No business logic in this phase; empty interfaces + wiring + one trivial test
  per package to keep CI honest.
