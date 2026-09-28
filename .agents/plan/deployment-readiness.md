# Deployment readiness — first real deployment

Tracked as #40. Split out of it: #38 (identity), #39 (setup race), #41 (8e),
#42 (8f).

**Goal:** run the plugin against the owner's own OpenCloud
(`gitlab.com/knoppiks-home/apps/opencloud`), after 8d and before 8f.

**Depends on:** 8a–8d (landed). 8e is pulled in ahead of the live test.

## Survey (September 2026)

Blockers found between "8d is done" and "it runs somewhere real":

1. **CSP blocks the Recovery Key ceremony.** OpenCloud 7.3.0 sends
   `script-src 'self' 'unsafe-inline'`, without `'wasm-unsafe-eval'` (measured
   on the fixture). `hash-wasm` compiles WebAssembly at runtime, so setup,
   "Check my Recovery Key" and replacement fail in current browsers. Nothing
   had caught this because nothing had run in a real browser (8f).
2. **Nothing is published.** `deploy/deployment-backupd.yaml` names
   `ghcr.io/knoppiks/opencloud-backupd:latest`, which does not exist. The web
   bundle exists only as a CI artifact.
3. **No Service or Ingress manifest.** `/backup/api/` must reach backupd on
   OpenCloud's own origin. OpenCloud's proxy cannot append a route (8c
   decision 3), so it has to be an ingress.
4. **No admin view (8e).** Targets come only from `BOOTSTRAP_*` or `curl`.
5. **Stale docs.** The README says there is no UI and no target API. Deploy
   comments say targets are "edited via the UI".
6. **Setup race** (8d.4 finding): two concurrent `POST /backup/setup` calls can
   both pass `assertNotConfigured` and orphan keys (#17).

## Decisions (settled with the owner)

1. **CSP: document the override, no code change.** Operators add
   `'wasm-unsafe-eval'` to `script-src` via OpenCloud's CSP file
   (`PROXY_CSP_CONFIG_FILE_LOCATION`). The fixture carries the same override so
   it is tested, not claimed. The rejected alternative, a pure-JS Argon2id, has
   no CSP cost but is markedly slower on the devices a family uses.
2. **Images go to ghcr on a `v*` tag**, from GitHub Actions:
   `ghcr.io/knoppiks/opencloud-backupd` and
   `ghcr.io/knoppiks/opencloud-backup-vault-web`.
3. **The web bundle ships as an OCI image for an initContainer**, matching
   OpenCloud's own `web-extensions` images: the image carries the app under a
   fixed path and the operator's init step copies it into the apps directory.
4. **Deployment artifacts:** a Service and an Ingress example under `deploy/`,
   and a deployment runbook in the README.
5. **The setup race is fixed** with the same per-Space lock rotation uses (#39).
6. **8e lands before the live test** (#41). Its own decisions are taken with the
   owner before code, as for every sub-phase.
7. **After the plugin work, changes are prepared in the owner's OpenCloud
   repository**: gateway port, CSP, initContainer, backupd workload, ingress
   path, secrets. Staged, never committed.
8. **Test target: `lu-s3`** (the off-site endpoint over the tunnel).

## Outcome (plugin side)

- **CSP, measured.** A page served with 7.3.0's policy fails hash-wasm's
  Argon2id in headless Chrome with a `CompileError` naming `script-src`. The
  same page with `'wasm-unsafe-eval'` added passes. The fixture now carries
  `test/fixtures/opencloud/csp.yaml` (the measured default plus that one
  source) through `PROXY_CSP_CONFIG_FILE_LOCATION`. The resulting header is the
  default plus that source and nothing else. `install-webapp.sh` fails when
  `script-src` lacks it (verified by removing the line). Firefox was not
  checked.
- **Images.** `web/Dockerfile` builds the bundle into busybox at
  `/usr/share/nginx/html/backup-vault/`, with a default command that copies it
  to `/extensions/backup-vault`. Both Dockerfiles build on `$BUILDPLATFORM`, and
  the Go one cross-compiles. An arm64 build was checked locally. All four base
  digests are multi-arch indexes. `make web-image` builds it.
- **Release.** `.github/workflows/release.yml` runs the whole of `ci.yml` (now
  `workflow_call`-able) against the tag, then pushes both images for amd64 and
  arm64, tagged `X.Y.Z` and `X.Y`, with no `latest`. The CI `image` job builds
  both Dockerfiles. The deployment manifest names `:0.1.0`, which exists once
  `v0.1.0` is tagged.
- **Manifests.** `deploy/service-backupd.yaml` and
  `deploy/ingress-backupd.yaml` (path `/backup/api`, same host, prefix not
  stripped). The deployment's issuer and base URL were
  `https://cloud.example.org`, which passed the startup check. They are now
  `REPLACE_ME` values, so an unedited manifest is refused.
- **Setup race.** Fixed in #39; see the 8d.4 findings in `phase-8-web-ui.md`.
- **README.** The status paragraph is corrected. There are new sections
  "installing Backup Vault" (initContainer, CSP) and "step by step".

- **Identity (found while planning 8e, #38).** A real browser token's `sub` is
  not the OpenCloud user id, and membership was checked against `sub`, so every
  user saw no Spaces. Fixed; see decisions.md, "Amendments before the first
  real deployment". This alone would have ended the live test at the overview.
- **Flake, not fixed (already tracked as #37):** `TestIntegration_CS3State/round_trip` failed once with
  a `cs3 download: unexpected status 425` (Too Early: the upload was still in
  OpenCloud's post-processing), then passed three times in a row. The state
  store reads straight after a write. A retry on 425 in the CS3 download path
  would close it, and the same window can hit the service in production.

## Known risks for the live test

- **Ingress path vs. app route.** The extension's app id is `backup-vault` and
  OpenCloud Web routes apps under `/<appId>`. Traefik's `PathPrefix` is a
  string prefix, so an ingress path `/backup` would also capture
  `/backup-vault/...`. The ingress path must be `/backup/api`.
- **Gateway reachability.** OpenCloud binds its gateway gRPC to loopback by
  default. `OC_GATEWAY_GRPC_ADDR=0.0.0.0:9142` plus a Service port are needed.
  Whether reva then hands out other loopback addresses (registry via NATS) to
  an external client is unverified; the fixture did not show it, but the
  fixture is not a pod network.
- **Plaintext gRPC.** `CS3_GATEWAY_ADDR` has no TLS option; it must stay on the
  cluster network.
- **First containerised run.** The fixture runs backupd on the host. The
  deployment's read-only rootfs, memory-backed `/work` and 512Mi limit have
  not met a real Space.
- **R10** (unbounded revision growth in the state Space) is still open.
