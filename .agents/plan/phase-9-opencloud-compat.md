# Phase 9 — OpenCloud 8.x and the Compatibility Matrix

**Goal:** make the supported OpenCloud range a tested fact rather than a
README sentence, and bring it up to date: add the Production line (7.2.x),
adopt 8.x, and make the next upstream release something CI notices before a
household does.

**Why first:** OpenCloud 8.0.0, 8.0.1 and 8.1.0 are out; the owner's
deployment is kept current by Renovate and will move to them. CS3 is not a
public upstream interface (decisions.md, 7.5.0 amendment), and the last minor
upgrade (7.5.0) broke every read and write. Everything later in this plan —
the release, the packaging, the docs' compatibility table — states a range,
and the range has to be true first.

**Depends on:** Phase 8 (landed). **Policy:**
[compatibility-policy.md](compatibility-policy.md) (decisions.md #25).
**Findings:** review-2026-10.md O1–O7.

## Deliverables

### 9.1 One source for the OpenCloud pin

Today the version lives in ~8 places (review O5). Introduce one file, e.g.
`test/fixtures/opencloud/versions.yaml`, listing every matrix leg as
`{name, channel, image, tag, digest}`. Read it from:

- the fixture compose file (via env interpolation set by `up.sh`),
- `up.sh` (which today repeats the image and is not checked against compose),
- the CI matrix (generated from the file with a small job, or `fromJSON`),
- a Go test that asserts the README/docs "supported" sentence matches the
  file, so the prose cannot drift again.

Renovate (Phase 11) updates digests in this one file.

**Outcome (PR A, #63):** the file is `pkg/ocversion/versions.yaml`, not under
`test/fixtures/`: `go:embed` cannot reach outside a package's directory, and
9.2 needs the window compiled into `backupd`. Legs are named by role
(`production`, `oldest-rolling`, `newest-rolling`) plus a `canary` entry; up.sh
selects one with `OC_LEG` (default `newest-rolling`) and hands the image to
compose through `./.env`. `TestNoOtherOpenCloudPin` greps every committable
file outside `.agents/` for an image reference or a pinned digest;
`TestREADMESupportedSentence` keeps the README equal to the file. The CI
matrix and the canary share one reusable workflow, `opencloud-suite.yml`. PR A
lands with the one leg it can prove (7.5.0). The 7.3.0 leg was in it and its
E2E failed: below 7.5 the gateway returns the public data gateway URL with a
transfer token, and `CS3_DATA_SERVER_URL` rewrites that host to the
storage-users data server, which answers 500 "invalid upload path" to the
first state write. The Go integration tests never set the variable, so they
passed. So 9.4's question is answered: the setting is **not** harmless below
7.5. PR B (#64) fixes that, adds `production` and `oldest-rolling`, and moves
`newest-rolling` to 8.1.0.

### 9.2 Runtime OpenCloud version detection (spike, then code)

**Spike:** how does an external service learn OpenCloud's version, reliably,
on 7.2–8.1? Candidates to measure, not assume: `/status.php`
(`productversion`), the OCS capabilities endpoint
(`core.status.productversion`), a graph field, a gateway RPC. Pick the one that
needs no extra credential and is stable across the window. Record in
`phase-0-findings.md`.

**Code:** `backupd` reads the version at startup and on the slow readiness
cadence, logs it, exposes it in `GET /api/v1/version` (Phase 11 adds that
route; this phase adds the field), and logs WARN when it is outside the
window compiled into the build. **Never refuses** (policy §3). The admin view
shows the same notice.

### 9.3 Adopt OpenCloud 8.1.0

Upgrade the fixture, then **measure** each item below against it and record
the result — the 7.5.0 amendment is the template:

1. Full OpenCloud fixture suite (`make test-opencloud`) and browser E2E.
2. Default CSP — still without `'wasm-unsafe-eval'`? Re-derive
   `test/fixtures/opencloud/csp.yaml` from 8.1's default, not 7.3's
   (opencloud-compose's `csp.yaml` is one reference point).
3. Admin app-role id and `appRoleAssignments` shape.
4. Space grants `Opaque` map shape (`TestIntegration_SpaceGrantsShape`) and the
   permission sets copied into `pkg/cs3/role.go` from 7.3.0.
5. Data-server behaviour (`InitiateFileDownload/Upload` address, token).
6. "Too early" (425) behaviour during post-processing.
7. Overwrite and revision semantics in the state Space (R1 / R10 measurements).
8. Graph `/me` identity and `memberOf` (decisions, "Amendments before the first
   real deployment").
9. Web: `@opencloud-eu/extension-sdk`, `web-pkg`, `web-client` to 8.x. The
   extension's imports (`useAuthStore`, `useAbility`, `useModals`,
   `useRouter`, `useSpacesStore`, `useCapabilityStore`, `useClientService`,
   `createLocationSpaces`, `createFileRouteOptions`) are not on 8.0's removal
   list; confirm by typecheck. Re-check the design-system `oc-*` stubs in
   `src/test/host.ts` against the host's 8.x design system.
10. `go-cs3apis` pin: compare with the version OpenCloud 8.1 vendors; bump if
    it moved.

**Decision to take with the owner during the phase:** can one web bundle
serve 7.2 *and* 8.x hosts? Shared modules are host singletons
(`import: false`), so probably yes; the matrix proves it. If not, the policy
needs a "bundle per OpenCloud major" clause and Phase 11 ships two web images.

### 9.4 Add the Production line (7.2.x)

Run the same measurement list against 7.2.4. 7.2 predates 7.5's data-server
change: confirm `CS3_DATA_SERVER_URL` is harmless when set (or document that
it must be unset). Fix what breaks, or — if 7.2 needs a code path the rest of
the window does not — bring that to the owner before writing it.

### 9.5 The CI matrix

`integration-opencloud` and `e2e` become matrix jobs over the legs in
`versions.yaml` (policy §3): Production, oldest Rolling, newest Rolling. All
blocking. Matrix job names must be stable so the ruleset (Phase 11) can
require them.

### 9.6 The canary

A scheduled (nightly) workflow runs the OpenCloud suites and E2E against
`opencloud-rolling:latest` by **tag**. Non-blocking. On failure it opens (or
updates) one issue titled after the upstream version it failed on. This is
the early warning for review O4 — CS3 changes nobody announces.

### 9.7 Documentation

- `compatibility-policy.md` "window today" table updated with results.
- `decisions.md`: amendment "Moving to OpenCloud 8.x" (what was measured,
  what changed), like the 7.5.0 one.
- README "Supported OpenCloud versions" sentence generated from / tested
  against `versions.yaml` (Phase 13 moves it to the docs site).
- Upgrade notes for operators: 8.0 requires a search reindex on the OpenCloud
  side (unrelated to this plugin, worth one line so nobody blames it).

## Exit criteria

- [ ] One pin file; nothing else names an OpenCloud image or digest
      (enforced by a test or a grep in CI).
- [ ] CI matrix green on 7.2.x, 7.3.0 (oldest Rolling in window) and 8.1.0.
- [ ] Canary workflow exists and has opened a test issue once (dry run).
- [ ] `backupd` logs the OpenCloud version and warns outside the window
      (unit test with a fake version source; integration test against the
      fixture).
- [ ] Web SDK on 8.x; typecheck, unit tests and E2E green on every leg.
- [ ] decisions.md amendment and compatibility table updated.

## Out of scope

- Switching the fixture to opencloud-compose — Phase 12 (it changes what is
  tested, so it lands with the add-on it tests).
- LTS (4.0.x) — policy §3.
