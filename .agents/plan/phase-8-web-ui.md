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

## Risks / notes

- Argon2id in the browser (wasm) + exact parameter match with Go is the
  fiddly part — build the interop test **first**.
- Extension API surface of OpenCloud Web may shift between versions; pin the
  tested OpenCloud version in compose and document the supported range.
