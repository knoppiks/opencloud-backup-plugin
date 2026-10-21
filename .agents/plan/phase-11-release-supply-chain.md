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
what the docs tell users to run. Walked end to end on `main` (rc.4; see
the outcome).

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
- **Walked with `v0.1.0-rc.4` (2026-10-15).** rc.3 stopped before building
  anything: `sigstore/cosign-installer` has no major tags, `@v4` did not
  resolve (#106, pinned `@v4.1.2`). rc.4: every job green; draft with 22
  assets, pre-release; `verify-release.sh -t v0.1.0-rc.4 -a` OK (signature,
  checksums, provenance of 20 assets); `decrypt -version` names the tag's
  commit; both images signed, provenance OK, four per-platform SBOM
  attestations OK; no version tag before publishing. Publishing a
  hand-made release on the same tag (no assets) was refused by
  `release-publish.yml` before tagging anything; publishing the bot's draft
  tagged `0.1.0-rc.4` on both signed digests, and no `0.1` (pre-release).
  Verified from the owner's machine, not a clean one: the clean-machine
  walk is 11.9 step 3.
- **Image verification needs cosign v3 (#108):** the images are signed with
  cosign v3, whose signatures an older cosign does not find ("no signatures
  found"). README and the release notes now say so; `COSIGN_VERSION` is
  pinned in both release workflows, and a Go test ties the pinned major to
  what the docs say.

## 11.4 Tags and the release workflow

The workflow parts (main-reachability guard, re-run without moving a tag,
`concurrency`, the `release` environment question: no) moved into 11.3
(#104). Left:

- Tag ruleset: `v*` tags may not be moved or deleted; creation restricted to
  the owner.
- `deploy/` image references updated by the release (or by Renovate after it)
  so a manifest never names a tag that does not exist.

### Outcome (11.4, #110)

- **Two tag rulesets** (applied 2026-10-16 via the API; a bypass covers a
  whole ruleset, so one could not both let the owner create and forbid him
  to move):
  - "Release tags are immutable" (25254674): `refs/tags/v*`, `update`,
    `deletion`, `non_fast_forward`, **no bypass**, the owner included.
    A wrong tag is fixed with the next version (as rc.3 → rc.4), or by
    switching the ruleset off on purpose.
  - "Release tags are created by the owner" (25254676): `refs/tags/v*`,
    `creation`, bypass: repository admin role.
  Not probed with a test tag: a protected probe could never be removed.
- **`deploy/` names published images only.** No bot writes to `main`: the
  release does not bump `deploy/`. Instead `scripts/check-deploy-images.sh`
  (CI job `k8s manifests`, PRs and nightly; `make deploy-images`) fails on
  any image of this project in `deploy/` that is not a version tag
  (`X.Y.Z[-rc.N]`, optional `@sha256:`), is not in the registry, or whose
  pinned digest differs. Tested offline against a fake registry
  (`test/release/deploy-images-test.sh`, 11 cases). `deploy/` moved from the
  never-published `0.1.0` to `0.1.0-rc.4`; the bump after a release is
  manual until Renovate (11.6) does it.

## 11.5 Changelog and release notes

**Superseded plan:** `.github/release.yml` categories by PR label, a
curated `CHANGELOG.md`, a PR template. Replaced by the owner (2026-10-17,
#112): the changelog is the GitHub release notes, built from **issues**.

### Outcome (11.5, #112)

- **Labels** (GitHub only, no file in the repo): `kind/` exactly one
  (Bug, Feature, Enhancement, Refactoring, Question, Epic), `aspect/` any
  number (16, incl. project-specific `Web UI`, `Recovery`, `Release`,
  `OpenCloud Compatibility`), `hint/Breaking`, `hint/Operator Action`,
  `hint/Good first issue`, `status/` (Duplicate, Invalid, Wontfix, Help
  wanted, Information Needed). Old labels renamed, so issues kept them.
- **Milestones = phases** (1–14; 1–10 closed), `Roadmap` for unplanned
  work. All 54 closed issues backfilled (kind, aspects, phase); #68 (canary
  dry run) has none and is `status/Invalid`.
- **Which issues a release ships** (`scripts/release-issues.sh`): closed as
  completed and not `status/Invalid|Duplicate|Wontfix`, and named by work
  merged since the previous *final* tag (all history for the first): a
  merged PR's closing references, a merged PR's branch name (`N-slug`), or
  a closing keyword in a commit of the range. All three are needed for the
  history so far (measured on rc.4: closing references alone found 39 of
  51; ten issues were closed by a commit, several by "Refs" + manual close).
  An rc's range also starts at the previous final tag.
- **Notes** (`scripts/release-notes.sh`): Breaking changes, Operators must
  act, then by kind (Highlights = Epic, Features, Enhancements, Bug fixes,
  Refactoring, Other), then the OpenCloud legs tested (version, image by
  digest, from `versions.yaml` at the tag), images, downloads, verification.
  The owner edits the draft before publishing.
- **Checks** (`scripts/check-issues.sh`): `pr-issue.yml` refuses a PR
  that closes no issue, or whose issue lacks exactly one `kind/` or a
  milestone (bots exempt; re-run after fixing the issue). The release guard
  refuses a final tag shipping an issue without exactly one `kind/`; an rc
  only warns.
- Tests: `test/release/release-issues-test.sh` (throwaway repo + fake `gh`:
  ranges across an rc, three sources, not-planned/invalid/open left out,
  other repositories left out), notes and check cases in
  `release-scripts-test.sh`; both mutation-checked.
- AGENTS.md: the issue/label/milestone rules and the branch-name rule.
- **Ruleset:** `closes a ready issue` is required on `main` (ruleset
  "Default", 2026-10-17, after its first run on #113).
- **Walked with `v0.1.0-rc.5` (2026-10-17, #114):** guard found 54 issues,
  all with one `kind/`; notes generated. **The draft's body was empty**, as
  rc.4's had been: GoReleaser reads `--release-notes` inside its changelog
  step, and `changelog.disable: true` skipped that step (reproduced
  locally: "skipped generating changelog" vs. "loading file … read 30
  bytes"). Fixed with `disable: false` (a given notes file is used as is,
  no changelog of GoReleaser's own); `make release` now runs
  `scripts/check-release-notes.sh`, which fails unless the draft carries
  the notes. rc.6 walks the notes again.

## 11.6 Dependency updates and scanning

- **Renovate** (`renovate.json`): gomod, npm/pnpm, Dockerfile digests,
  compose images (fixture and add-on), this project's own images in
  `deploy/` after a release (11.4 leaves that bump manual), GitHub Actions (pinned by SHA),
  `versions.yaml` OpenCloud digests (custom manager), Go toolchain line,
  `GO_VERSION`/`NODE_VERSION` in `ci.yml` (custom regex manager). Grouped
  weekly; security updates immediately. **OpenCloud digest bumps are never
  automerged** — they are the CS3 risk (compatibility-policy §4).
- Enable Dependabot *alerts* (not updates) for the security advisory feed.
- `govulncheck` job (source mode) — blocking on reachable vulns.
- `pnpm audit --prod` / osv-scanner for the web — blocking on high.
- Trivy (or grype) on both built images — blocking on fixable critical.

### Outcome (11.6, #116)

Owner's choices (2026-10-19): hosted Mend app; Trivy; the web audit judged
against the bundle; no automerge; actions pinned by Renovate.

- **`renovate.json`:** `config:recommended` +
  `helpers:pinGitHubActionDigests`; weekly (Monday before 07:00 Berlin),
  vulnerability alerts (GitHub + OSV) at any time; lock file maintenance;
  `gomodTidy`; `ignorePaths` narrowed so `test/` (Caddy in the fixture) is
  seen. Groups: Go toolchain (go.mod `toolchain`, both Dockerfiles, four
  workflows: one branch, measured), Node, Go modules (kopia and CS3 APIs
  apart), web dependencies, GitHub Actions, build and scan tools, base
  images, deploy manifests, OpenCloud SDKs. The `go` directive is not
  bumped.
- **Custom managers:** `# renovate: datasource=… depName=…` above each
  pinned tool version (workflows, Makefile, scripts), `# renovate:
  datasource=docker` above each scanner image; Garage in
  `internal/testutil/garage.go`; the OpenCloud legs in `versions.yaml`
  (tag + digest). Per policy §3: `oldest` gets digest updates only,
  `previous-major` no major, a future `production` patches only, `newest`
  anything; never automerged, with a note to update README and the policy.
  Kubernetes manager over `deploy/` (this project's image, after a
  release).
- **Checks on the config:** `make renovate-config` (pinned validator,
  strict; CI job `renovate config`); `TestEveryPinnedToolVersionIsAnnotated
  ForRenovate` fails on a pin without an annotation (mutation-checked);
  `TestGarageIsTheSameEverywhere`. A local `renovate --platform=local
  --dry-run=full` produced 24 branches; the leg rules held back
  `oldest`/`previous-major` moves.
- **`make vulncheck`** (govulncheck v1.9.0, `./cmd/...`, five platforms; CI
  `known vulnerabilities (Go)`). First run: 7 reachable in x/net, grpc,
  x/text; fixed here (x/net v0.60.0, grpc v1.83.1, x/text v0.42.0, x/crypto
  v0.57.0 with them). `./...` would also scan `internal/testutil`, which
  ships nowhere.
- **`make web-audit`** (osv-scanner v2.7.0 image; CI `known vulnerabilities
  (web bundle)`): the license plugin also writes the bundled package list
  (`node_modules/.cache/backup-vault/bundled-packages.json`, 11 packages,
  3 generated-only); `web/build/vulnerability-audit.ts` decides. First run:
  49 findings, all in tooling (axios, adm-zip, happy-dom, ws, …), 0
  blocking.
- **`make image-scan`** (Trivy 0.76.0 image; in CI's `image` job, and in
  `release.yml` on both platform digests before signing) and **`make
  image-scan-probe`**: a stand-in image claiming a vulnerable zlib must
  fail, its patched twin pass. First run: no critical; 7 HIGH in Go
  modules of `backupd`/`takeout`, fixed by the bumps above.
- **Dependabot alerts** enabled (2026-10-19, API).
- Ignore files when needed: `web/osv-scanner.toml`, `.trivyignore.yaml`
  (reason, expiry). Neither exists yet.
- **For the owner:** install the Mend Renovate app on the repository
  after merging (the config is read from `main`; no onboarding PR). Add
  the three new jobs to the ruleset (or leave it to 11.7). Renovate's
  pull requests close no issue, so they are not in the release notes;
  `pr-issue.yml` exempts bots.

### Follow-up (11.6, #133): catch-up and coarser groups

Owner (2026-10-20): the ~20 updates waiting for Renovate's first schedule
were applied by hand in one PR, and the groups made coarser, because
merging and rebasing them one by one cost more than the updates.

- **Applied:** Go toolchain 1.27.3 (go.mod, both Dockerfiles by digest,
  four workflows), Node 24.22.0, every direct Go module but go-cs3apis
  (CS3) and kopia (current), the web extension's minor/patch updates and
  pnpm 10.34.7, a regenerated lockfile, golangci-lint v2.15.0, kubeconform
  v0.8.0, syft v1.55.0, cosign v3.1.3, renovate 44.171.7, Garage v2.4.1,
  Caddy 2.11, and every action at its latest release, majors included
  (setup-node v7, docker/* v4 and build-push v7: runtime bumps, no input
  we use removed). The `tools/` modules take the fixes of Renovate's
  security PRs #118, #127–132; gitleaks needs `mholt/archives` v0.1.5 for
  rardecode v2.2.0, which #128 alone would have broken.
- **Caddy 2.11 broke the fixture's sign-in** (first CI run of this PR):
  its `reverse_proxy` now sets `Host` to the upstream's address for an
  HTTPS upstream (caddyserver/caddy#7454), and OpenCloud's IdP refuses a
  logon for any host but `localhost:9200` (400). The Caddyfile passes
  `header_up Host {hostport}` (`{host}` drops the port and is refused
  too). Every fixture test and E2E spec failed on every leg; local unit
  and Garage integration runs could not see it.
- **Left to Renovate, one PR each:** TypeScript 7, ESLint 10, Vitest 5,
  happy-dom 21, pnpm 12 (each may need code changes) and the go-cs3apis
  digest (CS3).
- **`tools/` builds with the project's toolchain** (`GOTOOLCHAIN` from
  go.mod's `toolchain`; the tool modules name none, so there is one pin):
  golangci-lint refuses a module that targets a newer Go than it was built
  with. A toolchain bump rebuilds the tools.
- **Groups now:** `pipeline` (actions, all update types; tools in
  workflows, Makefile, scripts and `tools/`), `container images` (base
  images, Garage, Caddy), `Go modules` (main module), `web dependencies`,
  plus Go toolchain, Node, OpenCloud SDKs, deploy manifests and every
  major outside actions apart; lock file maintenance monthly. Measured
  with a local `--dry-run=full` against `main` before this PR: 13 branches
  instead of 20.

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

### Outcome (11.7, #125)

Owner's choices (2026-10-20): pin in this PR; Go tools in `tools/`,
other linters as images; GitHub + verified + allow-list; every job
required except `fuzz (short)` (flaky, "context deadline exceeded" at the
end of `-fuzztime`, four times since 2026-10-12; no issue for now);
zizmor fails on any finding.

- **Actions:** all 77 references `@<sha> # vX.Y.Z` (the commit of the
  major tag in use; no version changed). `TestEveryActionIsPinnedToACommit`
  fails on anything else (mutation-checked). Renovate keeps them.
- **Go tools:** `tools/<name>/go.mod` with a `tool` directive each
  (golangci-lint v2.12.2, gitleaks v8.30.1, kubeconform v0.6.7, actionlint
  v1.7.12, govulncheck v1.9.0), so go.sum pins them and their dependencies
  stay out of the project's graph and audits. `make tools` builds them into
  `dist/tools/` (rebuilt when go.mod/go.sum change); `lint`, `lint-probe`,
  `k8s-validate`, `vulncheck`, `secret-scan`, `pipeline-lint` use them, and
  CI calls those targets: no curl downloads and no `golangci-lint-action`
  left. Renovate: gomod manager, grouped with the build and scan tools.
  go-licenses stays `go install …@v2.0.1` inside the script (sumdb
  verified; it also runs in the Dockerfiles).
- **`make pipeline-lint`** (CI `pipeline lint (workflows, scripts,
  Dockerfiles)`): actionlint with shellcheck on `run:` blocks
  (`scripts/shellcheck.sh` runs the pinned image), zizmor 1.31.1 (online
  audits with the job token), shellcheck v0.11.0 on every tracked `*.sh`,
  hadolint v2.15.1 on both Dockerfiles. Images by digest, Renovate-annotated.
  First run: 117 zizmor findings; fixed: pins, `persist-credentials: false`
  on every checkout (nothing pushes with the checkout's token), no Actions
  cache in `release.yml`'s own jobs and in `release-snapshot` on a tag (two
  inline ignores, low confidence, the condition zizmor cannot read). One
  hadolint ignore (consecutive `RUN` in a build stage). zizmor's
  `self-repository` (`uses: $/…`) disabled in `.github/zizmor.yml`:
  actionlint 1.7.12 rejects the syntax.
- **Timeouts** on every job (`TestEveryJobHasATimeLimit`), sized at 2–5×
  the measured run.
- **Caches:** Go module/build cache keyed on the tools' go.sum too;
  Playwright's Chromium by version (`actions/cache` v6.1.0). Not done:
  pnpm store (install takes seconds; setup-node's cache needs pnpm before
  Corepack provides it) and buildx layers (the CI image build is ~2 min and
  would need a cache the release must not read).
- **`make secret-scan`** = `scripts/secret-scan.sh`: history, then a copy
  of the files git would commit (`gitleaks dir .` on the checkout walks
  `node_modules/` for many minutes). Probed with a stand-in AWS key.
  AGENTS.md updated.
- **After merge (owner):** ruleset requires every job but `fuzz (short)`;
  repository settings `sha_pinning_required` and allowed actions
  (GitHub-owned + verified + allow-list).

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

### Outcome (11.8, #137)

Owner's choices (2026-10-21): newest release only; acknowledgement within
14 days; private reporting on; questions as `kind/Question` issues; issue
forms with the kind preset and no blank issues; `* @knoppiks`; the planned
description and topics; strip with `DEBUG=1` to keep symbols; an
`.editorconfig` that matches the formatters, checked.

- **Renovate's stale security PRs** #127–132, #134, #135 closed themselves
  ("autoclosed", 2026-10-21) once #136 had superseded them.
- **`SECURITY.md`:** supported versions (newest release; the OpenCloud
  window by link to the policy and README, not repeated), private
  reporting, what to include and never include, response, scope and out
  of scope per decisions.md's threat model.
- **`CONTRIBUTING.md`:** setup (Go per go.mod, Docker, Node/Corepack, yq),
  the make targets, which tests a change must run (the fixture and E2E for
  anything OpenCloud or the browser sees), AGENTS.md's hard constraints in
  plain words, the gitleaks rule, labels and milestones, branch and commit
  style, `Closes #N`.
- **`.github/`:** `CODEOWNERS`; issue forms `bug.yml` (plugin and OpenCloud
  version required), `feature.yml`, `question.yml`, `config.yml` (no blank
  issues, contact link to private reporting); `pull_request_template.md`
  ending in `Closes #`. Forms validated against SchemaStore's schemas
  (`check-jsonschema`, one-off).
- **`.editorconfig`** + `make editorconfig` (editorconfig-checker v3.11.3
  in `tools/`, CI step in `build / vet / lint`, no new job). Shell scripts
  are tabs except the OpenCloud fixture's (two spaces, as written); Caddyfile
  tabs as `caddy fmt`; frozen fixtures and lock files untouched.
- **`.dockerignore`:** `web/`, `**/node_modules/`, `**/dist/` (`spikes/` is
  gone since 10.7). Service image context measured at 2.5 MB.
- **`make build`** passes `-s -w` unless `DEBUG=1`.
- **Repository settings** (2026-10-21, API): private vulnerability
  reporting on; description; topics `opencloud`, `backup`, `kopia`,
  `self-hosted`, `encryption`. Homepage waits for the docs site (Phase 13).
- **Checks** (`internal/buildinfo/repository_test.go`, each mutation-checked):
  every form sets exactly one valid `kind/`; the bug form requires both
  versions; blank issues off and the private-reporting link present;
  SECURITY.md links it; the PR template ends in `Closes #`; every relative
  link and anchor in SECURITY.md, CONTRIBUTING.md and the PR template
  resolves; `.dockerignore` excludes the three paths.
- **Follow-up (#139): README status.** The note at the top still described
  the project before Phase 8 (admin view "the current phase", nothing run
  in a browser). Rewritten for a newcomer at v0.1.0: what works, what is
  not there yet (packaging, docs site, member mail, Garage deletes), links
  to the supported range and the verification steps rather than repeating
  them. The notifications line now says operators get mail. The link check
  covers README.md too (all its links resolved). The rc.6 draft's notes
  did render (6.4 kB body, 22 assets), closing 11.5's open point; the draft
  stays until 11.9 step 5.

## 11.9 Cut `v0.1.0`

1. All of the above merged; CI green on every required job.
2. Tag `v0.1.0` on `main`; draft release reviewed; published.
   Then bump `deploy/` to `0.1.0` (a PR; the check refuses it before the
   release is published).
3. Verify from a clean machine: download `decrypt`, verify checksum and
   cosign signature, open the frozen Take-Out fixture (Phase 10) with it.
4. Deploy to the owner's OpenCloud by digest from the release notes.
5. Delete the `v0.1.0-rc.*` images or leave them marked pre-release (owner's
   call; tags stay in git, and cannot be deleted under the 11.4 ruleset).
   Only after step 2's `deploy/` bump: the nightly check fails while
   `deploy/` names a deleted image.

## Exit criteria

- [x] `LICENSE`, `THIRD_PARTY_NOTICES`, license check in CI; busybox question
      decided (#99).
- [x] Every artifact reports the same version; mismatch banner tested (unit +
      E2E with a faked server version) (#102).
- [ ] `v0.1.0` published with signed checksums, SBOMs, provenance, five
      `decrypt` builds; verification steps documented and walked once.
- [x] Renovate, govulncheck, web audit, image scan active; actions
      SHA-pinned; every job required (#116, #125, #133; but `fuzz (short)`,
      owner's exception; checked 2026-10-21: 23 required, the rest is fuzz).
- [x] SECURITY.md, CONTRIBUTING.md, templates, CODEOWNERS present (#137).
- [x] Release notes from the issues a release ships, by kind, with the
      OpenCloud versions tested; PR and release checks (#112).
