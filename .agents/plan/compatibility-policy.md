# Compatibility & Versioning Policy

**Status: binding** (decisions.md #24, #25). Like
[key-envelope-format.md](key-envelope-format.md), this is a promise to the
people who install the plugin, not an internal convention. Changing it is a
decision, recorded in `decisions.md`, never a drift.

The policy answers four questions an operator asks before upgrading anything:

1. Which plugin version am I running, and what does the number tell me?
2. Which OpenCloud versions does it work with?
3. Can I upgrade OpenCloud without upgrading the plugin, and the other way round?
4. Will a backup I make today still be readable after both have moved on?

---

## 1. One plugin version

Every artifact of a release carries **the same version**:

| Artifact | Where the version is visible |
|---|---|
| `ghcr.io/knoppiks/opencloud-backupd:X.Y.Z` | image tag, OCI label, `backupd version`, startup log line, `GET /api/v1/version` |
| `ghcr.io/knoppiks/opencloud-backup-vault-web:X.Y.Z` | image tag, OCI label, bundle build constant, shown in the UI |
| `takeout`, `decrypt` binaries | `-version`, GitHub Release asset names |
| compose add-on, Kustomize add-on | pinned image tags inside them, release asset |

`backupd` and the web bundle **must run at the same version**. That has been
the rule since `release.yml` existed; from Phase 11 it is *checked*: the
bundle reads `GET /api/v1/version` and shows a banner when the server's
version differs from its own (decisions.md #24).

## 2. Plugin versioning: Semantic Versioning, independent of OpenCloud

The plugin's number does **not** track OpenCloud's. OpenCloud majors arrive
every few months (7.0 in May 2026, 8.0 in September), and tying the two would
spend the plugin's MAJOR on events that break nothing here, leaving no way to
signal a break that is the plugin's own.

While the version is **0.x**, MINOR may break and the rules below are
followed in spirit. **1.0.0** is cut when phases 9–13 have landed: a release
a family can install from documentation alone, on a supported OpenCloud, with
a published `decrypt`.

From 1.0.0:

| Bump | When |
|---|---|
| **MAJOR** | An operator has to act on upgrade (a renamed/removed env var after its deprecation period, a manifest change that is not backward compatible, a state migration with no way back); support for an OpenCloud version is dropped **before** it leaves the support window (§3). |
| **MINOR** | New features; a newly supported OpenCloud version; an OpenCloud version dropped **because it left the window**; a new env var; a deprecation (with warning). |
| **PATCH** | Fixes, dependency updates, documentation. |

**DECISION NEEDED (proposal above):** dropping an OpenCloud version that has
left the support window is MINOR. The window is announced, so the operator
already knows; burning a MAJOR every six months on it would make MAJOR
meaningless. The alternative is to treat every drop as MAJOR.
**Status (2026-10-07, #67): proposed, owner to confirm** — MINOR is applied
as the working rule until then. It binds nothing yet: the plugin is 0.x, and
the only drop so far (7.2.x) happened before any release.

### Things that never break, at any version bump

These have their own version numbers inside the data and are read forever.
No MAJOR bump licenses dropping them:

- **Key envelopes** (`key-envelope-format.md`): every version ever written
  stays readable by `decrypt` and `backupd`.
- **Take-Out manifest**: `decrypt` reads every Take-Out any released
  `takeout` has produced. Pinned by a frozen fixture from Phase 10 on.
- **Recovery Key encoding** (`ocbk1-…`): a key a user saved stays valid.
- **TW-wrapped credential blob** and **state-Space record layouts**: newer
  versions read older layouts (as R1 does with the pre-versioned ones).

### Things with a deprecation period

- **Environment variables**: a renamed variable keeps working for at least one
  MINOR release, logging a warning that names its replacement.
- **HTTP API** (`/api/v1`): the only client is the bundle of the same version,
  so the API is versioned by an **API level** integer in
  `GET /api/v1/version`, not by URL. `/api/v2` is reserved for a change that
  would need two bundles to coexist, which this policy does not foresee.
- **Downgrades** of the plugin are supported within a MINOR line. Across MINOR
  lines a state migration may make them impossible; release notes say so when
  it happens.

## 3. OpenCloud support window

OpenCloud ships three channels (docs.opencloud.eu, "Release Lifecycle"):
**Rolling** every ~3 weeks, **Production** every ~6 months, **LTS** for
paying customers.

**Supported = the current Production line (all its patch releases) plus
every Rolling release published since that Production release was cut.**
(decisions.md #25)

Rationale: Production is what OpenCloud tells production users to run, and a
household that follows that advice must not be excluded. Rolling is what
enthusiasts — including the owner's own deployment — run, and it is where
breakage arrives first. LTS is out of scope: it is a customer offering, and
the families this targets do not have it.

### The window today (2026-10-07)

**Rolling 7.3.0 to 8.1.0.** Measurements per version: phase-9 doc, 9.3
outcome; what changed: decisions.md, "Moving to OpenCloud 8.x".

| Channel | Versions | Status |
|---|---|---|
| Production | 7.2.x (7.2.4) | **excluded** (owner decision): a service account cannot create the state Space there (decisions.md, "Moving to OpenCloud 8.x"); returns with the next Production line |
| Rolling | 7.3.0 | **CI leg `oldest-rolling`**, blocking |
| Rolling | 7.4.0, 7.5.0, 8.0.0, 8.0.1 | in the window, bracketed by the outer legs; 7.5.0 was a CI leg while newest, 7.4.0 and 8.0.x never ran |
| Rolling | 8.1.0 | **CI leg `newest-rolling`**, blocking; the canary (`latest`) resolved to the same digest on 2026-10-07 |

There is no Production leg until the next Production line ships; the
matrix below shows the policy's shape, `versions.yaml` the legs in force.

### When the window moves

The next Production release is announced for **2026-10-26**. When a new
Production line ships:

1. It joins the window immediately (it is a Rolling release already tested).
2. The previous Production line and the Rolling releases older than the new
   Production leave the window.
3. **DECISION NEEDED:** a grace period. Proposal: the previous Production line
   stays supported until the first plugin MINOR released **90 days** after the
   new Production — families upgrade slowly, and a backup plugin that stops
   supporting the OpenCloud version a household runs is a backup that stops.
   **Status (2026-10-07, #67): proposed, owner to confirm** — the 90-day
   grace is the working rule. At the 2026-10-26 switch it has nothing to
   cover: 7.2.x is excluded already, and the grace as proposed covers the
   previous Production line only, so the Rolling releases older than the
   new line (7.3.0 up to it) leave the window at once under step 2.

### What "supported" means, concretely

A version is supported if and only if **CI runs the full OpenCloud fixture
suite and the browser E2E against it** (decisions.md #9's lesson: a claim
not exercised against the real thing is not a claim). The CI matrix
(Phase 9) is:

| Leg | Version | Blocking |
|---|---|---|
| Production | newest patch of the Production line | yes |
| Oldest Rolling | oldest Rolling still in the window | yes |
| Newest Rolling | newest Rolling release | yes |
| Canary | `opencloud-rolling:latest` (by tag, not digest), nightly | **no** — opens an issue |

Intermediate Rolling releases are not run on every PR. They were each run
when they were newest, and the outer legs bracket them. A release's notes list
the exact versions (by digest) it was tested against.

### What the plugin does outside the window

It **warns and runs**. At startup and on `/readyz` it reads OpenCloud's
version (mechanism established in Phase 9) and logs at WARN when the version
is outside the window baked into the build. The admin view shows the same
notice. It never refuses to start: a household that upgraded OpenCloud a day
before the plugin caught up must still get its nightly backup, and the
failure mode of CS3 drift is a failing run that is already reported loudly
(stale-backup notification), not silent corruption.

## 4. Which OpenCloud interfaces this depends on

So that an upgrade can be checked against a list instead of a memory:

| Interface | Used for | Public upstream? |
|---|---|---|
| CS3 gateway gRPC (`ListStorageSpaces`, `Stat`, `ListContainer`, `InitiateFileDownload/Upload`, `CreateStorageSpace`, auth `serviceaccounts`) | reading Spaces, writing restores and state | **no** (opencloud#3289) |
| Storage-users data server (`/data`, 7.5+) | file transfer | **no** |
| Graph `/me`, `/me?$expand=appRoleAssignments,memberOf`, `/users` | identity, admin role, groups, user picker | yes (libre-graph) |
| OIDC issuer + JWKS | token validation | yes |
| Admin app-role id `71881883-…` | admin detection | de facto, unchanged 7.3–7.5 |
| Space grants in `Opaque` map | membership, roles, expiry | **no** |
| Web extension SDK, `web-pkg` composables, design-system `oc-*` globals | the UI | yes, semver'd per major |
| CSP file (`PROXY_CSP_CONFIG_FILE_LOCATION`) | `'wasm-unsafe-eval'` | yes (config) |
| Apps directory scanned at startup | installing the bundle | yes (documented) |

Every row marked **no** is what the canary exists for.
