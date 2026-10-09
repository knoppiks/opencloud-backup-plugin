# Phase 13 — Documentation Site

**Goal:** documentation a household can install, use and recover from without
reading source code or a planning folder — published, versioned with the
releases, and generated from the code wherever the code is the truth.

**Depends on:** Phase 11 (versions to publish against), Phase 12 (the install
paths to document). Can run in parallel with Phase 14.
**Findings:** review-2026-10.md D1–D3, W7. **Decision:** #28.

## 13.1 Tooling (decision #28)

- **VitePress**, English only, source in `docs/` at the repository root, its
  own pnpm package (not a workspace with `web/` — the extension's toolchain is
  pinned to OpenCloud's and must not move for the docs' sake).
- **GitHub Pages** via Actions (`actions/deploy-pages`), from a `gh-pages`
  branch that accumulates versions:
  - `/next/` — built from `main` on every merge;
  - `/vX.Y/` — built from each release tag, never rebuilt afterwards except
    for fixes cherry-picked to that line;
  - `/` — redirects to the newest release;
  - a version switcher in the nav, from a `versions.json` the workflow
    maintains.
  VitePress has no built-in versioning; this is the usual pattern and keeps it
  a static site.
- Checks in CI: build, dead links (internal and external, `lychee`), and the
  generated pages are current (13.3).
- Custom domain optional (owner's call); default
  `knoppiks.github.io/opencloud-backup-plugin`.

## 13.2 Information architecture

**Start here**
- What it is, who it is for, what it is not (no sync, not zero-knowledge —
  said on the first page, as decisions.md's product framing does).
- How it works (the README diagram, expanded).
- Security model: a public, readable version of the trust/key and threat
  model. `decisions.md` stays canonical; the page links back and a review
  step in the release checklist confirms they agree.

**For everyone in the household** (screenshots from `screens.e2e.ts`, which
already renders every page in light and dark)
- Turn on backups for a Space.
- Your Recovery Key: what it is, where to keep it, how to check it.
- Restore files.
- Replace the Recovery Key.
- Who may do what (roles table).
- **When the server is gone:** get `decrypt` (download + verify, from the
  release), open a Take-Out, use `recovery.ocbke`.

**For the OpenCloud admin**
- Backup destinations: add, check, grant, delete.
- What the admin can and cannot see (decision #15), in plain words.

**For the operator**
- Install: Docker Compose (primary), Kubernetes/Kustomize, Helm notes.
- Preconditions (TLS, one origin, one instance, memory work dir, state Space).
- Configuration reference (generated).
- Upgrading the plugin; upgrading OpenCloud (compatibility matrix, canary,
  what to check first).
- Rotating server keys; Take-Out runbook.
- Protecting backups from the credential that writes them (Tier 3).
- Monitoring (Phase 14 metrics, log lines worth alerting on).
- Troubleshooting by symptom ("Endpoint not reachable", setup loops in the
  browser → CSP, "another instance is running", stale backups, 425s).
- Uninstall (what to keep: the bucket, the Recovery Keys, `decrypt`).

**Reference**
- CLI reference: `backupd` subcommands, `takeout`, `decrypt`.
- HTTP API (13.4).
- Formats: key envelope, Recovery Key encoding, Take-Out layout — public
  specs derived from `key-envelope-format.md`, so a third party could write
  an independent decrypter. That is the strongest form of "you are not locked
  in".
- Compatibility matrix (generated from `versions.yaml`).
- Changelog (from `CHANGELOG.md`).

**Development**
- Architecture (package map after Phase 10's layout), testing (unit,
  Garage, OpenCloud fixture, E2E), release process, contributing (links
  `CONTRIBUTING.md`).

## 13.3 Generated, not written

Wherever prose would repeat code, the code generates it and CI fails when the
page is stale:

| Page | Source |
|---|---|
| Configuration reference | Phase 10's config declarations (`docs/reference/environment-*.md`, `make generate`) |
| CLI reference | the binaries' flag sets (`-h` output captured) |
| HTTP API | the OpenAPI spec (13.4) |
| Compatibility matrix | `pkg/ocversion/versions.yaml` + release notes |
| Screenshots | `web/e2e/screens.e2e.ts` artifacts |

## 13.4 OpenAPI

- Hand-written `api/openapi.yaml` (OpenAPI 3.1) for `/api/v1`, user and admin
  routes, error envelope and codes.
- **Contract test (Go):** every route registered in `pkg/api` (internal after
  Phase 10) is in the spec and vice versa; API tests validate response bodies
  against the spec (`kin-openapi`).
- **TS types generated** from the spec (`openapi-typescript`) replace the
  hand-mirrored `web/src/api/types.ts` and the hand-kept error-code list. A Go
  DTO rename then breaks the web typecheck instead of the E2E.
- Rendered on the docs site (static, no "try it out" — every call needs a
  token and some carry key material).

## 13.5 Truth pass and slimming

- README becomes an entry point: what, why, status, install link, docs link,
  license. Everything else moves to the site.
- Fix the stale statements in review D2 (README status, decisions #12 status,
  Path B contract wording, phase-1 exit criteria).
- In-app help: each Backup Vault page links to its docs page **for the
  bundle's own version** (`/vX.Y/…`, `/next/…` for dev builds); the
  Recovery Key page and the key-file text link to "get `decrypt`".
- `.agents/plan/` stays the design record; the site never links into it for
  user-facing facts.

## Exit criteria

- [ ] Site live on GitHub Pages with `/next/` and the newest release; version
      switcher works.
- [ ] Every section in 13.2 exists; generated pages are checked in CI.
- [ ] OpenAPI spec + contract test; web types generated from it.
- [ ] README slimmed; D2 statements fixed.
- [ ] A person who has never seen the project installs it with the compose
      add-on from the docs alone (owner or a volunteer; recorded).
- [ ] Every Backup Vault page has a working help link.
