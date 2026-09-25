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

### Sub-phase 8d plan — decisions taken before implementation

These decisions were made before any view was written. Surveying the API from
the browser's side showed four gaps. Each of them would otherwise have been
papered over in TypeScript:

- `GET /spaces` returns `{id, name, type}` and nothing else. It does not give
  the caller's role or any backup state.
- `/backup/status` has no staleness. The only "stale" rule lives inside
  `notify.Monitor.isStale`.
- Key state is available only from a second endpoint.
- A restore job does not record the folder it wrote into, so the UI has nothing
  to link to.

1. **The backend closes those gaps. The frontend does not work around them.**
   - `GET /spaces` gains the caller's **own** `role` on each Space. This is not
     the membership disclosure that `spaceDTO`'s comment rules out, which is
     about other people's grants. It is what the caller could learn anyway by
     trying an action and receiving a 403.
   - `/backup/status` gains `keys_configured` and `stale`. The staleness rule is
     extracted from the monitor into one function that both call.
   - Restore jobs gain a space-relative `restore_folder`.

   The rejected alternative was N+1 requests per card, a TypeScript copy of the
   staleness rule, and roles discovered by failing. Its cost is two copies of a
   rule that must agree, and the one showing the user a green card would be the
   copy nobody tests against the notifier.

2. **Progress is indeterminate, and the plan's "progress bar" stays deferred.**
   The Phase-6 amendment deferred live progress "to Phase 8 if the UI proves it
   needs more". Nothing in 8d proves that. A running job shows "running since
   <time>", an indeterminate indicator, and the last completed run's counts.
   Status is polled only while a job runs and the view is mounted. A real
   percentage would mean wiring kopia's uploader progress through the snapshot
   engine boundary. That is backend work with its own tests, and does not belong
   inside a UI sub-phase.

