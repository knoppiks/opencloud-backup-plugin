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
already knows; burning a MAJOR every few months on it (§3: a major leaves
whenever OpenCloud ships a new one) would make MAJOR meaningless. The alternative is to treat every drop as MAJOR.
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

### How OpenCloud releases

All releases come from one codebase (`opencloud-eu/opencloud`, release list
2025-02 to 2026-10):

- **Rolling** is `main`: a new MINOR or MAJOR every ~3 weeks, never patched.
  The MAJOR goes up whenever something breaking lands, which so far has been
  about operating the server (search index, config, migrations), every 2–3
  months (1.0 in February 2025, 8.0 in September 2026).
- **Production** is a Rolling release OpenCloud kept patching on a side
  branch (`stable-2.0`, `stable-4.0`, `stable-7.2`). A new line is cut from
  `main` every 4–9 months; the previous line's patches stop at about the same
  time. Production users jump from one line straight to the next.
- **LTS** is a customer offering, out of scope: the families this targets do
  not have it.

So the MAJOR number says nothing about the channel, and little about what
breaks *this* plugin: the one break so far (7.5.0's data URLs) came in a
MINOR.

### The rule (decisions.md #25, as amended by "Moving to OpenCloud 8.x")

**Supported = every release of OpenCloud's newest two majors, from the
oldest leg up to the newest, plus every patch release of the current
Production line.** The channel a release came out on does not matter inside
the range.

**Tested** (CI legs, all blocking, `pkg/ocversion/versions.yaml`):

| Leg | Pins | Why |
|---|---|---|
| `oldest` | the first release of the previous major (floor: 7.3.0) | breaking changes land in minors too; the oldest version claimed is always run |
| `previous-major` | the newest minor of the previous major | what most households a major behind run |
| `newest` | the newest release | where the owner runs and breakage arrives first |
| `production` | the newest patch of the current Production line | its patches come from a side branch and can differ from `main` |
| canary | `opencloud-rolling:latest` by tag, nightly | **non-blocking**, opens an issue |

A leg that would pin the same release as another is left out. Releases
between the legs are not run on every PR; each was run when it was newest,
and the legs bracket them.

**When the window moves:**

1. *A new release:* `newest` moves to it once CI is green. A new MINOR of the
   previous major moves `previous-major`.
2. *A new major:* the oldest major leaves. `oldest` moves to the first
   release of the major before the new one, `previous-major` to that major's
   newest minor. `Parse` refuses Rolling legs spanning three majors, so this
   cannot be forgotten.
3. *A new Production line:* `production` moves to it. The previous line is
   no longer current; it stays supported only as far as the two-majors range
   covers it.
4. *A floor:* a release the plugin cannot work on is excluded with a reason,
   and the range starts above it. Today's floor is 7.3.0: on 7.2.x a service
   account cannot create the state Space (decisions.md, "Moving to OpenCloud
   8.x").

The previous grace-period proposal (keep the previous Production line 90
days) is **superseded** by this rule: a release leaves only when the second
major after its own ships, with OpenCloud's cadence roughly 4–6 months after
its own major appeared, and a current Production line never leaves.
**Status (2026-10-08, owner): rule decided; dropping the grace period is
proposed, owner to confirm.**

Rationale: the old rule ("the current Production line plus every Rolling
release since it was cut") restarted the window at every Production cut, so
on the day a new line shipped every release before it — including the one
households had run the day before — left at once. A version range with a
two-majors horizon moves one step at a time.

### The window today (2026-10-08)

**7.3.0 to 8.1.0.** Measurements per version: phase-9 doc, 9.3 outcome;
what changed: decisions.md, "Moving to OpenCloud 8.x".

| Versions | Status |
|---|---|
| 7.2.x and older | **excluded** (owner decision): below the 7.3.0 floor |
| 7.3.0 | **CI leg `oldest`** |
| 7.4.0 | in the window, bracketed, never run |
| 7.5.0 | **CI leg `previous-major`** (newest 7.x) |
| 8.0.0, 8.0.1 | in the window, bracketed, never run |
| 8.1.0 | **CI leg `newest`**; the canary (`latest`) resolved to the same digest on 2026-10-07 |

No `production` leg: the current Production line (7.2.x) is below the
floor. It returns with the next Production line, announced for
**2026-10-26**.

### Worked example (dates after today are assumptions)

| Date | Event | Window | Legs |
|---|---|---|---|
| 2026-10-08 | today | 7.3.0 – 8.1.0 | oldest 7.3.0, previous-major 7.5.0, newest 8.1.0 |
| 2026-10-26 | Production 8.2.x cut from 8.2.0 | 7.3.0 – 8.2.0, plus 8.2.x | oldest 7.3.0, previous-major 7.5.0, newest 8.2.0, production 8.2.x |
| ~2026-12 | 9.0.0 ships | 8.0.0 – 9.0.0, plus 8.2.x | oldest 8.0.0, previous-major 8.3.0, newest 9.0.0, production 8.2.x |
| ~2027-02 | 10.0.0 ships | 9.0.0 – 10.0.0, plus 8.2.x (still the current Production line) | oldest 9.0.0, previous-major 9.x, newest 10.0.0, production 8.2.x |
| ~2027-04 | Production 10.1.x cut | 9.0.0 – 10.1.0, plus 10.1.x; 8.2.x leaves | oldest 9.0.0, previous-major 9.x, newest 10.1.0, production 10.1.x |

### What "supported" means, concretely

A version is supported if and only if CI runs the full OpenCloud fixture
suite and the browser E2E against it or against legs on both sides of it
(decisions.md #9's lesson: a claim not exercised against the real thing is
not a claim). A release's notes list the exact versions (by digest) it was
tested against.

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
