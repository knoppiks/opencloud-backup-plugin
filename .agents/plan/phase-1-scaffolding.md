# Phase 1 — Scaffolding

**Goal:** repository structure, CI, and deploy skeleton so every later phase
lands in a tested, buildable frame. Can start in parallel with Phase 0 (module
layout does not depend on spike outcomes; the `/pkg/snapshot` internals do).

## Deliverables

1. **Go module layout**

   ```
   /cmd/backupd/        # main service (API + worker + scheduler)
   /cmd/takeout/        # admin Take-Out extractor CLI   (Phase 5)
   /cmd/decrypt/        # user-side standalone decrypt CLI (Phase 5)
   /pkg/api/            # HTTP handlers, DTOs (chi or stdlib mux)
   /pkg/cs3/            # CS3 gateway client wrapper (interface + impl)
   /pkg/keys/           # DK/RK/SRW key service
   /pkg/snapshot/       # kopia wrapper (repo-per-space lifecycle)
   /pkg/s3target/       # Garage/S3 config, capability probe
   /pkg/scheduler/      # per-space cron scheduling
   /pkg/jobs/           # job/state store
   /internal/testutil/  # Garage fixture, fake CS3, clock
   ```

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
   - Secret: SRW-wrapping key (placeholder, generation documented).
   - ConfigMap: S3 target (endpoint, bucket, region `garage`).
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

- Pin toolchain in `go.mod` (currently `go 1.26`); pin Garage image tag once
  chosen in Phase 0.
- Add `.gitignore` (spike leftovers, `/tmp`, IDE dirs — `.idea/` exists).
- No business logic in this phase; empty interfaces + wiring + one trivial test
  per package to keep CI honest.
