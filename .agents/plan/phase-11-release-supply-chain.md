# Phase 11 — Release, Versioning & Supply Chain

**Goal:** a first real release, `v0.1.0`, that a stranger can trust: licensed,
versioned in every artifact, with `decrypt` downloadable and verifiable, built
by a pipeline whose inputs are pinned, scanned and kept current.

**Depends on:** Phase 10 (module path and layout settled before anything is
released under them). **Policy:** [compatibility-policy.md](compatibility-policy.md).
**Findings:** review-2026-10.md R1–R10, D3. **Decisions:** #23 (license),
#24 (versioning).

## 11.1 License (decision #23: Apache-2.0, gated on an audit)

Measured on 2026-10-06 over the modules linked into the three binaries:
40 Apache-2.0, 17 BSD, 16 MIT, 1 ISC; no copyleft. Web runtime dependencies
(`@noble/ciphers`, `@noble/hashes`, `hash-wasm`) are MIT. That is the
starting point, not the audit.

1. **Go:** `go-licenses` (pinned) over `./cmd/...`: report + a CI check that
   fails on any license outside an allow-list (Apache-2.0, BSD-2/3, MIT, ISC,
   MPL-2.0 *file-level* only after owner review).
2. **Web:** `pnpm licenses list --prod` over the bundle's dependencies, same
   allow-list. The host-provided modules (`vue`, `@opencloud-eu/*`) are not
   bundled (`import: false`) and are out of scope; verify that claim against
   the built bundle.
3. **NOTICE obligations:** Apache-2.0 dependencies that ship a `NOTICE` file
   (kopia among the candidates) must have it reproduced when distributing
   binaries. Generate `THIRD_PARTY_NOTICES` from the audit and ship it in the
   images (`/licenses/`) and the release archives.
4. **Container base images:** distroless static carries Debian package
   licenses (fine, recorded by the SBOM). The **web image is busybox, GPL-2.0**:
   distributing it obliges offering its source. **DECISION NEEDED:** keep
   busybox and link the corresponding source (as every busybox-based image
   does), or replace the copy step with a tiny static Go binary on
   distroless — which also removes a shell from the image. Recommendation:
   the Go binary; it is ~30 lines and removes the question.
5. Add `LICENSE` (Apache-2.0 text), SPDX headers policy (proposal: no
   per-file headers; `LICENSE` + `NOTICE` at root suffices for Apache-2.0),
   README license section, `web/package.json` already says Apache-2.0.

### Outcome (11.1, #99)

- `LICENSE` (Apache-2.0) at the root; README "License" section; no per-file
  headers, no NOTICE of our own.
- **Go:** `scripts/third-party-notices.sh` runs go-licenses v2.0.1 per
  platform (`check` against the allow-list, `save` for license and NOTICE
  files), adds the Go standard library's license, and writes the union.
  `make licenses` runs it over `./cmd/...` on all five `decrypt` platforms
  (CI job `third-party licenses`, artifact `third-party-notices`). Each image
  runs it for its own programs and platform. `make license-probe` proves the
  check still refuses a dependency without a license and one under GPL-3.0.
  Measured: 84 libraries, ten NOTICE files (AWS SDK, smithy-go, minio-go,
  Prometheus ×4, grpc, yaml ×2), CC0-1.0 for blake3, and one MPL-2.0 module,
  `go-vss`, in the Windows `decrypt` only (named exception, follow-up to
  remove it).
- **Web:** `pnpm licenses --prod` was the wrong tool: it sees 3 packages, the
  bundle holds 8 (the Module Federation runtime comes from the SDK, a
  devDependency). `web/build/third-party-licenses.ts`, a Vite plugin, traces
  every bundled module to its package and fails the build on a disallowed
  license, a missing license file, an untraceable module or a host-provided
  package; it writes `THIRD_PARTY_NOTICES` into the bundle. No `vue` or
  `@opencloud-eu/web-*` code is bundled: verified on every build now. Found:
  the SDK (AGPL-3.0) stylesheet is compiled into the bundle's CSS (standing
  named exception; the bundle runs inside AGPL-3.0 OpenCloud Web anyway).
- **Busybox decided: replaced.** `cmd/install-bundle` (logic in
  `internal/bundleinstall`) on distroless static; 4.7 MB image, no shell.
  The web image now builds from the repository root (`-f web/Dockerfile .`).
  CI runs the image without arguments and checks the bundle lands.
