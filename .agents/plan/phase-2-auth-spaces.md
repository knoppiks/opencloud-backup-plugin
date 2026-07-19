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

## Testing

- Unit: middleware with a fake JWKS server (valid, expired, wrong audience,
  none). API handlers against a fake `SpaceReader`. Admin middleware against a
  fake admin-resolver (admin → 200, non-admin → 403).
- Integration (compose oCIS from Spike 3): real login → list spaces → compare
  with seeded fixtures. Negative: foreign user sees nothing. Admin detection:
  seeded admin resolves `isAdmin=true`, seeded normal user `false`.

## Exit criteria

- [ ] Valid IDP token → correct space list, invalid → 401, foreign space → 403.
- [ ] No CS3 details or credentials leak through the API.
- [ ] `GET /targets` returns only granted targets, `{id, name}` only, no creds.
- [ ] Admin middleware: admin → allowed, non-admin → 403 on `/admin/*`.
- [ ] Integration test green against the compose environment.

## Risks / notes

- Token exchange semantics (user token vs. machine auth) come from Spike 3; if
  reva requires a gateway-issued token, the wrapper hides that detail.
- Keep `Space.members` in the model now — Phase 3 (shared RK retrieval) and
  Phase 5 (Path B authorization) need it.