3. **The wizard writes in the order that keeps a crash cheap, and resumes from
   the server, not the browser.**
   - The steps are `PUT /backup/config` (target binding: cheap, redoable), then
     `POST /backup/setup` (keys: once-only, #17), then `PUT /backup/schedule`.
   - When the wizard reopens, it reads `keystatus`/`status` and skips what is
     already done. The ceremony above all is never offered again for a Space
     with keys.
   - Nothing about a half-finished setup is kept in browser storage. A Recovery
     Key must never end up there.
   - Setup's 409 is shown as "this Space is already protected".
   - Role gates come from decision 1's `role`:
     - An editor can bind a target and set a schedule, but meets "a manager of
       this Space has to finish setup" at the key step.
     - A viewer gets a read-only card.
     - The server still enforces every one of these gates. The client gate is
       about presentation, not security.

4. **The confirmation gate asks for two randomly chosen groups of the seven.**
   One group can be read off the screen before the key is hidden. The full key
   is friction that a family audience would work around. The gate is a state in
   the wizard's machine and cannot be skipped. It is unit-tested alongside the
   self-verify failure.
   - The Recovery Key lives in component state for the duration of the ceremony
     only. It is never in a store, a route, a query string or a log.
   - The Data Key is zeroized after the setup POST settles, whether it succeeds
     or fails.

5. **Schedule: daily or weekly, with a time of day.** Daily at 02:30 is
   preselected; weekly adds a weekday. The time is in the **service's**
   timezone (R6), and the UI says so rather than implying the browser's. There
   is no cron input. A custom cron set through the API is shown read-only.
   `weekday` is `omitempty` on the wire, so a weekly preset with no weekday
   means Sunday.

6. **Retention is editable on the status board by editors.** The floor (#22)
   is explained in the form, not only in the 400.
   - `PUT /backup/config` replaces the whole record, so a retention edit would
     have to re-send `target_id` and `enabled` as read a moment earlier. Two
     tabs could then silently undo each other's edits, because the state store
     has no compare-and-set (#16). **So 8d.1 adds `PATCH /backup/config`:**
     fields that are absent stay as stored, the same validation as `PUT`
     applies (retention floor, grant re-check when `target_id` is present),
     editor role, and 404 when the Space has no config yet. That narrows the
     race to the field actually being edited. It does not remove it, and the
     plan does not claim it does.
   - The wizard does not ask about retention; it takes the default.

7. **Shared-space retrieval becomes "Check my Recovery Key".**
   - Any member (viewer and above, #7) can enter a Recovery Key. The browser
     fetches the recovery envelope and unwraps it locally, then reports only
     "this key works" or "this key does not open this Space's backups".
     Nothing is sent back.
   - The same view offers `recovery.ocbke` as a download for the offline
     `decrypt` path.
   - This also gives owners a way to find out they have lost their key before
     a disaster instead of during one. The "lost key" wording (no escrow, no
     re-setup) lives here and in the replacement flow.

8. **i18n keeps the TypeScript catalogue and its spec. There is no `.po`
   pipeline.** This deviates from 8c decision 6. The spec already fails on a
   missing, stale or copied translation, and extraction tooling for two
   languages would add tooling to maintain without adding any check the spec
   does not already make. Revisit if a third language arrives.

9. **Component tests arrive with the first view that needs them.** They use
   `@vue/test-utils` (pinned), with the host's `oc-*` components stubbed in one
   shared setup file.

10. **Found during the survey, fixed in 8d.1:** `JobState` in
    `web/src/api/types.ts` says `'queued'`, but Go says `pending`. The client
    spec stubs the wrong value, so it agrees with the bug.

**Slices.** Each slice leaves the repository green:

| # | Slice | Delivers |
|---|---|---|
| 8d.1 | Backend additions + overview + status board | decision 1, `JobState` fix, component-test harness, per-Space cards, status board, "Back up now", retention edit |
| 8d.2 | Setup wizard | target picker, ceremony + gate, schedule, done screen, resume |
| 8d.3 | Restore | snapshot picker, confirm, progress, link to `restore_folder` |
| 8d.4 | Recovery Key | replacement flow, "Check my Recovery Key", envelope download |

### Sub-phase 8d.4 plan — decisions taken before implementation

Only what the 8d decisions leave open. Settled with the user before any code.
Three things the survey found shaped the options:

- `cmd/decrypt` reads a Take-Out directory and nothing else. It has no way to
  take an envelope from anywhere but the Take-Out's own `recovery.ocbke`, and a
  Take-Out forced without an envelope ignores one dropped in later. Before this
  slice, a downloaded envelope would have had no consumer.
- Rotation writes the state store only. The target's copy of the envelope, and
  so every Take-Out, is refreshed at the **next backup run**. Until then the
  old key still opens a fresh Take-Out and the new one does not.
- The state store has no compare-and-set (#16), but the service is a single
  instance (#16), so a lock inside the process makes a compare-then-write real.

1. **An ambiguous rotate failure is resolved by testing both keys against what
   the server stored.** For offline, timeout and 5xx, the old and the new key
   stay in component state, and "Check again" re-fetches the envelope:
   - the new key opens it: the rotation landed; continue to the done screen;
   - the old key opens it: nothing landed; the new key is discarded, the user
     is told to throw it away, both keys are forgotten, and the flow starts
     again;
   - neither opens it: someone else replaced the key; both are forgotten and
     the page says so.
   A fetch that fails, or an envelope that is not one, leaves the state
   uncertain. It is never read as an answer about a key. Same reasoning as
   8d.2 decision 5.
2. **Rotation carries a precondition.** The rotate body gains a required
   `replaces_sha256`: the hex SHA-256 of the envelope the browser unwrapped.
   That is a hash of ciphertext every member may already read, not key
   material. The handler holds a per-Space lock, compares the hash with the
   stored envelope, and answers 409 `conflict` if they differ. The page reads
   that as "someone else replaced the Recovery Key". Without it, two managers
   rotating at once both get 200 and the loser keeps a key that opens nothing.
   The rejected alternative, re-fetching after the POST, detects the race only
   after the winner's envelope has been overwritten.
3. **Two routes.**
   - `/space/:spaceId/recovery-key` is for any member: "Check my Recovery
     Key", the download, and the lost-key wording. For a manager it links to
     the replacement.
   - `/space/:spaceId/recovery-key/replace` is the rotation. Below manager it
     says that a manager has to do this.
   The board links to the first for every member once the Space is set up.
   Both are lazy chunks. The crypto stays out of the overview and board chunks.
4. **The download is the raw envelope, named `recovery.ocbke`, and
   `cmd/decrypt` gains `-envelope <file>`.** The flag overrides the Take-Out's
   own envelope and gets the same checks: it must be an envelope, from a
   version this build reads, and an RK wrap. It covers a Take-Out made between
   a rotation and the next run, and a Take-Out forced without an envelope. The
   file is built as a `Blob` with an object URL that is created and revoked
   locally. Nothing crosses the network except the existing GET. The name
   matches the Take-Out's, so the file also drops into one as it is.
5. **After a rotation the done screen states the delay and offers "Back up
   now".** It says that existing backups stay readable and nothing is
   re-uploaded, and that the old key stops working once the next backup has
   run, which the button starts. After that, the old copy should be destroyed.
   The rejected alternative, republishing from the rotate handler, would put
   target credentials into the API request path.
6. **What "Check my Recovery Key" reports:**
   - decode refuses the input: "this is not a Recovery Key, check for typos";
   - it decodes but does not open the envelope: "this key does not open this
     Space's backups", plus the lost-key wording;
   - the envelope cannot be fetched or parsed: "could not check", never
     "wrong key".
   Nothing about the check is sent anywhere.
7. **The lost-key wording** (there is no escrow, setup cannot be re-run, and a
   replacement needs the current key) appears on the check page, in the
   "does not open" result, and at the start of the replacement.
8. **The replacement has the same gate as setup.** The new key is shown once,
   and two random groups must be typed back before the POST. The gate logic
   and the key display move out of the wizard into shared code, because a
   second caller now needs them.
9. **Key hygiene, as for the wizard.**
   - Both machines are pure TypeScript under `src/recoverykey/`.
   - Lint bans storage there and in both views, and bans Vue and OpenCloud
     imports from the machines.
   - The Data Key never reaches the flow. `performRecoveryKeyRotation` recovers
     it, re-wraps it and zeroizes it internally.
   - The old key is held only until its purpose is settled: until the
     rotation succeeds, fails outright, or the ambiguous case is resolved.
   - Every key is forgotten on dispose.

### Sub-phase 8d.4 outcome (implemented, issue #35)

The Recovery Key page and the replacement landed as planned. The board links
every member of a set-up Space to `/space/:spaceId/recovery-key`: "Check my
Recovery Key", the key file and the lost-key facts. Managers get a link from
there to `/space/:spaceId/recovery-key/replace`. The machines are
`recoverykey/check.ts` and `recoverykey/rotation.ts`, and the views are
`RecoveryKeyView.vue` and `ReplaceRecoveryKey.vue`. The plan left some things
open:

- **The precondition is built by the crypto, not by the view.**
  - `performRecoveryKeyRotation` now returns a request with `replaces_sha256`,
    computed from the envelope it unwrapped (`envelopeDigest`, noble's SHA-256,
    checked against WebCrypto).
  - The handler refuses anything but 64 lowercase hex characters before it
    touches the store.
  - The lock is a `sync.Map` of mutexes keyed by Space id, held only by
    rotation.
  - A test sends eight rotations of the same envelope at once. Exactly one gets
    200, and the stored envelope is the one that was answered 200.
- **`decrypt -envelope`.**
  - `takeout/decrypt.Options` gained `EnvelopeFile`.
  - `ListSnapshots` now takes `Options` too, rather than growing a fifth
    positional argument. That changed its signature; `cmd/decrypt` was its only
    caller.
  - Two new errors: `ErrBadEnvelopeFile` (missing, unreadable, not an RK
    envelope) and `ErrEnvelopeFileMismatch` (it opens with the key but the
    repository does not open with what it holds).
  - Neither error names the file's path.
  - `cmd/decrypt`'s usage text says when to use the flag.
- **Shared code, because a second caller arrived:**
  - The gate (`pickGateGroups`, `gateMatches`), `cryptoRandomInt` and
    `nextFrame` moved to `recoverykey/gate.ts`. `wizard/machine.ts`
    re-exports them.
  - The key display and the gate form are now components
    (`RecoveryKeyDisplay.vue`, `RecoveryKeyGate.vue`) and the wizard uses them.
  - `asApiError` and the "may have landed" rule (`mayHaveLanded`) moved to
    `api/errors.ts`, with the wizard, the restore flow and the rotation as
    callers.
  - `LostKeyNotice.vue` holds the lost-key wording once.
- **The check answers a typo before any request.** Decode is cheap, so
  "malformed" costs no GET and no Argon2id. The check gets the key as an
  argument and keeps it nowhere. The typed value lives only in the view's input.
- **A download is checked before it is saved.** The page refuses to save bytes
  that are not a Recovery Key envelope: the base64 must decode, the header must
  parse, and the kind must be RK. A broken file would otherwise be found out on
  the day it is needed.
- **Deviation — the object URL is revoked 30 seconds after the click, not
  immediately.** Some browsers start reading the Blob after `click()` returns
  and fail the download if the URL is already gone. The Blob is ciphertext any
  member may read.
- **Deviation — the lost-key wording appears once per screen.** On the check
  page it is a block of its own. When the answer is "does not open", the block
  moves into that answer instead of appearing twice.
- **The current key is kept in the input while it may need correcting.** For a
  typo or a wrong key the user fixes it in place. Once it has opened the
  envelope, the input is cleared, and the machine holds the key until the
  rotation is settled.
- **Bundle.**
  - The crypto is now one shared chunk (`gate-*.mjs`, 21.5 kB gzip), used by
    the wizard, the check page and the replacement page. Before this slice it
    was inside the wizard's chunk (26.7 kB).
  - The pages themselves: wizard 4.9 kB, check page 2.9 kB, replacement
    4.0 kB.
  - The overview (1.9 kB), the board (4.0 kB) and the restore page (3.6 kB)
    import none of it. This was checked in the built chunks' imports.
- **Lint.** The storage ban now covers `src/recoverykey/**`, both new views
  and both key components. The Vue/OpenCloud import ban covers
  `src/recoverykey/**`. Both were verified by mutation, in every covered file.
- **Mutation checks.** Each of these made the suite fail:
  - skipping the gate;
  - keeping the keys after settling;
  - reading an ambiguous POST as success;
  - putting the old key into the request;
  - reading "neither key opens" as "nothing landed";
  - reading an unwrap that throws as "wrong key";
  - not checking the envelope kind before a download;
  - keeping the checked key in state;
  - hashing the new envelope instead of the old one;
  - offering replacement to viewers.

  On the Go side, removing the lock or the comparison each failed the
  concurrent-rotation test, and ignoring `EnvelopeFile` failed four tests.
- **gitleaks.** `dir .` flagged the minified `web/dist` output, which is
  gitignored. A minified assignment of the two key fields in the rotation
  machine matches the generic-api-key rule. CI's history scan never sees `dist/`. With `dist/`
  removed, both `dir` and `git` scans are clean. Nothing was allowlisted.
- **Tests:** 486 web tests (was 397), in 32 files. Among them:
  - "the Recovery Key reaches no API call" sweeps for the check machine, the
    rotation machine and both views, covering the old key and every new key,
    whole, bare and per group;
  - checks that the old key never appears in any state the machine emits.

  Go: the precondition (missing, malformed, stale, concurrent), and `decrypt`
  with an envelope file after a rotation, for a Take-Out without an envelope,
  for bad files and for a file from another Space.
- **Not verified:** the pages in a real browser against a real backend (8f).
  Also not verified: the download in real browsers, and the clipboard.
- **Findings, not fixed (for the user to decide):**
  - **Setup has the race rotation had.** Two concurrent `POST
    .../backup/setup` calls can both pass `assertNotConfigured` and both write.
    The Space could then end up with one caller's recovery envelope and the
    other's server envelope, which is #17's orphaning failure through a race.
    The same per-Space lock around "check, write both" would close it.
  - **`README.md`'s status paragraph is stale.** It still says "There is no
    user interface yet", although 8c–8d have landed.
  - The known leftovers are unchanged:
    - the scheduler does not check keys;
    - `performSetupCeremony` does not wipe the Data Key when self-verification
      fails;
    - the board's inline errors are not `ActionError`;
    - restore folders are named after the restore's start time, not the
      snapshot's.

### Sub-phase 8d.3 plan — decisions taken before implementation

Only what the 8d decisions leave open. Settled with the user before any code.

1. **One job can be read by id, and only within its own Space.** Nothing in
   the API reads a single job, so a view that started a restore could follow
   it only through `/backup/status`. But `current_job` is whatever runs *now*,
   and that may be a backup that started straight after. So the backend gains
   `GET /spaces/{id}/backup/runs/{jobId}` for viewers and up. A job from
   another Space answers the same 404 as an id that never existed.
   The lookup goes through the Space's own history listing, not the store's
   process-wide id index. That way it is scoped by construction, and it cannot
   miss a job that a different process created after its index was built.
   The rejected alternative was to poll status and then search `/backup/runs`
   for the id. It works until a scheduled backup starts at the wrong moment,
   and then the page shows someone else's run as theirs.
2. **The folder is linked through the host's own route helpers, with the path
   as text when that is not possible.** `useSpacesStore().getSpace(id)`,
   `createFileRouteOptions` and `createLocationSpaces` come from `web-pkg`,
   which is a host singleton, so they cost the bundle nothing. If the host has
   not loaded that Space, the page names the folder in words instead of
   linking to it. It does not make up a URL. Whether the link opens the right
   folder in a real browser is 8f's to prove.
3. **Restore has its own route, `/space/:spaceId/restore`.** It is a lazy
   chunk like the wizard. The board links to it for every member once the
   Space is set up, viewers included (decisions.md #7; the owner kept restore
   at viewer in R2). The page goes: pick a backup, confirm, watch progress,
   then open the folder. The route carries the Space id and nothing else, so
   a reload lands on the picker and a running restore still shows on the
   board.
4. **The confirm step states the cost.** It shows the file count and size of
   the chosen backup. It says the copy goes into a new folder, that nothing is
   overwritten, and that the copy uses the Space's storage.
5. **Run history links each restore to its folder, failed ones included.**
   `restore_folder` is written when the job starts because a failed restore
   can leave a partial folder behind (8d.1). The history is where someone who
   left the page finds it again.
6. **A 409 reads as "another backup or restore is running" on the restore
   page.** The shared wording, "a backup is already running", is wrong when
   the run in the way is a restore.

### Sub-phase 8d.3 outcome (implemented, issue #35)

The restore flow landed as planned: pick, confirm, follow, then a link to the
folder. The machine is in `web/src/restore/flow.ts` and
`views/RestoreView.vue` renders it at `/space/:spaceId/restore`. The board
links there for every member of a Space that is set up. The plan left some
things open:

- **`jobs.Store` gained `GetInSpace`.** Plan decision 1 says the lookup goes
  through the Space's history. That became a store method rather than a filter
  in the handler. The state store lists the Space's keys, matches the id in
  the key name, and reads one document. Before serving it, it checks that the
  record's own `SpaceID` matches the Space it is filed under. The memory store
  checks the same field. A contract test runs both stores. A second test
  builds the reader's id index first, then creates the job through a second
  store over the same state. `Get` misses that job and `GetInSpace` finds it.
- **The handler refuses anything that is not lowercase hex of at most 64
  characters before calling the store.** It answers with the same 404 body as
  a job that does not exist, and a test compares the bodies. This is defence
  in depth only: the store's lookup is by key name, not by path. The role
  table gained a row, so the credential and role sweeps cover the route.
- **The flow never offers a second restore while one is running.** The plan
  only covered a reload. Three more cases do the same thing:
  - A 409 on the POST.
  - A POST that got no answer, or a 5xx.
  - A status read that shows a restore already running.

  In each case the page re-reads status and follows the running restore,
  whether this request started it or another tab did. The run lock already
  prevents two restores at the same time. This prevents a second full copy
  afterwards, which would cost the member's storage a second time. It is the
  same reasoning as the wizard's ambiguous-setup rule (8d.2 decision 5).
- **The backup that no longer exists.** A 404 on the POST means retention
  removed the backup between the listing and the click. The page lists the
  backups again and explains why. It does not branch on the server's message:
  `not_found` from this route can only be the Space or the snapshot, and the
  caller has just read the Space.
- **Deviation — a run whose record goes missing becomes `lost`.** The page
  stops polling and points to the recent activity. It does not retry the
  lookup every five seconds for as long as the page stays open. The plan
  did not cover this. In practice the job store only removes finished runs
  older than a year.
- **The folder is checked before it is linked.** `isRestoreFolder` accepts
  exactly `Restore/<name>`. Anything else is not linked and not shown: `..`,
  a nested path, an absolute path, a backslash, or a different root. The
  server is the only writer of that field, but the value turns into a link
  inside someone's Files app. The route builder is tested against web-pkg's
  real `createFileRouteOptions` and `createLocationSpaces`, so a change in how
  the host builds Files routes breaks a test here rather than producing a
  dead link.
- **`ActionError` takes optional wording.** `restoreErrorTitle` and
  `restoreErrorAdvice` in `api/errortext.ts` change only `run_in_progress`.
  Every other code keeps the shared text.
- **Bundle:** the restore page is its own 3.6 kB gzip chunk with no crypto in
  it. The spaces store and route helpers are loaded through the host's shared
  `web-pkg`, as `useAuthStore` already is.
- **Mutation checks.** Each of these made the suite fail:
  - dropping the Space check in either store;
  - reading through the id index;
  - dropping the check that the record matches the Space it is filed under;
  - skipping the status re-read after an ambiguous POST;
  - not following a restore that is already running at load;
  - accepting any folder path.
- **Verified:** `go test ./...`, and web lint, typecheck, tests (397, was 326)
  and build. **Not verified:** that the Files link opens the right folder in a
  real browser. That needs a loaded host spaces store and a real Space id, and
  both are 8f's to provide. The one assumption is that the service's Space id
  equals the host's drive id. The fixture's `seed.sh` already relies on this,
  because it passes the graph drive id to the service as the Space id.

### Sub-phase 8d.2 plan — decisions taken before implementation

Only what the 8d decisions above leave open. Settled with the user before any
code.

1. **`enabled` becomes true at the schedule step, and not before.** Measured:
   the scheduler's eligibility is `Enabled && TargetID != ""` and never looks at
   keys. A Space bound and enabled without keys therefore creates a failed job
   at every due time ("backup is not configured for this space"), fires a
   `run_failed` notification each time (that kind has no repeat limit), and goes
   stale after 48h. So the target step sends `enabled: false`, and the schedule
   step — reachable only once keys exist — sends `enabled: true`. That PUT is
   what "setup complete" means. The resume rule follows: `configured &&
   keys_configured && !enabled` resumes at the schedule step. A Space that is
   all three is set up; the wizard shows the done screen, not a form.
2. **An editor stops at the key step.** They bind the target and meet "a
   manager of this Space has to finish setup". They do not reach the schedule
   step, because that is where `enabled` turns on (1). The manager who opens the
   wizard later resumes at the key step and continues to the schedule.
3. **The service's timezone is exposed by the API.** `GET /backup/status` and
   `GET`/`PUT /backup/schedule` gain `timezone`: the IANA name the scheduler
   reads presets in. It is omitted when the service cannot name it (no `TZ`,
   no `SCHEDULE_TIMEZONE`, only `/etc/localtime`) or runs no scheduler; the UI
   then says "server time" without a name. The rejected alternative — generic
   wording only — would have left `next_run` (UTC) and the preset hour (service
   zone) in two zones with nothing on screen saying which.
4. **Argon2id stays on the main thread, behind a busy state.** The wizard
   renders "Creating your Recovery Key…" and yields a frame before the ceremony
   starts, so the page says why it froze. A Web Worker is deferred: resolving a
   worker URL from inside the federated `.mjs` the host loads is unverified, and
   the default parameters stay. Never below the floor (#19).
5. **An ambiguous setup failure is resolved by testing the saved key, not by
   guessing.** The Data Key is zeroized when the POST settles, either way, so a
   retry cannot resend it. For offline, timeout and 5xx the POST may have
   landed. The Recovery Key — which the user has already proven they saved —
   stays in component state, and "Try again" re-reads status:
   - no keys: a new ceremony, and the user is told to discard the key they saved;
   - keys: fetch the recovery envelope and unwrap it locally with the saved
     key. It opens: the POST landed, continue to the schedule. It does not:
     someone else set the Space up, which is the "already protected" state.
   Nothing extra crosses the network; the envelope is ciphertext any member may
   read.
6. **The wizard's machine is pure TypeScript in `web/src/wizard/`**, driven by
   injected dependencies (the API subset, the ceremony, a random source). The
   Recovery Key lives in the machine's state, and the machine is owned by one
   component instance: never a store, a route or storage. It is cleared on
   completion and on unmount.
7. **Found during the survey, fixed here:** `PUT /backup/schedule` accepted
   unknown fields and read an absent `enabled` as false. The role table's own
   row sent `{"schedule": …}`, which passed authorization while resetting the
   cron and disabling the Space. The endpoint now refuses unknown fields, as
   `PATCH /backup/config` does, and the row sends a real preset.

### Sub-phase 8d.2 outcome (implemented, issue #35)

The wizard landed as planned: target, then key ceremony and gate, then
schedule, then the done screen, resuming from `/backup/status`. The machine
lives in `web/src/wizard/machine.ts` and `views/SetupWizard.vue` renders it.
The board and the overview card link into it. Notes on what the plan did not
say:

- **Deviation — there is no "pick space" step.** The flow list begins with
  "pick space (personal preselected)". The wizard is opened from a Space's card
  or board instead, at `/space/:spaceId/setup`, so the Space is already chosen.
  The route carries the id and nothing else: no step, and never key material.
- **The entry point is one rule, shared.** `status/setupaction.ts` decides what
  a Space offers: "Set up backup", "Finish setup", "Turn on scheduled backups",
  or, for an editor at the key step, the sentence that a manager has to finish.
  It is built on the wizard's own resume rule (`status/setupstep.ts`), so the
  link and the page it opens cannot disagree. The card never shows the
  needs-a-manager case as a link, because a link to a page that says "you
  can't" would be a dead end. A Space whose runs are off gets "Turn on scheduled
  backups". That covers a wizard left at its last step and also a Space someone
  paused on purpose. For both, turning it on is exactly what the wizard's last
  step does.
- **The resume rule is not in the machine, and that is about bundle size.** It
  started out in `wizard/machine.ts`. That put the whole ceremony (hash-wasm,
  noble) into the overview's chunk, because the card imports the rule. Now it
  is in `status/`. Measured: the crypto ships only in the wizard's chunk
  (26.7 kB gzip), and the overview (1.9 kB) and the board (3.8 kB) carry none
  of it.
- **The gate matches with decode's tolerance, and the matcher is in
  `src/crypto`.** `normalizeRecoveryKeyInput` and `recoveryKeyGroups` share one
  character mapping with `decodeRecoveryKey`. A gate stricter than decode
  would refuse a copy that recovery accepts; one looser than decode would pass
  a copy that recovery refuses. A test checks that a lower-cased key with
  look-alikes normalizes to what decode reads.
- **`recoveryKeyOpens` is new in `src/crypto`.** It answers decision 5's "does
  the saved key open what the server stored" and zeroizes the recovered Data
  Key before returning. A stored envelope that is not an envelope at all is
  thrown, not reported as "no": that tells us nothing about the key, so the
  wizard stays uncertain rather than declaring the Space someone else's. 8d.4's
  "Check my Recovery Key" can use the same function.
- **Storage is banned by lint, not by care.** `src/wizard/**` and
  `SetupWizard.vue` may not touch `localStorage`, `sessionStorage` or
  `indexedDB`, either as globals or as properties. `src/wizard` may not import
  Vue or OpenCloud. Verified by mutation.
- **The clipboard is the one place the key leaves the page.** The plan asks for
  a copy button. The OS clipboard is local, but it outlives the page. That is
  accepted as what "copy" means, and nothing in the wizard reads the clipboard
  back.
- **`Scheduler.Timezone()` reports what Go can name.** With `TZ` or
  `SCHEDULE_TIMEZONE` set, that is the IANA name. With only `/etc/localtime`,
  Go calls the zone `Local`, which tells a user nothing, so the field is left
  out. Without a scheduler (the pipeline is disabled) it is left out too.
  `next_run` stays UTC on the wire and is shown in the browser's zone. It is an
  instant, so that is correct; the zone label applies to the preset's hour.
- **Findings, not fixed (for the user to decide):**
  - The scheduler still does not check keys. The wizard never enables a Space
    that has none (decision 1). A client going straight to the API still can,
    and that Space then fails every night. A backend guard (skip Spaces without
    keys, with no job and no notification) would close this. It was offered
    and not chosen for this slice.
  - `performSetupCeremony` zeroizes the Recovery Key's secret when
    self-verification fails, but not the Data Key. It never returns it, so the
    key is unreachable, but it is not wiped.
  - The board still renders its inline action failures by hand. The new
    `ActionError.vue` could replace them.
- **Verified against the fixture:** `make test-opencloud` passes and
  `make web-install-fixture` registers and serves the new bundle. Not verified:
  the wizard in a browser against a real backend and a real token (8f), and how
  long Argon2id at the default parameters holds a slow device's main thread.
- **Tests:** 326 web tests (was 236), in 21 files, including:
  - the machine: step order, resume from every server state, the target cases,
    the gate, 409, ceremony failure, the ambiguous-failure resolution, and Data
    Key zeroization on success, on every failure class and on dispose;
  - component tests for roles, the picker, the done screen, German and weekly
    schedules;
  - two "the Recovery Key reaches no API call" sweeps, which check the whole
    key, its bare form and every group.

  Mutation checks confirmed that a skipped gate and a Data Key that is not
  wiped both fail the suite. On the Go side there are tests for the timezone
  field (named, unnamed, no scheduler) and for PUT schedule refusing unknown
  fields. The role-table row now sends a real preset.

### Sub-phase 8d.1 outcome (implemented, issue #35)

The backend gaps are closed. The overview has per-Space cards, and there is a
status board with "Back up now" and a retention edit. The component-test harness
came in with the first view that needed it. Notes on what the plan did not say:

- **The caller's role costs a group lookup more often than before.** `permits`
  can stop at a threshold. `role` cannot, because a group grant may raise a
  direct viewer to editor. So `GET /spaces` now resolves groups whenever a Space
  has a group grant and the caller is below manager. A broken resolver used to
  hide only Spaces the caller reached *through* a group. It now fails the whole
  listing for such a caller (502/503). That is #20 applied as written:
  understating the role would send someone to ask for a permission they already
  hold. Managers and owners still trigger no lookup, and that is pinned by a
  test.
- **The staleness rule lives in `notify.StaleRule`, and the monitor's window
  grew.** The monitor used to read 20 backup records and the status board 50,
  so after a run of failures the two could disagree about when the last success
  was. Both now read `notify.StaleLookback` (50).
  `TestBackupStatus_StalenessAgreesWithTheMonitor` drives the real monitor over
  the same stores and compares its notification with the board's flag in six
  scenarios. A mutation check confirmed it catches a board that never reports
  stale.
- **The key store is wired even without `SRW_KEY`.** The status endpoint now
  requires it, because "are keys configured" read as `false` when the store was
  missing would offer a setup the server must refuse (#17). The deployment used
  to wire it only alongside the SRW wrapper. Now it is always wired, and setup
  still refuses with 503 without the wrapper. A side effect: without `SRW_KEY`,
  `keystatus`, `recovery-envelope` and Recovery Key rotation now answer where
  they used to 503. All three touch ciphertext only. Rotation is RK-side only
  and needs no SRW key.
- **`restore_folder` is written when the job is created, not when it
  finishes.** A failed restore can still leave part of a snapshot behind, and
  the member needs to know where. The `jobs` package comment said a job record
  holds no paths. It now says "no paths from the user's data", because this one
  is a name the service chooses.
- **`PATCH /backup/config` refuses unknown fields.** A client that sends
  `schedule` in a patch would otherwise get 200 and a change that never
  happened. A patch with no fields is accepted and changes nothing.
- **The overview still makes one status request per Space.** Decision 1 removed
  the *second* request per card (`keystatus`) and the role guessing. It did not
  batch statuses, because at household scale N parallel reads is not a
  problem. Each card shows its own failure.
- **No notification list on the board.** The plan's "warnings mirroring Phase 6
  notifications" is met by `stale`/`stale_since` and `last_error`, which carry
  the same facts the two member notification kinds do. The notification
  *messages* are English server text, and a list of them would have been the
  one untranslatable block on the page. The endpoint stays; a view can adopt it
  if the notification kinds grow.
- **i18n gained a check.** Interpolated strings (`%{when}`) arrived with the
  dates, so `translations.spec.ts` now also fails when a German entry drops or
  renames a placeholder. A mutation check confirmed it.
- **The host stand-in is `src/test/host.ts`.** It holds the gettext plugin,
  `oc-*` stubs that render plain HTML, and a `router-link` stub. Views reach
  their Space id through route `props: true`, so no spec needs a router, and
  the remote still imports no `vue-router` value.
- **Verified against the fixture:** `make test-opencloud` passes (the job-record
  field round-trips through the CS3 state store). `make web-install-fixture`
  registers the new bundle and serves its entry chunk. Not verified: a real
  user token against the new fields, which needs the OIDC browser flow that 8f
  brings.
- **Tests:** 236 web tests (was 143), in 18 files. Go tests for every new field
  and route, including `PATCH` in the role table, so the credential-leak and
  role sweeps cover it.

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