- Decisions: see decisions.md, "Amendments from Phase 11.1".

## 11.2 Version in every artifact (decision #24)

- Go: `-ldflags "-X …/internal/buildinfo.Version=… Commit=… Date=…"` in
  Makefile, Dockerfile (`ARG VERSION`, `ARG COMMIT`), CI and release.
  Fallback to `runtime/debug.ReadBuildInfo` for `go install` builds.
- `backupd`: startup log line; `backupd version`; `GET /api/v1/version`
  (authenticated, any signed-in user) returning
  `{version, commit, api_level, opencloud: {version, in_window}}`. `/healthz`
  stays content-free (no unauthenticated fingerprinting).
- Web: Vite `define` injects `__APP_VERSION__` and `__API_LEVEL__`; the bundle
  calls `/version` once per session and shows a banner to admins (and a
  calm notice to members) on mismatch. The version is shown in the admin view
  footer.
- `web/package.json` version set by the release, not hand-edited.
- OCI labels on both images (`org.opencontainers.image.version`, `revision`,
  `source`, `licenses`, `title`, `description`) — in the Dockerfiles, not only
  via metadata-action, so local builds carry them too.

### Outcome (11.2, #102)

- **Stamp:** `make stamp` (VERSION = `git describe --tags --match 'v[0-9]*'
  --abbrev=12 --dirty`, COMMIT, DATE = commit time UTC) feeds `make build`,
  `decrypt-release`, `web-build`, `image`, `web-image`; both Dockerfiles take
  `ARG VERSION/COMMIT/DATE` and set the OCI labels themselves. The release
  sets VERSION to the tag.
- **`go install`:** commit and time come from the pseudo-version
  (`buildinfo.pseudoVersionRevision`, table-tested); "commit unknown" only
  for a tagged module version.
- **backupd:** `backupd version` and the startup log line existed (10.5);
  `GET /api/v1/version` now adds `version`, `commit`, `api_level` (1).
- **Web:** `__APP_VERSION__` via Vite `define` from `BACKUP_VAULT_VERSION`;
  `API_LEVEL` as a TS constant cross-checked by a Go test (no `define`
  needed). `version/mismatch.ts` decides, `VersionNotice.vue` shows it on the
  Overview and the destinations page, which also names both versions.
  `web/package.json`'s version is still 0.0.0; the release sets it in 11.3.
- **Checks:** unit specs (mismatch table, once-per-load cache, Overview and
  admin views), E2E `version.e2e.ts` (real answer; faked other release for a
  member and an admin), CI `make image-stamp-check` on the built images.

## 11.3 GitHub Releases

A tag `vX.Y.Z` on `main` produces a **draft** GitHub Release containing:

| Asset | Notes |
|---|---|
| `decrypt_<ver>_{linux_amd64,linux_arm64,darwin_amd64,darwin_arm64,windows_amd64}` archives | the family's tool; stripped, static |
| `takeout_<ver>_linux_{amd64,arm64}` | for operators recovering without the image |
| `SHA256SUMS` + cosign keyless signature/certificate | verification instructions in the release body and docs |
| SBOMs (SPDX JSON) for binaries and both images | via syft |
| compose add-on and Kustomize add-on archives | added by Phase 12 |
| `THIRD_PARTY_NOTICES` | from 11.1 |

The owner reviews the draft and publishes. **Proposal:** GoReleaser (pinned)
for the CLI binaries, archives, checksums, SBOMs and signing — it does all of
it from one config file — while the two images stay on `docker/build-push`
(the web image is a Node build). Rejected: hand-written shell in
`release.yml`; it is what exists today and it has published nothing.

Images: pushed by digest, signed with cosign (keyless, GitHub OIDC), SBOM and
SLSA build-provenance attested (`actions/attest-build-provenance`). Tags
`X.Y.Z`, `X.Y`, no `latest` (unchanged). Pre-releases (`-rc.N`) publish
images but mark the GitHub Release as pre-release.

Macos binaries are unsigned/un-notarised; the docs say how to open them
(Gatekeeper). Notarisation is backlog.

### Plan (11.3, #104; agreed with the owner 2026-10-15)

Choices:

