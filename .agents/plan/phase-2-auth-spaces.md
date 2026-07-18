# Phase 2 — Auth & Space Enumeration

**Goal:** authenticated users can list their Spaces through our backend.
First real end-to-end slice: browser token → backend → CS3 gateway → JSON.

**Depends on:** Phase 0 Spike 3 (CS3 read path, token model), Phase 1 layout.

## Deliverables

1. **OIDC middleware** (`/pkg/api`)
   - Validates bearer tokens issued by the OpenCloud IDP (JWKS discovery via
     issuer metadata; audience/issuer checks).
   - Extracts user identity (sub, username) into request context.
   - No sessions of our own — stateless validation per request.

2. **CS3 client wrapper** (`/pkg/cs3`)
   - Implements `SpaceReader` against the Reva gateway using
     `github.com/cs3org/go-cs3apis`.
   - Exchanges/forwards the user token as learned in Spike 3.
   - `ListSpaces(ctx, user) []Space` — id, name, type (personal/project),
     size, member roles (needed later for shared-RK access checks).
   - Pagination handled internally.

3. **REST API** (v1, JSON)
   - `GET /api/v1/spaces` — spaces of the authenticated user.
   - `GET /api/v1/targets` — configured backup target(s) (admin-defined,
     read-only; from config, no credentials in the response).
   - `GET /healthz`, `GET /readyz`.
   - Consistent error envelope; no internal errors leaked.

4. **Authorization rule (foundation for later phases):** a user may only see /
   act on Spaces they are a member of — enforced against CS3 membership, not
   client input.

## Testing

- Unit: middleware with a fake JWKS server (valid, expired, wrong audience,
  none). API handlers against a fake `SpaceReader`.
- Integration (compose oCIS from Spike 3): real login → list spaces → compare
  with seeded fixtures. Negative: foreign user sees nothing.

## Exit criteria

- [ ] Valid IDP token → correct space list, invalid → 401, foreign space → 403.
- [ ] No CS3 details or credentials leak through the API.
- [ ] Integration test green against the compose environment.

## Risks / notes

- Token exchange semantics (user token vs. machine auth) come from Spike 3; if
  reva requires a gateway-issued token, the wrapper hides that detail.
- Keep `Space.members` in the model now — Phase 3 (shared RK retrieval) and
  Phase 5 (Path B authorization) need it.
