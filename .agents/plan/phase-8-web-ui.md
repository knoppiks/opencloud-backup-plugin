# Phase 8 — Web UI Integration

**Goal:** the "Backup Vault" OpenCloud Web extension. This is where "user
clicks yes once" becomes real.

**Depends on:** Phase 0 Spike 4 (extension mechanism), Phase 0 admin-role spike
(client-side admin detection via the web SDK ability model; decisions.md #13),
Phases 2/3/5/6 (APIs).

## Views / flows

1. **Overview (single page, per-Space cards)**
   - Space list (from `GET /spaces`) with backup state per space:
     not configured / active / stale / failed.
   - Target shown read-only to the user (name only, from `GET /targets`;
     admin-managed, decisions.md #12). No credentials or endpoints exposed.

0. **Admin: target management (admin-only view)**
   - Visible only when the caller is an OpenCloud admin, gated client-side by the
     web SDK ability model (`useAbility()`; mechanism pinned by the admin-role
     spike) **and** enforced server-side by the admin middleware (Phase 2).
   - List / add / edit / delete targets (name, endpoint, region, bucket, prefix,
     path-style/TLS flags).
   - **Credential fields are write-only** (decisions.md #14): entered on
     add/edit, never rendered back; the API never returns them. Editing a target
     without re-entering credentials leaves the stored (wrapped) credentials
     untouched.
   - Grant management per target: **all users** or **specific users** (and/or
     specific spaces). Grants drive what each user sees in `GET /targets`.
   - This view manages targets/grants **only** — it exposes no user backup data,
     job status for other users' spaces, or plaintext (decisions.md #15).

2. **Setup wizard ("Yes, back up my data")** — the critical UX:
   1. Pick space (personal preselected).
   1b. **Pick target** (from `GET /targets`, granted targets only): shown only
       when the user has **more than one** granted target. With exactly one, it
       is auto-selected and this step is skipped, preserving the "one click"
       promise (decisions.md #12). The chosen `target_id` is sent to
       `POST /backup/setup`; the server re-checks the grant (never trusts the
       client).
   2. **Recovery Key ceremony (client-side):**
      - Generate RK in the browser (WebCrypto; Argon2id via wasm for the KEK).
      - Generate/wrap DK per the Phase 3 contract; **plaintext RK never leaves
        the browser.**
      - **Self-verify before sending (binding, see `key-envelope-format.md`
        §5):** unwrap the envelope just produced with the RK about to be shown,
        and compare the recovered DK. The server cannot check this — it holds no
        RK — so a client that skips it can hand the user a Recovery Key that
        opens nothing, with a green status to match.
      - Argon2id parameters must meet the API's accepted minimum (`time >= 2`,
        `memory >= 19 MiB`, `lanes >= 1`); weaker envelopes are refused with a
        400 that says so. Tune above the floor for slow devices, never below.
      - Display RK once + copy button + "save it in your password manager"
        wording (family-friendly language).
      - Confirmation gate: re-enter/re-paste a portion of the RK (proves it
        was saved) → only then `POST /backup/setup`.
      - **Setup is once-only:** a Space that already has keys answers 409. Treat
        that as "this Space is already protected", not as an error to retry —
        the wizard must not offer a way to re-run the ceremony, because doing so
        would orphan every existing backup.
   3. Pick schedule preset (daily/weekly) → `PUT /schedule`.
   4. Done screen: what happens next, when the first run occurs.

3. **Status board**
   - Last/next run, progress bar for running jobs (poll status endpoint),
     history list, stale/failure warnings mirroring Phase 6 notifications.
   - "Back up now" button (`POST /backup/run`).

4. **Restore flow (Path B)**
   - Snapshot picker (`GET /snapshots`, human dates: "Yesterday 03:00").
   - Confirm dialog: restores into `Restore/<ts>/`, does not overwrite.
   - Progress + completion link to the folder.

5. **Shared spaces**
   - Members see the space's backup status; RK-blob retrieval flow per
     Phase 3 (member-only endpoint).

6. **Replace the Recovery Key** — the only supported way to change a key after
   setup, and entirely client-side:
   1. Fetch the current envelope (`GET .../backup/recovery-envelope`).
   2. Ask for the *current* Recovery Key and unwrap it in the browser. A wrong
      key fails here, before anything is sent.
   3. Generate a new RK, re-wrap the **same** DK, and self-verify as in the
      setup ceremony.
   4. Same display-once + confirmation gate, then
      `POST .../backup/recovery-key/rotate` with the new envelope only. **The DK
      is not sent on this path.**
   5. Tell the user plainly: existing backups stay readable, nothing is
      re-uploaded, and the old Recovery Key stops working — destroy the old
      copy.

   A user who has *lost* their Recovery Key cannot use this flow, and there is
   no server-side escrow to fall back on. The UI must say so rather than
   offering setup again.

## Technical

- Follow Spike 4 findings: packaging (web app bundle / apps.yaml), OpenCloud
  design-system components, Vue 3 + TS, Vite build.
- Auth: reuse the web session token; the extension calls our backend with the
  forwarded token (mechanism from Spike 4).
- Frontend lives in `/web/` in this repo; CI job: lint + typecheck + unit
  tests (Vitest) + build; bundle artifact.
- Error states designed, not bolted on: backend down, token expired, run
  failed, restore conflict.
- i18n scaffold from the start (family audience: German + English).

## Testing

- Unit (Vitest): wizard state machine, RK ceremony (mock WebCrypto),
  confirmation gate cannot be skipped, self-verify step fails the ceremony when
  the produced envelope does not unwrap to the intended DK.
- Component: status board states (fresh/stale/failed/running) via mocked API.
- Admin target view: renders only for an admin ability; credential fields are
  write-only (never populated from API); non-admin never sees the view. Wizard
  target picker: hidden with one granted target, shown with several.
- E2E (Playwright against compose stack): full happy path — setup wizard →
  manual run → status turns green → Path B restore → file appears in
  `Restore/<ts>/`. One negative: wrong RK re-entry blocks setup.
- Crypto interop test: RK produced by the browser ceremony successfully
  decrypts a Take-Out via `cmd/decrypt` (browser-wasm and Go Argon2id/AEAD
  parameters must match exactly — this test is mandatory). The same test after a
  Recovery Key rotation: the new RK decrypts the take-out, the old one does not.

## Exit criteria

- [ ] Extension loads in OpenCloud Web, nav entry visible (success metric 7).
- [ ] Admin sees the target-management view; non-admin does not (client gate +
      server 403). Credentials never rendered back.
- [ ] E2E happy path green in CI.
- [ ] Browser↔CLI crypto interop test green.
- [ ] Plaintext RK provably never sent: network-layer assertion in E2E
      (inspect all requests during ceremony).
- [ ] UI reviewed against OpenCloud design system (native look — success
      metric 7).

## Sub-phases and sequencing

The phase is large enough to land in pieces. Each sub-phase is independently
reviewable and leaves the repository green.

| # | Sub-phase | Delivers | Depends on |
|---|---|---|---|
| 8a | **Crypto interop foundation** | Deterministic seal seam + committed test vectors in `pkg/keys`; `/web/` package; the TypeScript envelope, Recovery Key and ceremony modules; interop tests in both directions | — |
| 8b | **Admin target/grant API** | The `/api/v1/admin/` handlers decisions.md #12 defers to this phase: target CRUD, grant management, write-only credentials | R1 append-only stores, Phase 2 admin middleware |
| 8c | **Extension skeleton** | `/web/` loads in the fixture, nav entry, routing, i18n scaffold, API client, error states | 8a |
| 8d | **User flows** | Overview, setup wizard, status board, restore, RK replacement | 8a, 8c |
| 8e | **Admin view** | Target management + grants, client-side admin gate | 8b, 8c |
| 8f | **E2E** | Playwright against the compose stack, including the network-layer assertion that no plaintext RK is ever sent | 8d, 8e |

**Why 8a first.** The browser-to-Go crypto match is the only part of this phase
that can fail silently and be discovered years later, by a user who needs their
Recovery Key and finds it opens nothing. Everything else fails loudly.

### Decisions taken for this phase

1. **Browser crypto libraries: `@noble/ciphers` + `hash-wasm`.** WebCrypto has no
   XChaCha20-Poly1305 and no Argon2id, so both must come from somewhere;
   `AGENTS.md` forbids hand-rolling either. `@noble/ciphers` is audited, pure
   TypeScript and implements exactly the construction `golang.org/x/crypto` uses;
   `hash-wasm` supplies Argon2id as a small wasm module with explicit
   time/memory/lanes/salt parameters. The rejected alternative,
   `libsodium-wrappers-sumo`, covers both primitives from one reference
   implementation but costs several hundred kilobytes of wasm in a bundle the
   OpenCloud SPA loads eagerly.
2. **Package manager: pnpm**, with the version pinned via `packageManager` and
   Corepack. The Phase-0 spike used npm; the frontend is a long-lived artefact
   and follows the upstream OpenCloud skeleton instead.
3. **E2E is its own sub-phase (8f), not a tax on every view.** Standing up
   Playwright requires the fixture, a built bundle, app registration and
   automated OIDC login. Until 8f lands, the E2E exit criteria stay unmet and
   are recorded as such rather than quietly dropped.
4. **The admin API is Phase 8 work, not a prerequisite found missing.**
   decisions.md #12 already says so; it is called out here because
   `phase-2-auth-spaces.md` delivers only the middleware the routes sit behind,
   and a reader of the exit criteria below could reasonably assume the endpoints
   exist.

Tracked as issue #35.

### Sub-phase 8b plan — decisions taken before implementation

The route table:

```
GET    /api/v1/admin/targets              list
POST   /api/v1/admin/targets              create
GET    /api/v1/admin/targets/{id}         read one
PUT    /api/v1/admin/targets/{id}         update metadata; credentials optional
DELETE /api/v1/admin/targets/{id}         delete target + its grants
GET    /api/v1/admin/targets/{id}/grants  read the audience
PUT    /api/v1/admin/targets/{id}/grants  replace the audience, whole list
POST   /api/v1/admin/targets/check        stateless reachability check
```

All of it sits behind the existing `Authenticate -> ResolveAdmin -> RequireAdmin`
chain; the `/api/v1/admin/` prefix route stays as the 404 for anything
unmatched, so a path nobody implemented is still not a way past the gate.

1. **Grants are replaced as a whole list, not one at a time.** The store already
   writes a target's entire grant list as one document per version (`state.go`),
   so a full-list `PUT` is one request and one write. `Store` gains
   `ReplaceGrants`; `PutGrant`/`DeleteGrant` stay for the seeder and for callers
   that genuinely mean "one grant". The rejected alternative — `POST`/`DELETE`
   per grant — needed a body on a `DELETE` (a grant's identity is
   scope+user/space, not an id) and turned a multi-grant edit into N
   non-atomic requests, which is an authorization bug waiting for a crash.

2. **Credentials are all-or-nothing on write, and this is a consequence, not a
   preference.** A target's whole `CredentialSet` is one sealed blob and the
   admin path may not open it (#14). So: credentials absent from an update ->
   the stored blob is untouched (`UpdateTarget` already does this); credentials
   present -> the submitted set *replaces* the stored set entirely. Re-entering
   only the backup pair therefore drops a configured maintenance pair. Merging
   would require decrypting in the admin process, which is precisely the
   capability #14 withholds. The UI must say so; the API cannot hide it.

3. **`Target` gains a non-secret `maintenance_configured` flag.** The admin UI
   has to be able to show whether a target is credential-separated, and the only
   other way to answer it is to open the blob. The flag is a yes/no written at
   seal time from what the admin submitted — it is not key material. Records
   written before it read as `false`, which is the correct answer for a
   single-credential target.

4. **Deleting a target that Spaces are bound to is refused with a count.** 409
   plus "N spaces still use this target", never their ids — a count discloses no
   Space and no user, so #15 holds. There is no `force`: the alternative is an
   admin action that silently stops other people's backups. The admin rebinds or
   the members do, then the delete succeeds.

5. **Target ids are server-generated random hex**, as job ids are. An
   admin-chosen id is a namespace the admin types, which needs charset
   validation and a collision path, and makes "create" able to overwrite.

6. **The connection check is stateless and coarse.**
   `POST /api/v1/admin/targets/check` takes a full target spec *including*
   credentials and stores nothing; it never opens a stored blob. Testing a
   *stored* credential against an admin-supplied endpoint would be a credential
   oracle — edit the endpoint to a host you control, press "test", and the
   stored access key id leaves in the SigV4 `Authorization` header — so the
   check only ever uses what the caller already holds. The cost is that an
   existing target cannot be tested without re-entering its secret, which is
   what write-only credentials mean.
   It answers a classification per credential role (`ok`, `unreachable`,
   `auth_failed`, `bucket_missing`, `denied`, `timeout`) and nothing else: no
   upstream status code, no response body, no error text, no redirects, bounded
   timeout. The operation is a read-only list of the target prefix — what a run
   does first anyway. The endpoint is still an outbound request to a host the
   caller names, and the coarse answer is what keeps it from being a probe of
   the household's internal network with the results echoed back.

### Sub-phase 8c plan — decisions taken before implementation

The skeleton's job is to make every later view cheap: one place that knows where
the backend is, one place that speaks to it, one place that renders "this went
wrong". Seven decisions were taken before any code, because each of them is
expensive to reverse once views are written against it.

1. **The API base URL is a *path*, never an origin, and the type system says
   so.** The client resolves `new URL(apiPath, window.location.origin)`, where
   `apiPath` comes from `applicationConfig` and defaults to the deployed prefix.
   An `apiPath` carrying a scheme or a host is refused, not honoured.
   Rationale: the Go service has no CORS middleware and no `OPTIONS` route, and
   that is deliberate — the listener is plain HTTP behind an ingress on
   OpenCloud's own origin, and the Data Key crosses it once at setup (R5). A
   configurable *origin* would make "same-origin" a deployment convention that a
   single `config.json` edit can violate, with the failure mode being either a
   silently broken extension or a pull towards adding CORS. A configurable
   *path* makes cross-origin structurally impossible while still allowing the
   API to be relocated without rebuilding the bundle. The rejected alternative,
   hardcoding the path, costs that relocation for no gain.
   *Mechanism, which the SDK settles:* `web/src/manifest.json` is copied
   verbatim into `dist/manifest.json` with only `entrypoint` added, and the
   SDK's own metadata reader treats `manifest.config` as the app config. So the
   `config` key is the supply route; whether OpenCloud 7.3.0 forwards it into
   `setup({applicationConfig})` is verified against the fixture rather than
   assumed.

2. **The API moves to a namespaced prefix, and the service learns
   `BACKUPD_BASE_PATH`.** Routes stay written as `/api/v1/...`; a base path is
   stripped in front of the mux. The deployed prefix becomes `/backup/api/v1/`.
   Rationale: `/api/v1/` is a namespace OpenCloud also owns on the origin this
   service is now required to share. Nothing collides on 7.3.0 today, which is a
   statement about one release of software this project does not control. One
   env var and an `http.StripPrefix` retire the whole collision class, and the
   alternative — discovering it from a future OpenCloud release, in a household
   deployment, as backups that stop — is not a trade worth making for the saved
   line.

3. **The fixture gains a single origin, and which proxy provides it was measured
   rather than chosen.** OpenCloud's own proxy was the preferred answer — it is
   the production topology the README already prescribes — provided it forwards
   the caller's bearer token untouched. That proxy *authenticates* requests and
   rewrites headers, and if it exchanged the OIDC token for a reva one, every
   extension request would arrive at `Authenticate` as garbage. So the first
   task of 8c was a spike against the 7.3.0 fixture rather than an assumption.

   **Measured, and the risk we were testing for did not materialise.** A route
   carrying `unprotected: true` forwards `Authorization: Bearer …` **verbatim**;
   the proxy adds `X-Forwarded-*` and `Traceparent` and takes nothing away. It
   also **does not rewrite the path** — a request to `/backup/api/v1/spaces`
   arrives at the backend as `/backup/api/v1/spaces`, which is the independent
   confirmation that decision 2's `BACKUPD_BASE_PATH` is required rather than
   merely tidy.

   **A different obstacle appeared, and it is the one that decides the
   question: OpenCloud's proxy has no way to append a route.** Setting
   `proxy.policies` *replaces* the default route table wholesale — with our
   route plus a `/` fallback defined, every other endpoint on the origin
   (`/config.json`, `/graph`, `/.well-known`, `/konnect`, `/remote.php`, `/ocs`)
   answered 500. Setting `additional_policies` instead preserves the defaults but
   its routes are never consulted: the static selector resolves the
   default-named policy first and a same-named additional policy does not merge
   into it, so `/backup/` fell through to the SPA's catch-all. Using OpenCloud's
   proxy therefore means restating its entire default route table in our config,
   pinned to 7.3.0, and re-pinning it on every OpenCloud release — in the
   fixture *and* in every operator's deployment.

   **So: a dedicated reverse proxy provides the origin.** This is not a
   divergence from production, which is the objection that would have mattered
   (R9: a fake at a boundary tests the code's idea of the boundary). Production
   already prescribes "an ingress that terminates TLS on the same origin as
   OpenCloud", and an Ingress with two path rules *is* this shape; the
   OpenCloud-proxy variant was the odd one out. The measurement above is kept on
   the record because it leaves that variant available to an operator who wants
   it, with its cost stated.

4. **`backupd` joins the fixture in 8c, not in 8f.** The sub-phase delivers an
   API client and error states; both are claims that only a running backend can
   check. Two of this phase's known failure modes — `OIDC_AUDIENCE` not matching
   the SPA client id (#21), and the proxy question above — produce a 401 on every
   request and are indistinguishable from a dozen frontend bugs. Finding them
   here costs a fixture; finding them in 8f costs a fixture *plus* a new
   Playwright setup, with no way to tell which half is wrong. The price is
   honest: self-signed issuer trust inside the service container, a state Space
   that `seed.sh` must create with no member grants (R1 refuses to start
   otherwise), SRW/TW keys, and a memory-backed `BACKUP_WORK_DIR`.

5. **Lint arrives with one rule that pays for the tool on its first run.**
   ESLint flat config plus Prettier, wired into the make targets and the CI web
   job. The rule is a `no-restricted-imports` ban on `src/crypto/**` covering
   `vue`, `vue-router`, `@opencloud-eu/*`, `axios` and `fetch`. 8a's outcome
   states that directory has no Vue, no HTTP and no OpenCloud dependency, and
   calls that the reason the interop vectors could be written before any UI
   existed. It is currently true by care alone, and the first person to reach for
   a composable in a ceremony helper would make it false without noticing. A
   stated invariant that nothing checks is a comment.

6. **i18n ships its wiring and a German catalogue; extraction waits for 8d.**
   `ClassicApplicationScript.translations` takes a `{lang: {msgid: …}}` map, so
   the extension carries its own catalogue rather than depending on the host's —
   which is worth knowing, because today we pass none and every `$gettext` call
   falls through to its msgid, so nothing has looked broken. 8c wires it and
   ships `de` for the strings that exist, with a test that every catalogue key is
   a msgid the source actually uses. Extraction tooling pinned against a handful
   of strings is churn; the wiring is the scaffold, and it has to exist before
   8d's strings have anywhere to go.

7. **"It loads in the fixture" becomes a command, not a claim.** A make target
   builds, installs into `test/fixtures/opencloud/apps/`, and restarts OpenCloud
   — the restart is a documented gotcha, and a gotcha handled by a target is one
   nobody rediscovers. A verify step curls `/config.json` for the `external_apps`
   entry and asserts the entrypoint `.mjs` serves 200, which is precisely what
   Spike 4 checked by hand. In-browser proof stays 8f's; this is the exit
   criterion made scriptable without it.

### Sub-phase 8c outcome (implemented, issue #35)

The skeleton landed as planned. What the plan did not say, in rough order of how
much it cost:

- **The state Space could not be created, and that is a backend defect 8c
  tripped over rather than a frontend one.** Standing `backupd` up behind the
  fixture's origin was supposed to be configuration. It turned out that
  `cs3state.Check` refused every Space OpenCloud can actually produce, so a
  default deployment with durable state could not start at all — since R5 made
  `STATE_SPACE_ID` required, which is to say since R5. Recorded in full as
  "Amendments from Phase 8 — 8c" in `decisions.md`; the fix is that `Check`
  discounts the service account's own grant, plus `backupd
  provision-state-space`. The runbook in `README.md` was impossible to follow as
  written and now says so, along with why.
  *Why 8c found it and nothing else had:* `Check` was covered by unit tests
  against a fake Space which agreed with the code by construction. It is R9's
  lesson — a fake at a boundary tests the code's idea of the boundary — in a new
  place, and the place was a startup check, which is a claim about the
  environment specifically.

- **The proxy question had a clear answer and it was not the expected one.** The
  risk being tested — that OpenCloud's proxy would swap the OIDC bearer for a
  reva token — did not materialise: an `unprotected: true` route forwards
  `Authorization` verbatim and rewrites no path. What killed the option was that
  the proxy has no way to *append* a route: `policies` replaces the default
  route table wholesale (every other endpoint answered 500 in the spike) and
  `additional_policies` routes are never consulted by the static selector. Using
  it would mean restating OpenCloud's entire route table, per release, in every
  deployment. The fixture therefore gets Caddy on `:9200` and OpenCloud moves to
  `:9201`; the measurements are in the plan section above so the option stays
  available to an operator who wants it.
  **`:9200` staying the single origin is what made this cheap**: `OC_URL`, the
  issuer, the data-gateway URLs and every existing integration test are
  untouched, and the OpenCloud-fixture suite passes through the new proxy
  without knowing it is there.

- **`backupd` runs on the host in the fixture, not in the compose stack.** The
  sub-phase decision said "in the compose stack"; the deviation is forced by the
  issuer. Tokens carry `iss: https://localhost:9200`, so the service must be
  configured with that exact string — and inside a container `localhost:9200` is
  that container. Aliasing `localhost` to reach the proxy is worse than running
  the binary where every integration test in this repo already runs it, and it
  keeps the 8d/8e loop at `go run` rather than an image build. `up.sh` now
  writes everything it needs into `fixture.env`, including
  `OIDC_AUDIENCE=web` — the value #21 makes load-bearing.

- **`applicationConfig` works, and the delivery route is
  `web/src/manifest.json`.** The SDK copies that file into `dist/manifest.json`
  adding only `entrypoint`, and OpenCloud 7.3.0 forwards its `config` key into
  `config.json`'s `external_apps[]`, from where it arrives in `setup()`.
  Verified end to end against the fixture rather than assumed, because this was
  the one link in decision 1 that could not be read off the SDK source.

- **Same-origin is now a property of the code, not a warning in a README.**
  `resolveApiBase` takes a path and resolves it against `window.location.origin`,
  and refuses anything carrying a scheme, a host or a protocol-relative prefix —
  with a final assertion that the resolved origin still matches, so a future
  edit that loosens the checks has to defeat that too. The service gained the
  matching half, `BACKUPD_BASE_PATH`, and the health probes deliberately stay at
  the root because the kubelet does not go through the ingress that adds the
  prefix.
  Measured on the running fixture: `/healthz` 200 at the root, bare
  `/api/v1/spaces` **404** (the prefix is not bypassable), `/backup/api/v1/spaces`
  401 through Caddy with exactly the error envelope the TypeScript client parses.

- **ESLint earned itself on the first run.** The `src/crypto` import ban is not
  a style rule: 8a's outcome calls that directory's independence the reason the
  interop vectors could exist before any UI, and it held by care alone. Verified
  by mutation — adding `vue`, `vue3-gettext` and `fetch` to `bytes.ts` produces
  three errors naming each one. Prettier came with it and reformatted four files
  8a had hand-formatted; the changes are cosmetic, and two of them collapse
  vertical unions that were arguably nicer before. That is the trade a formatter
  is: it is worth it to stop having the conversation.

- **i18n ships a German catalogue and a test that can fail.** The finding worth
  recording is that `translations` was never passed to the host, so every
  `$gettext` call fell through to its msgid — which looks perfect in English and
  is simply untranslated in German, with nothing to notice. `translations.spec.ts`
  scans the source for msgids and fails on an English string with no German, on
  a German entry whose original has gone, and on a "translation" that is a copy
  of the msgid. It also asserts it found msgids at all, because a regex matching
  nothing would make the other three vacuous. Verified by mutation.
  No `.pot`/`.po` pipeline yet, as planned — it arrives in 8d with enough text
  to justify it.

- **"It loads in the fixture" is a command.** `make web-install-fixture` builds,
  installs, chowns for the container's uid, restarts OpenCloud — the restart is
  the documented gotcha, and one handled by a target is one nobody rediscovers —
  then asserts the app is in `external_apps` *with its `config.apiPath`* and that
  the entry chunk serves 200. `make web-verify-fixture` re-checks without
  rebuilding. Verified by mutation that the registration check fails when the app
  is absent.

- **Deviation — `make dev-up` now seeds.** `up.sh` rewrites `fixture.env` from
  scratch and `dev-up` never ran `seed.sh`, so the seeded ids silently vanished
  and `make test-opencloud` failed reporting a variable as "not set". It cost a
  false failure during this sub-phase before the cause was obvious.

- **Not done, and not claimed:** no component tests mount a view. The client, the
  base-URL resolution, the app config and the catalogue are unit-tested (143
  tests in `web/`), and the extension is proven to load and to reach the API
  through the real origin; asserting what `RequestState.vue` *renders* for each
  failure needs `@vue/test-utils` and the host's design-system globals stubbed,
  which belongs with 8d's views rather than ahead of them.

### Sub-phase 8b outcome (implemented, issue #35)

The eight routes landed as planned, with the semantics above. What the plan did
not say:

- **Grant scopes travel as words, not as the stored integer.** `GrantScope` is
  an `iota` on disk and stays that way — the records are append-only and
  re-encoding them would mean rewriting history — but the API speaks
  `all_users` / `user` / `space`. An authorization API whose meaning depends on
  remembering that `2` means "user" is one typo away from granting the wrong
  audience, and the zero value would be the typo that grants everybody.
- **`Grant.Validate` was missing and is now enforced in the store, not only at
  the boundary.** The model always said "exactly one of the scope-specific
  fields is set, per Scope" and nothing checked it: `PutGrant` would happily
  persist `{Scope: 0}`, which `grantsAllow` then matches against nothing — a
  grant an admin wrote, that silently grants no one. Both stores now refuse it,
  so the seeder and any future caller are held to the same rule as the API.
- **A refused grant write changes nothing.** `ReplaceGrants` validates the whole
  list before writing any of it; applying the valid prefix would leave an admin
  with an audience they did not choose, which is worse than the error.
- **The 404s are uniform.** A target that does not exist and an id that never
  did produce the same body on every route, for the same reason denials are
  uniform elsewhere: otherwise the admin surface enumerates.
- **`TestAdminRouteTable` is the new `roleTable`.** The admin routes get the
  table treatment the space-scoped ones already had — unauthenticated is 401,
  non-admin is 403, admin gets through, once per route — and
  `TestAdminRoutesReturnNoCredential` walks the same table instead of the single
  hand-written row it replaced. It checks the plaintext markers, the encodings
  of the seeded blob, *and* the encodings of every blob in the store after the
  call, because `create` mints a fresh envelope that a test knowing only the old
  one would miss. Verified by mutation: adding the sealed blob to the admin DTO
  fails three rows.
- **Validation errors are part of the credential promise.** A handler that
  never returns a credential can still leak one by quoting the offending input
  back; `TestAdminValidationErrorsDoNotEchoCredentials` submits real secrets in
  a body that fails validation and checks the 400.
- **Deviation — the check has a seventh outcome, `unknown`.** The six agreed
  names cover what an admin can act on; an S3 error code nobody mapped is not
  one of them, and forcing it into `denied` would send someone to fix a
  permission that is fine. It follows the precedent already in the package: the
  capability probe reports `unknown` rather than guessing.
- **Deviation — `maintenance_credentials` alone is refused, on create *and* on
  update.** The plan only said a half-filled pair is refused. Sending a
  maintenance pair with no backup pair is the same mistake wearing a different
  hat: it would require opening the stored blob to keep the half that was not
  sent, which is the decrypt the admin path does not have.
- **`DELETE` needs the space-config store, and says so when it does not have
  it.** With no way to count bound Spaces the handler answers 503 rather than
  deleting — the same shape as decisions.md #20: a consequence that cannot be
  established is refused, not guessed.
- **Without `TW_KEY` the admin surface still works, minus credentials.**
  Create and credentialed updates answer 503 with a message naming the missing
  key; listing, metadata edits and grants carry on. An operator whose TW key is
  missing needs the admin UI to open in order to discover that.

### Sub-phase 8a outcome (implemented, issue #35)

The crypto foundation, in both directions, plus three things the plan did not
anticipate.

- **The interop pin is two files, not one.** `pkg/keys/testdata/vectors.json`
  (generated by `go test ./pkg/keys -run TestGoldenVectors -update`, read by
  `web/src/crypto/vectors.spec.ts`) proves the browser reproduces Go's bytes
  from Go's inputs — KEK, header, sealed envelope, Recovery Key display string.
  That only ever exercises the salts, nonces and entropy *Go* chose, so
  `web/testdata/browser-vectors.json` carries envelopes the browser produced
  with its own randomness and its own generator, and `TestBrowserVectors` opens
  every one of them through the same `DecodeRecoveryKey` path the decrypt CLI
  uses. Either half alone would have left a direction unchecked.
- **`seal` gained a randomness seam.** Deterministic vectors are impossible
  while salt and nonce come from `crypto/rand` inside the function, so
  `sealWith(random io.Reader, …)` exists and `seal` is a one-line call into it
  with `rand.Reader`. Unexported, reader first, so a call site supplying weak
  randomness is visible at a glance.
- **Finding — the Recovery Key is 34 characters, not 35.** Every document and
  the package comment said "35 characters in seven groups of five"; the value
  has always been 34, grouped 5-5-5-5-5-5-4, 46 characters including prefix and
  dashes. Nothing asserted it, so it survived from Phase 3 to here. The wizard
  lays the key out in a grid, the confirmation gate asks for a specific group
  and the decrypt CLI prompts for it — three places that would have been built
  against a shape that does not exist. Now pinned by `TestRecoveryKeyShape` and
  corrected in `key-envelope-format.md`, `phase-3-keys.md` and the package
  comment.
- **Deviation — the typo test asserts a rate, not an absolute.** The checksum
  is eight bits, so roughly one single-character typo in 256 passes it by
  chance; a test demanding that *every* typo be caught would fail for no reason
  about once a fortnight. What is asserted without exception is the property
  that matters: a typo never decodes to the entropy that was mistyped.
- **The self-verification has a test that can fail.** `vi.mock` replaces `seal`
  with two plausible bugs — an envelope sealed under a different secret, and one
  wrapping a different Data Key — and the ceremony must refuse both. Without the
  mock the check would be exercised only by code that is already correct, which
  is the one case it exists to not care about.
- **Note — no ESLint yet.** The phase's CI line says "lint + typecheck + unit
  tests + build"; what landed is typecheck (`vue-tsc`, strict, including
  `exactOptionalPropertyTypes` and `noUncheckedIndexedAccess`), tests and build.
  A Vue/TS lint configuration earns its keep alongside the components in 8c and
  is deferred to there rather than configured against four files.
- **`web/src/crypto` has no Vue, no HTTP and no OpenCloud dependency.** It is
  reviewable, and testable, entirely on its own — which is the only reason the
  vectors above could be written before any UI exists.

## Risks / notes

- Argon2id in the browser (wasm) + exact parameter match with Go is the
  fiddly part — build the interop test **first**. *(Done in 8a: the parameters
  match, pinned in both directions.)*
- Extension API surface of OpenCloud Web may shift between versions; pin the
  tested OpenCloud version in compose and document the supported range.
