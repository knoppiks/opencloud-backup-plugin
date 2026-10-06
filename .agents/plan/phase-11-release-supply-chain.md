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

## 11.4 Tags and the release workflow

- Tag ruleset: `v*` tags may not be moved or deleted; creation restricted to
  the owner.
- `release.yml` refuses a tag whose commit is not reachable from `main`
  (fixes R5: rc.2 was tagged on an unmerged branch).
- `workflow_dispatch` input for re-running a release *without* moving a tag.
- `concurrency` on the release workflow; a `release` environment so the
  publish job waits for the owner's approval (optional for a solo
  maintainer — owner to choose).
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

- [ ] `LICENSE`, `THIRD_PARTY_NOTICES`, license check in CI; busybox question
      decided.
- [ ] Every artifact reports the same version; mismatch banner tested (unit +
      E2E with a faked server version).
- [ ] `v0.1.0` published with signed checksums, SBOMs, provenance, five
      `decrypt` builds; verification steps documented and walked once.
- [ ] Renovate, govulncheck, web audit, image scan active; actions
      SHA-pinned; every job required.
- [ ] SECURITY.md, CONTRIBUTING.md, templates, CODEOWNERS present.
