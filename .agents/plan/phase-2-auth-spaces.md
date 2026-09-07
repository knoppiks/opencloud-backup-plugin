# Phase 2 — Auth & Space Enumeration

**Goal:** authenticated users can list their Spaces through our backend.
First real end-to-end slice: browser token → backend → CS3 gateway → JSON.

**Depends on:** Phase 0 Spike 3 (CS3 read path, token model), Phase 0 admin-role
spike (admin detection, for the admin-only endpoints; decisions.md #13),
Phase 1 layout.

## Deliverables

1. **OIDC middleware** (`/pkg/api`)
   - Validates bearer tokens issued by the OpenCloud IDP (JWKS discovery via
     issuer metadata; audience/issuer checks).
   - Extracts user identity (sub, username) into request context.
   - No sessions of our own — stateless validation per request.
   - **Admin detection** (decisions.md #13): resolves whether the caller is an
     OpenCloud admin using the mechanism pinned by the admin-role spike (graph
     `appRoleAssignments`, or the documented config allow-list fallback), and
     places an `isAdmin` flag in request context. An **admin-only middleware**
     gates the `/api/v1/admin/*` routes and 403s non-admins. Admin status grants
     **only** target/grant management — never plaintext, never cross-space
     backup access (decisions.md #15).

2. **CS3 client wrapper** (`/pkg/cs3`)
   - Implements `SpaceReader` against the Reva gateway using
     `github.com/cs3org/go-cs3apis`.
   - Exchanges/forwards the user token as learned in Spike 3.
   - `ListSpaces(ctx, user) []Space` — id, name, type (personal/project),
     size, member roles (needed later for shared-RK access checks).
   - Pagination handled internally.

3. **REST API** (v1, JSON)
   - `GET /api/v1/spaces` — spaces of the authenticated user.
   - `GET /api/v1/targets` — backup targets **granted to the authenticated user**
     (decisions.md #12), sourced from the app target store (`pkg/targets`), not
     from static config. **Least disclosure:** the response carries only what the
     UI needs to pick a target — `{id, name}` — and **never** credentials
     (decision #14). Endpoint/bucket/region are not exposed to end users in v1.
   - `GET /healthz`, `GET /readyz`.
   - Consistent error envelope; no internal errors leaked.
   - Admin target-management endpoints (`/api/v1/admin/targets`,
     `.../grants`) are specified in the phase that implements the target store;
     Phase 2 provides the admin middleware they sit behind.

4. **Authorization rules (foundation for later phases):**
   - A user may only see / act on Spaces they are a member of — enforced against
     CS3 membership, not client input.
   - A user may only see / use targets **granted** to them; grant checks are
     server-side (`targets.Authorizer`), never trusting client-supplied
     `target_id`.

### Role table (amended by remediation R3)

Membership alone is not authority. Every space-scoped route states a minimum
role, derived from the CS3 grant's *permission set* (reva serializes no role
name). Expired grants are not grants; group grants count for the caller's
groups.

| Action | Minimum role |
|---|---|
| view status, schedule, runs, notifications, config; list snapshots; fetch the recovery envelope; **restore into the Space** | viewer (any member) |
| run backup now; change config, schedule or retention | editor |
| key setup; Recovery-Key rotation | manager (or owner) |

Rationale for the boundaries:

- **Editor for writes to the backup configuration.** Changing where a Space is
  backed up to, or how long it is kept, is a change to the Space's protection.
  Editor is the OpenCloud authority to change the Space's contents.
- **Manager for the key ceremony.** Setup decides what every future snapshot is
  encrypted under; rotation invalidates the Recovery Key every other member is
  holding. Both reset what the Space's members can decrypt with.
- **Viewer for restore**, per decisions.md #7: disaster recovery is a member
  capability, not a management one. See the caveat recorded there.

Role derivation, group resolution and expiry are implemented in `pkg/cs3`
(`role.go`) and enforced in `pkg/api` (`access.go`). The grant shape is pinned
against a live OpenCloud by `TestIntegration_SpaceGrantsShape`.

**Group grants require `OC_BASE_URL`.** OpenCloud 7.3.0 puts no groups claim on
the access token and exposes no `/me/memberOf` route, so groups are read from
`GET /graph/v1.0/me?$expand=memberOf` with the caller's own bearer token. They
are resolved lazily — only when the caller's direct grant falls short and the
Space actually carries a group grant — and at most once per request. Without
`OC_BASE_URL` a Space with a group grant answers 503 for the callers who would
need it, rather than silently admitting or denying them.

## Testing

- Unit: middleware with a fake JWKS server (valid, expired, wrong audience,
  none). API handlers against a fake `SpaceReader`. Admin middleware against a
  fake admin-resolver (admin → 200, non-admin → 403). The role table as a
  caller × route cross-product (`TestRoleTable_EveryRouteEnforcesItsMinimumRole`).
- Integration (compose oCIS from Spike 3): real login → list spaces → compare
  with seeded fixtures. Negative: foreign user sees nothing. Admin detection:
  seeded admin resolves `isAdmin=true`, seeded normal user `false`. Grant shape:
  a seeded project Space with a viewer, an editor, a manager, a group grant and
  an expiring grant parses to the expected roles.

## Exit criteria

- [ ] Valid IDP token → correct space list, invalid → 401, foreign space → 403.
- [ ] No CS3 details or credentials leak through the API.
- [ ] `GET /targets` returns only granted targets, `{id, name}` only, no creds.
- [ ] Admin middleware: admin → allowed, non-admin → 403 on `/admin/*`.
- [ ] Every space-scoped route refuses a caller below its minimum role, and an
      expired or group grant is evaluated correctly (R3).
- [ ] Integration test green against the compose environment.

## Risks / notes

- Token exchange semantics (user token vs. machine auth) come from Spike 3; if
  reva requires a gateway-issued token, the wrapper hides that detail.
- Keep `Space.members` in the model now — Phase 3 (shared RK retrieval) and
  Phase 5 (Path B authorization) need it. **Amended by R3:** `Space.Members` is
  `map[string]cs3.Member{Role, ExpiresAt, Group}`, not `map[string]string`.
- `ListSpaces(ctx, user)` in deliverable 2 shipped as `ListSpaces(ctx)`: the
  worker credential lists everything and the API filters by the caller's role.
  Pagination is not implemented — OpenCloud returns the full set at this scale.