- **Image tags appear on publish.** The tag push builds, pushes *by digest
  only*, signs and attests both images; publishing the draft (`release:
  published`) adds `X.Y.Z` / `X.Y` with `imagetools create`. Reviewing the
  draft then gates the images too. Digests of a draft that is never
  published stay untagged in GHCR; cleaned up by hand.
- **Binary signature:** `cosign sign-blob` (keyless) over `SHA256SUMS` as one
  `SHA256SUMS.sigstore.json` bundle, plus `actions/attest-build-provenance`
  per archive (`gh attestation verify`).
- **Image SBOM/provenance:** BuildKit's own provenance and SBOM off; syft
  SPDX SBOM per image (release asset + cosign attestation), cosign signature
  and `attest-build-provenance` (`push-to-registry`) on the digest.
- **Archives:** tar.gz (zip on Windows) holding the binary, `LICENSE` and the
  `THIRD_PARTY_NOTICES` for that archive's platform only.
- **Web bundle** as an asset: `backup-vault_<ver>.tar.gz`, with its own
  notices. `web/package.json`'s version is set in the build, not committed.
- **One build definition:** `make decrypt-release` and CI's
  `decrypt-cross-build` run `goreleaser build --snapshot`, so every PR builds
  what ships.
- **Folded in from 11.4:** the main-reachability guard (R5), `concurrency`
  per tag, `workflow_dispatch` re-run of an existing tag. The tag ruleset
  and `deploy/` bumps stay in 11.4. **No `release` environment** (solo
  maintainer; draft review already gates).
- GoReleaser's changelog is off; the body is a template with the
  verification steps and image digests. Categorised notes are 11.5.

