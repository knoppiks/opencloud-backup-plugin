# Phase 0 — Admin-Role Detection Spike

**Goal:** validate that the in-app admin (decisions.md #12/#13/#15) can be
derived from the **OpenCloud admin role** — reliably on both the server (Go
backend) and the client (web extension) — before Phase 2 and Phase 8 depend on
it. Output is knowledge + a pinned mechanism, not a feature.

**Why this is a spike, not an assumption:** research against the repo found the
capability *exists* but is **unproven** here. The graph `User` model exposes
`appRoleAssignments` / `memberOf`, and the web SDK ships a CASL ability model
(`useAbility()`), but no code demonstrates admin detection, and the concrete
admin `appRoleId` (and the exact CASL rule) for the pinned OpenCloud version is
not recorded. If graph detection turns out unreliable, decisions.md #13 defines
the fallback (operator-provided allow-list of admin subject IDs).

**Depends on:** the existing OpenCloud fixture (`test/fixtures/opencloud/`,
Spikes 3 & 4). Pin: `opencloudeu/opencloud-rolling:7.3.0`.

**Driver (throwaway):** `spikes/adminrole/` (Go + a small web check reusing
`spikes/webext/`). Not production code.

## Questions to answer

1. **Server-side:** given a user's OIDC bearer token (or the worker's ability to
   call graph on their behalf), can the backend reliably decide "is this user an
   OpenCloud admin?"
   - Does the OIDC **access token** carry a role/group claim we can trust
     directly? (If yes, cheapest path — no extra call.)
   - Otherwise, call `GET /graph/v1.0/me?$expand=appRoleAssignments` and map
     `appRoleId` → admin. **Pin the concrete admin `appRoleId`** emitted by
     7.3.0.
2. **Client-side:** in the web extension, can we gate an admin-only view with the
   SDK ability model — e.g. `useAbility().can('read-all', 'Setting')` (or the
   correct admin subject/action for 7.3.0)? Confirm it is `true` for an admin and
   `false` for a normal user.

## Tasks

- Seed the fixture with **two users**: the bootstrap admin and one normal user
  (extend `test/fixtures/opencloud/seed.sh`).
- Server driver: authenticate as each user; inspect token claims; call graph
  `/me?$expand=appRoleAssignments`; record which field/`appRoleId` distinguishes
  admin. Assert admin→true, user→false.
- Client driver: in the webext spike, read `useAbility()` for each session and
  assert the admin gate resolves correctly.
- Record the exact mechanism (claim name **or** graph field + admin `appRoleId`;
  and the client CASL rule) in `phase-0-findings.md`.

## Exit criteria

- [ ] Server-side admin detection returns `true` for the seeded admin and
      `false` for the seeded normal user, via a mechanism pinned in findings.
- [ ] Client-side ability gate resolves the same way in the extension.
- [ ] The admin `appRoleId` (or the accepted token claim) and the client CASL
      rule are recorded in `phase-0-findings.md` with the OpenCloud version.
- [ ] If detection is unreliable, the config allow-list fallback (decisions.md
      #13) is documented as the path Phase 2 will implement instead.

## Notes

- This spike only decides *whether the caller is an admin*. It grants **no** data
  powers: admin scope is targets + grants only (decisions.md #15). The check
  gates `/api/v1/admin/*` (server) and the admin view (client); everything else
  stays user-scoped.
- Keep the fixture change minimal and reversible; `down.sh --purge` must still
  yield a clean slate.
