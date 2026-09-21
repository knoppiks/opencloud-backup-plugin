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

### Sub-phase 8a outcome (implemented)

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