Checks: CI job `release-snapshot` (`goreleaser check`, snapshot, asset
names, archive contents, `decrypt`/`takeout version` report the stamp,
`scripts/verify-release.sh` over the snapshot's checksums). The script is
what the docs tell users to run. Walked end to end with `v0.1.0-rc.3` on
`main`.

### Outcome (11.3, #104)

- `.goreleaser.yaml` (GoReleaser v2.18.3, syft v1.54.1): `decrypt` ×5,
  `takeout` linux ×2, archives with `LICENSE` + per-platform notices (build
  hook runs `scripts/third-party-notices.sh` per target), SPDX SBOM per
  archive, `SHA256SUMS` over every asset incl. `dist/release-assets/*`,
  cosign bundle, draft, `prerelease: auto`, changelog off.
- Make: `decrypt-release`, `web-archive`, `release-snapshot`, `release`,
  `release-scripts-test`. Archives and the web archive are reproducible
  (measured: two snapshots, identical checksums except SBOMs).
- `release.yml`: `guard` (tag form, on `main`, not yet published) → `ci`
  + `web-bundle` → `images` (by digest, cosign sign, provenance, SBOM per
  platform attested) → `release` (images.txt, notes, `make release`,
  `attest-build-provenance` over `SHA256SUMS`). `concurrency` per tag,
  `workflow_dispatch` on the tag re-runs.
- `release-publish.yml` (on `release: published`): verifies `images.txt`
  through `verify-release.sh -t TAG`, tags the digests
  (`scripts/image-tags.sh`), checks each tag names the signed digest.
- Scripts: `check-release-assets.sh` (names, contents, notices' platform,
  SBOMs, `SHA256SUMS` exactly = assets, `-version` stamp, static),
  `verify-release.sh` (user-facing), `release-notes.sh`, `image-tags.sh`;
  tested by `test/release/release-scripts-test.sh`, which also damages a
  snapshot four ways and expects the check to refuse each.
- CI: `release assets (snapshot)` replaces `decrypt-cross-build`.
- README: "Verifying a release" (cosign, checksums, provenance, images,
  Windows, macOS Gatekeeper); download pointers in the runbook and the
  extension install.
- **Open:** the `v0.1.0-rc.3` walk (tag on `main`, draft reviewed,
  published, tags appear, `verify-release.sh -a` from a clean machine).

## 11.4 Tags and the release workflow

The workflow parts (main-reachability guard, re-run without moving a tag,
`concurrency`, the `release` environment question: no) moved into 11.3
(#104). Left:

- Tag ruleset: `v*` tags may not be moved or deleted; creation restricted to
  the owner.
- `deploy/` image references updated by the release (or by Renovate after it)
  so a manifest never names a tag that does not exist.

## 11.5 Changelog and release notes

Commit subjects are Title Case imperative, not Conventional Commits, so
prefix-parsing tools do not apply. Instead:

- `.github/release.yml` — GitHub's generated release notes, categorised by
  **PR label** (`enhancement`, `bug`, `security`, `breaking`,
  `opencloud-compat`, `dependencies`, `docs`). Labels already exist for most.
- `CHANGELOG.md` (Keep a Changelog format), curated at release time from the
  generated notes: what an operator must do, what a user will notice,
  OpenCloud versions tested (from `versions.yaml`, by digest).
- PR template asks for the label and an "operator action needed?" line.

## 11.6 Dependency updates and scanning

- **Renovate** (`renovate.json`): gomod, npm/pnpm, Dockerfile digests,
  compose images (fixture and add-on), GitHub Actions (pinned by SHA),
  `versions.yaml` OpenCloud digests (custom manager), Go toolchain line,
  `GO_VERSION`/`NODE_VERSION` in `ci.yml` (custom regex manager). Grouped
  weekly; security updates immediately. **OpenCloud digest bumps are never
  automerged** — they are the CS3 risk (compatibility-policy §4).
- Enable Dependabot *alerts* (not updates) for the security advisory feed.
- `govulncheck` job (source mode) — blocking on reachable vulns.
- `pnpm audit --prod` / osv-scanner for the web — blocking on high.
- Trivy (or grype) on both built images — blocking on fixable critical.

## 11.7 CI hardening

- All actions pinned by full SHA (Renovate keeps them current); same majors
  everywhere (`decrypt-cross-build` catches up).
- gitleaks, kubeconform, golangci-lint installed via pinned `go install` or
  checksum-verified download.
- `actionlint` + `zizmor` on workflows; `shellcheck` on fixture scripts;
  `hadolint` on Dockerfiles.
- `timeout-minutes` on every job; pnpm store, Playwright browser and buildx
  layer caches.
- **Ruleset requires every job**, including the Phase 9 matrix legs (stable
  names) and secret-scan. Repository settings: require SHA-pinned actions,
  restrict allowed actions to GitHub + verified creators + an allow-list.
- `make secret-scan` runs both `git` and `dir` modes, as AGENTS.md says.
- Makefile uses the same pinned tool versions as CI (one `tools.mk` or
  `go tool` directives in `go.mod`).

## 11.8 Repository hygiene

- `SECURITY.md`: supported versions (from compatibility-policy), private
  vulnerability reporting enabled on GitHub, response expectations, what is
  in scope (key handling, the ciphertext-only boundary) — and what is not
  (Garage's lack of Object Lock is documented, not a vulnerability).
- `CONTRIBUTING.md`: dev setup (`make dev-up`), tests that must pass, the
  hard constraints from AGENTS.md in human words, commit style, gitleaks rule.
- `CODEOWNERS`, issue templates (bug with version/OpenCloud version fields;
  feature request; question → discussions), PR template, `.editorconfig`.
- Repository description, topics (`opencloud`, `backup`, `kopia`,
  `self-hosted`, `encryption`), homepage → docs site (Phase 13).
- `.dockerignore`: `web/`, `**/node_modules`, `**/dist`, `spikes/` for the
  root image.
- `make build` strips like CI.

## 11.9 Cut `v0.1.0`

1. All of the above merged; CI green on every required job.
2. Tag `v0.1.0` on `main`; draft release reviewed; published.
3. Verify from a clean machine: download `decrypt`, verify checksum and
   cosign signature, open the frozen Take-Out fixture (Phase 10) with it.
4. Deploy to the owner's OpenCloud by digest from the release notes.
5. Delete the `v0.1.0-rc.*` images or leave them marked pre-release (owner's
   call; tags stay in git).

## Exit criteria

- [x] `LICENSE`, `THIRD_PARTY_NOTICES`, license check in CI; busybox question
      decided (#99).
- [x] Every artifact reports the same version; mismatch banner tested (unit +
      E2E with a faked server version) (#102).
- [ ] `v0.1.0` published with signed checksums, SBOMs, provenance, five
      `decrypt` builds; verification steps documented and walked once.
- [ ] Renovate, govulncheck, web audit, image scan active; actions
      SHA-pinned; every job required.
- [ ] SECURITY.md, CONTRIBUTING.md, templates, CODEOWNERS present.
