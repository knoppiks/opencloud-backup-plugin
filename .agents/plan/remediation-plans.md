# Remediation Plans — draft, to be merged before Phase 7

Companion to [`review-2026-09.md`](review-2026-09.md). Each plan below is
self-contained: a problem statement, the options considered, a recommended
option, concrete tasks, tests that prove it, and the docs it touches. Plans
are numbered `R1..R9`; where a plan requires a decision from the owner, the
decision is called out as **DECISION NEEDED** and the options are laid out so
the plan can be merged with one option struck.

Ordering below is by severity, not by effort. A suggested sequencing is at the
end.

Rules that apply to every plan: no hand-rolled crypto; key material never
logged; every change ships with tests; `decisions.md` is updated when a plan
amends a locked decision.

---

## R1 — Durable key envelopes, targets and Space config (F1)

### Problem

The only server-side copy of every SRW-wrapped Data Key lives in one document
per Space behind a non-atomic delete-then-write, in a Space that is not backed
up, whose overwrite semantics were never verified against reva.

### Options

**Option A — append-only key documents (recommended).**
Never replace a key record; write a new immutable document per version and
read the newest.

- Layout: `keys/<space-id>/<nanos>-rk`, `keys/<space-id>/<nanos>-srw`
  (one envelope per file; nanos zero-padded so lexical = chronological, as
  `jobs/` already does).
- `PutRK`/`PutSRW` become create-only writes; `GetRK`/`GetSRW` read the
  lexically last entry of each kind. `Status` derives from the two newest.
- Old versions are never deleted by the service (they are ciphertext, tiny,
  and are the audit trail for rotation). A slow-cadence prune may drop all but
  the newest N days later than the retention window, if ever.
- Same treatment for `targets/<id>` (credentials) and `spacecfg/<space>`:
  `targets/<id>/<nanos>`, `spacecfg/<space>/<nanos>`; read newest.
- `state.Store` gains nothing new; this is a layout change inside the three
  sub-stores plus a shared "newest-of-prefix" helper in `pkg/state`.
- Cost: `List`-then-`Get` instead of `Get` (one extra `ListContainer` per
  read). Acceptable at family scale; cacheable per process if it ever matters.

**Option B — write-new-then-delete-old.**
Keep single-document semantics but upload to `keys/<space>.<nanos>`, then
delete the previous, then rely on "newest wins" on read. Functionally a
subset of A with more edge cases (two documents visible during the window,
reader must still pick newest). Not recommended.

**Option C — embedded SQLite on a small PVC** for keys/targets/spacecfg only,
keep CS3 for jobs/leases/history/notifications. Real transactions for exactly
the records that matter. Reintroduces a second storage system and a volume,
which decision #16 explicitly rejected. Only worth it if Option A proves
insufficient. Not recommended for v1.

**Option D — publish the SRW envelope to S3 alongside the RK envelope.**
Orthogonal to A/B/C; addresses "SRW exists nowhere else" and "state Space
lost ⇒ unattended backups cannot resume". **DECISION NEEDED**: this widens
what an attacker with S3 read access *and* the `SRW_KEY` can decrypt (today
they would also need the state Space). Since the `SRW_KEY` already lives in the
same cluster as the service account that reads plaintext Spaces, the marginal
exposure is small, but it is a trust-model change and must be recorded in
`decisions.md` if adopted. Recommendation: adopt, publish to
`<prefix>keys/<space-id>/server.ocbke` on every run next to `recovery.ocbke`.

### Tasks

1. Add `state.Newest(ctx, store, prefix)` helper (list, pick lexically last,
   get) with unit tests on the memory store.
2. Rework `pkg/keys/state.go` to append-only layout; keep `Store` interface
   stable; migrate in-place by treating a legacy `keys/<space>` document as
   version 0 (read it if no versioned entries exist; write the first versioned
   entries; never delete the legacy one).
3. Same for `pkg/targets/state.go` and `pkg/spacecfg/state.go`.
4. Make `cs3state.Put` refuse to replace by default (`ErrAlreadyExists` bubbles
   up) and add an explicit `Replace` used only by the record types that are
   genuinely re-derivable (leases, job records).
5. Write an integration test against the OpenCloud fixture that establishes
   **what reva actually does** on `InitiateFileUpload` of an existing path,
   and pin it. If reva overwrites, drop the delete branch in `cs3state.Put`
   and note revision growth; if it returns `ALREADY_EXISTS`, keep the branch
   for `Replace` only. Either way, add a trash-purge or revision-purge task if
   the fixture shows growth.
6. Validate the state Space at startup: refuse to start if `ListSpaces`
   reports the Space has any grant or any group grant, or if its type is
   `personal` (`cs3state.resolve`). Log the Space name, never its members.
7. (Option D, if adopted) extend `takeout.PublishTo` to accept `WrapSRW` for a
   distinct object name, and `Runner.publishEnvelope` to publish both.
8. Update `decisions.md` #16 constraints: "key, target and Space-config
   records are append-only and never replaced; only re-derivable records use
   replace".

### Tests

- Contract test: `PutRK` twice yields two versioned documents, `GetRK` returns
  the newer, the older still exists.
- Crash simulation: a fake store that fails after the first write of a
  two-write sequence must leave the previous version readable.
- Integration (OpenCloud fixture): overwrite semantics pinned; startup
  refuses a Space with a member.
- Phase-6 exit-criteria tests re-run against `cs3state` when the fixture is
  present, not only against the memory store.

### Docs

`decisions.md` #16 amendment; `deploy/secret-wrap-keys.yaml` wording; a
runbook section in `README.md` for provisioning the state Space.

### Outcome (implemented, issue #12)

Option A + Option D, with three deviations and one new finding.

- **Deviation — version documents live under new prefixes**
  (`keyenvelopes/`, `targetrecords/`, `targetgrantlists/`, `spaceconfigs/`)
  rather than under the existing ones. The plan's `keys/<space-id>/<nanos>-rk`
  cannot work: `keys/<space-id>` is already a *file* in the pre-versioned
  layout, and CS3 cannot have a file and a folder at the same path. Separate
  prefixes make the migration purely additive — the legacy document is read as
  version 0 and never touched.
- **Deviation — the kind is a path segment** (`.../rk/<nanos>`), not a name
  suffix, so one generic `state.Versions[T]` helper serves all four stores.
- **Deviation — target grants are append-only too.** Not in the plan, but
  `Put` becoming create-only forced the decision, and silently withdrawn access
  is a bad enough failure to be worth the one extra line.
- **Finding — reva overwrites silently.** `InitiateFileUpload` over an existing
  path succeeds on OpenCloud 7.3.0; it does not return `ALREADY_EXISTS`, so the
  delete-then-write branch never fires. `cs3state.Create` therefore checks the
  parent folder itself before writing rather than relying on the upload to
  refuse. Pinned by `TestIntegration_CS3StateOverwriteSemantics`.
- **Finding — overwriting grows revisions, and nothing reclaims them.** Five
  writes to one path left four revision nodes on the fixture's disk. Leases are
  renewed every few minutes, so lease and job documents accumulate revisions
  indefinitely. Small, but unbounded. **Not addressed here** — it needs either a
  revision-purge task or a documented OpenCloud-side cleanup, and it should be
  its own plan item (candidate: fold into R6, which already touches lease
  write frequency).

---

## R2 — Key ceremony guards and rotation (F2)

### Problem

Setup can be re-run by any member and silently replaces the DK; rotation
functions exist with no operational path; the server cannot verify the client
envelope.

### Plan

1. **`POST .../backup/setup` returns `409 Conflict` when the Space already has
   an RK or SRW envelope.** No override flag. Test: second call is refused,
   envelopes unchanged, S3 `recovery.ocbke` unchanged.
2. **New endpoint `POST .../backup/recovery-key/rotate`** for RK rotation
   without changing the DK. Client flow (Phase 8): fetch the current RK
   envelope, unwrap with the old RK in the browser, generate a new RK, re-wrap
   the *same* DK, POST the new envelope. Server: `Inspect` header, require
   `WrapRK`, append as a new version (R1). The DK is **not** sent on this
   path. Test: after rotation the SRW envelope still unwraps to the same DK
   and a backup run succeeds against the existing repository.
3. **SRW rotation as an operator CLI subcommand** `backupd rotate-srw` taking
   `SRW_KEY_OLD` and `SRW_KEY` from the environment: for every Space, unwrap
   with old, wrap with new, append. Refuse to run while the scheduler is up
   (same binary, so gate on a `--i-stopped-the-service` style flag or on a
   held global lease). Same for `rotate-tw`. Test: after rotation, runs
   succeed with only the new key configured.
4. **Client invariant, documented in `key-envelope-format.md` §5 and the
   Phase-8 doc:** before POSTing setup, the client must unwrap its own RK
   envelope with the RK it is about to show the user and compare the DK. The
   API cannot check this and says so in the handler comment.
5. **Argon floor at the API boundary:** reject `WrapRK` envelopes below
   `time=2, memory=32 MiB, lanes=1` (parameters, not a code change to
   `validateArgonParams`, which stays a DoS bound). Test: weak envelope → 400.
6. **Atomic setup** falls out of R1 if RK and SRW are written as two
   independent append-only documents and `Status` requires both; a partial
   setup is then visible and safely re-runnable *only* while `HasSRW=false`
   (i.e. the 409 in step 1 keys on "both present").
7. Reject `SRW_KEY == TW_KEY` at startup.

### Docs

`phase-3-keys.md` (rotation endpoints), `phase-8-web-ui.md` (client
invariant, rotate flow), `decisions.md` trust model (DK on the wire is stated
plainly: "the DK is sent to the server once, over TLS, at setup").

### Outcome (implemented, issue #14)

All seven steps, with one parameter change and two additions.

- **Argon floor set to `time=2, memory=19 MiB, lanes=1`** (OWASP's low-memory
  Argon2id baseline) rather than the plan's 32 MiB. The floor exists to reject
  trivially crackable envelopes, not to impose the server's preferred costs on a
  slow device, and the plan's figure would also have made every API key test do a
  32 MiB derivation. Salt length is checked too (`>= 16`). Lives in
  `pkg/keys/policy.go` as `MinArgonParams` + `CheckRecoveryEnvelope`; the DoS
  ceilings in `envelope.go` are untouched, as the plan required.
- **Addition — rotation logic is a package, not CLI code.** `pkg/rotate` holds
  `SRW` and `TW`; `cmd/backupd` only parses flags and wires the store. Both are
  **resumable**: a record that no longer opens with the old key but does open
  with the new one counts as already rotated, so an interrupted rotation is
  finished by re-running it. A record that opens with neither stops the run
  (`ErrKeyMismatch`) rather than being rewritten on a failed assumption. This is
  not in the plan text and is the difference between a rotation an operator can
  use and one they can only start.
- **Addition — `keys.Store` gained `Spaces()`, and `state` gained `Documents.IDs`
  / `Versions.IDs`.** An SRW rotation must visit every Space, and nothing could
  enumerate them; a Space missed by a rotation keeps an envelope that opens with
  a key about to be deleted. The `Versions.IDs` union covers pre-versioned
  records too, so a Space set up before R1 is not skipped.
- **Step 3's gate is both halves, as decided:** `-service-stopped` (the
  operator's assertion) *and* `jobs.ActiveLeases` (the machine's). Neither is a
  lock — there is still no CAS — and the doc comments say so.
- **Step 6 needed no work:** R1's independent append-only RK/SRW documents plus
  `Status.Configured = HasRK && HasSRW` already gave the "partial setup is
  visible and safely re-runnable" property the plan wanted.
- **Note for R3:** the rotate endpoint is member-gated like the rest of the key
  routes. When roles land it belongs in the "manager or owner" row alongside
  setup and restore, not with envelope retrieval.

---

## R3 — Membership: role, expiry, groups (F3)

### Problem

Presence in `grants` is treated as full authority regardless of role, expiry,
or group membership.

### Options

**Option A — role-gated actions (recommended).**
Parse the grant into a `Role` (`viewer | editor | manager | owner`) from the
reva permission set in the grant value, honour `grants_expirations`, resolve
`groups` via the caller's group claims or a graph lookup, then gate:

| Action | Minimum role |
|---|---|
| view status, list snapshots, fetch recovery envelope | viewer (any member) |
| run backup now, change schedule/retention | editor |
| key setup, RK rotation, restore into Space | manager or owner |

Rationale: restore writes into the Space; a viewer cannot write to the Space
through OpenCloud, so they must not be able to through the backup service.
Recovery-envelope retrieval stays "any member" (decision #7).

**Option B — any non-expired member for everything.**
Fixes expiry and groups only. Smaller, but leaves the viewer-can-restore hole.
Not recommended.

### Tasks

1. Pin the `grants` value shape against the OpenCloud fixture with an
   integration test (seed a project Space with a viewer, an editor and a
   manager; assert parsed roles). This is the highest-value task: the whole
   authorization model rests on an undocumented map.
2. `cs3.Space.Members` becomes `map[string]Member{Role, ExpiresAt}`; drop
   `roleLabel`.
3. Read `groups`; resolve the caller's groups either from the graph
   (`GET /graph/v1.0/me/memberOf`) with the caller's bearer, cached per
   request, or refuse group grants explicitly and document it. **DECISION
   NEEDED**: graph call per request vs. documented limitation. Recommendation:
   graph call; it is the same mechanism admin detection already uses.
4. `isMember` → `roleFor(space, subject, groups, now) (Role, bool)`; handlers
   call `requireRole`.
5. Cache `ListSpaces` per request (it is already called once per handler; make
   sure it is not called twice).

### Tests

Unit: expired grant denied; viewer denied restore and setup; editor allowed
run; group member allowed via group grant. Integration: the fixture-seeded
roles above.

### Docs

`phase-2-auth-spaces.md` (role table), `decisions.md` #7 clarification (any
member may *retrieve* the envelope; only managers may *reset* it).

---

## R4 — Kopia correctness: ignore rules and checkpoints (F4)

### Problem

Ignore rules are active inside user data; checkpoint manifests are surfaced
as snapshots and can defeat newest-snapshot protection.

### Plan

1. `u.DisableIgnoreRules = true` in `KopiaEngine.Backup`. Test: a Space
   containing `.kopiaignore` with `*` and a `CACHEDIR.TAG` still snapshots
   every file, and the marker files themselves are included.
2. Filter `IncompleteReason != ""` in every consumer: `List`, `findManifest`,
   `expiredManifests`, `Walk`, `RestoreAll`, `decrypt -list`, `decrypt`
   default selection. Put the filter in one helper (`completeOnly(mans)`) and
   use it everywhere; `decrypt` uses the same `pkg/snapshot` code path so it
   gets it for free.
3. After a successful `SaveSnapshot`, delete this run's checkpoint manifests
   explicitly (list manifests for the source with `IncompleteReason ==
   "checkpoint"` and `StartTime` within this run, `DeleteManifest` each). Do
   **not** call `policy.ApplyRetentionPolicy` — it is count-based and decision
   #10 forbids it, even though its zero-counter form would be a no-op for
   complete snapshots.
4. On a *failed* run, the same cleanup runs in a best-effort `defer` so that a
   failed long run leaves no incomplete manifest behind. Orphaned content is
   then reclaimed by maintenance (R7).
5. Make `CheckpointInterval` configurable on the engine (kopia caps it at 45
   min) so tests can drive it down to seconds.
6. `expiredManifests` protects the newest **complete** snapshot; incomplete
   ones are always eligible.

### Tests

- Backup with `CheckpointInterval = 1s` over a fake source that takes several
  seconds: `List` returns exactly one snapshot afterwards; no manifest with
  `IncompleteReason` remains.
- Same with an injected read failure after the first checkpoint: run fails,
  `List` is empty (or unchanged), `decrypt -list` on a take-out shows nothing
  new.
- `expiredManifests` with a checkpoint newer than a complete snapshot: the
  complete one is protected, the checkpoint is expired.

### Docs

`decisions.md` Phase-4 amendments: add "kopia ignore rules are disabled; a
Space's contents are never a policy input" and "checkpoint manifests are
transient and removed at run end".

---

## R5 — Credentials on disk, TLS, and deployment hardening (F5, F6, F9, F10)

### Problem

S3 secrets land in `repository.config`; the manifest allows two instances,
ships memory-state mode, and no TLS is stated; OIDC audience is optional.

### Plan

**Work dir**
1. Investigate whether kopia v0.23.1 can open a repository without
   `repo.Connect` writing a config file (e.g. building `repo.LocalConfig` in
   memory and calling the internal open path). If yes, do that. If no:
2. Mount `BACKUP_WORK_DIR` on `emptyDir: {medium: Memory}` with a
   `sizeLimit`, and refuse to start unless `BACKUP_WORK_DIR` is on tmpfs or
   `BACKUP_WORK_DIR_ALLOW_DISK=true` is set (check via `statfs` magic on Linux;
   warn-only elsewhere). Document the trade-off (cache lives in RAM; family
   scale is fine).
3. On startup, sweep and delete any leftover `kopia-run-*` directories.
4. Amend `decisions.md` #14 to state exactly where plaintext credentials can
   exist: process memory and a memory-backed work directory for the duration
   of a run.

**Deployment**
5. `strategy: {type: Recreate}` in `deployment-backupd.yaml`, with a comment
   pointing at decision #16's single-instance constraint.
6. `STATE_SPACE_ID` unset → **fatal**, unless `STATE_BACKEND=memory` is set
   explicitly. The manifest ships the env var with a placeholder that fails
   loudly, not an empty string.
7. Startup self-check for a second instance: on boot, write a
   `instance/<id>` document with a short TTL and refuse to start if another
   unexpired instance document exists that this process does not own. Not a
   CAS, so not a guarantee, but it turns "unsupported" into "usually caught".
8. `terminationGracePeriodSeconds` ≥ scheduler drain (30 s) + HTTP drain
   (10 s) + margin.
9. Fix `secret-wrap-keys.yaml` wording; uncomment and document
   `OC_SERVICE_ACCOUNT_*`.

**Listener and OIDC**
10. `OIDC_AUDIENCE` required (default to OpenCloud's `web` client id with a
    warning if the operator does not set it; **DECISION NEEDED** whether a
    default is acceptable or it must be explicit). Require `exp`. Add a
    negative cache for unknown `kid` (e.g. one refresh per minute).
11. Make startup tolerate IdP unavailability: retry JWKS discovery in the
    background and serve 503 on authenticated routes until it succeeds.
12. Add `ReadTimeout`/`WriteTimeout`/`IdleTimeout` on `http.Server`.
13. State in `README.md` and the manifest that the listener is plain HTTP and
    **must** sit behind TLS-terminating ingress on the same origin as
    OpenCloud; optionally add `TLS_CERT_FILE`/`TLS_KEY_FILE` for bare deploys.
14. Reject `SRW_KEY == TW_KEY`.
15. Retention floor: `retention_days >= 7` (constant, documented; Phase-7
    item moved forward).

### Tests

Unit: audience/exp cases; retention floor; `SRW==TW` rejected; startup fatal
without state Space. Integration: leftover work dir swept. kubeconform already
validates manifests.

---

## R6 — Scheduler and runner robustness (F8)

### Problem

Several silent-stop paths: a lost `Finish` write wedges a Space forever;
scheduled runs are unbounded; corrupt config documents vanish silently.

### Plan

1. **Bound scheduled runs.** `RunScheduled` wraps its context with
   `runTimeout()` exactly like `StartBackup`. Test: a fake engine that blocks
   is cancelled; job marked failed with "timed out".
2. **Body-read deadline** on data-gateway GETs: wrap `resp.Body` in a reader
   that resets a deadline on each `Read` (idle timeout, e.g. 2 min), so a
   stalled stream fails rather than hangs. Same for uploads.
3. **Orphaned `running` jobs.** `Recover` additionally calls
   `Jobs.ListRunning` and marks failed any running job whose Space has no
   live lease and is not held locally. Test: simulate `Finish` failure +
   `release`; the next `Recover` fails the job; the Space is due again.
4. **`Finish` write failure is retried** (three attempts, backoff) before
   giving up; and on give-up, the lease is *kept* rather than released so
   `Recover` sees it. Simpler than 3 alone; do both.
5. **Corrupt documents are surfaced.** `Documents.All` returns decode errors
   as a separate list; `Configs.List` logs each (key only, no content) and the
   monitor emits an operator event "state document unreadable" (no space id —
   the key path contains one, so the event carries only a count).
6. **Lease acquire without holding the mutex across I/O**: take the mutex to
   reserve the Space locally, release it, do the CS3 writes, and undo the
   local reservation on failure.
7. **Wire `notify.StateStore.PruneBefore`** on the same slow cadence as job
   history.
8. **Reduce idle load** (not correctness, but cheap):
   - Cache the service-account token for its lifetime minus a margin (reva's
     TTL is readable from the JWT `exp`); re-authenticate on `UNAUTHENTICATED`.
   - Run the stale monitor on its own cadence (e.g. every 15 min) instead of
     every tick.
   - `due()` reads `ListRecent(1)` for the baseline and relies on the lease /
     `ListRunning` for "in progress" instead of scanning ten records.
9. Timezone: default `SCHEDULE_TIMEZONE` to the container's local zone
   (`TZ`), and document it. The manifest sets `TZ`.

### Tests

Each item has a deterministic unit test with the injected clock; item 3/4
additionally an integration test against `cs3state` when the fixture is
present.

---

## R7 — Prune and maintenance: bring Phase 7 Tier 1 forward (F7)

### Problem

Nothing ever runs kopia maintenance. Failed runs leave orphan packs forever;
index blobs grow per run; `decisions.md` describes a separate prune job that
does not exist.

### Options

**Option A — same-process prune job now, separate credentials later
(recommended).** Schedule `Engine.Prune` per Space from the existing scheduler
on a slow cadence (default daily, staggered, never concurrent with a backup
of the same Space — reuse the run lock and a `jobs.KindPrune`). Use the same
credentials for now. This gives Tier 1 today; Tier 2 (write-only key for the
worker, owner key for prune in a separate context) stays in Phase 7 and
becomes a credential swap, not a code change. Update the `scheduler.go`
package comment that currently claims the opposite.

**Option B — wait for Phase 7 as planned.** Leaves orphans and growth in
place through Phase 7 and Phase 8. Not recommended: every failed run today
leaks storage on the buddy's disk.

### Tasks

1. `jobs.KindPrune`; scheduler emits prune jobs with `PruneInterval` per Space
   (config default 24 h, jitter from Space id).
2. `Runner.RunPrune(space)`: lock, `Engine.Prune(window)`, record counts.
3. Prune skips Spaces with no complete snapshot and never runs while a backup
   or restore holds the lock.
4. `runMaintenance` passes `force=false` once ownership is claimed properly
   (owner = the service's `UsernameAtHost`), so the kopia owner check is real
   rather than bypassed.
5. Record in `decisions.md` Phase-6/7 amendments that Tier 1 prune runs
   in-process on its own job kind, and what Tier 2 will change.

### Tests

Integration (Garage): two backups, second older than window after clock
advance, prune job → one snapshot remains, and after two maintenance cycles
with `SafetyFull` bytes are reclaimed (assert on manifest presence and
restorability, not blob counts — per Spike 2 notes). Unit: prune never
overlaps a running backup; prune skips a Space with only checkpoints.

---

## R8 — Docs-to-code truth pass (F7)

### Problem

`decisions.md`, `README.md`, deploy comments and several package comments
describe properties the code does not have.

### Tasks (each is a small edit; do them together after R1–R7 land, or now
with "planned" markers)

1. `decisions.md`:
   - #9/Phase-4: "prune runs as a separate job" → state current reality (R7)
     and the Tier 2 target.
   - Phase-6 notifications: operator log lines carry space ids; the "no space
     id" property holds for the SMTP channel and the event store. Either
     accept and say so, or add a log-redaction mode for space ids (**DECISION
     NEEDED**; recommendation: accept and document — the self-hoster is the
     operator, and hiding Space ids from logs makes support impossible).
   - Phase-5: drop or qualify "openable by a stock kopia release"; or encode
     the DK as hex when used as the repository password so the claim becomes
     true (requires a repository re-key for existing repos — not worth it;
     qualify the claim instead).
   - #14: exact plaintext locations (R5).
   - Trust model: "DK is sent to the server once at setup over TLS".
   - Threat model: add `OC_SERVICE_ACCOUNT_SECRET` as a first-class secret
     with owner scope on every Space; note that it, not SRW/TW, is the
     credential whose leak exposes plaintext.
   - #16: append-only carve-out (R1).
2. `README.md`: retention "works" → "runs daily" once R7 lands; add
   deployment preconditions (TLS, single instance, state Space runbook, tmpfs
   work dir); state that DK is sent at setup.
3. `cs3state.go` comment about re-derivable records; `scheduler.go` package
   comment about prune; `client.go` `roleLabel` comment; `spacecfg.go`
   timezone comment; `deploy/secret-wrap-keys.yaml` "database".
4. `cmd/takeout/main_test.go`: extend the audit to assert that no symbol from
   `pkg/keys` is reachable from `main` (e.g. `go list -deps` in the test, or
   move `Decrypt` into a separate package so `takeout` does not link it).
   Recommendation: split `pkg/takeout` into `pkg/takeout` (extract, verify)
   and `pkg/takeout/decrypt` so the linker guarantee is structural.

---

## R9 — Real-OpenCloud CI and test-gap closure

### Problem

Every OpenCloud-dependent test skips in CI; Path B and the state store have
never run against reva outside a developer machine.

### Options

**Option A — OpenCloud fixture in a CI job (recommended).** The fixture
already exists (`test/fixtures/opencloud/up.sh`); add a CI job that starts it
(Docker on the runner), exports `fixture.env`, and runs `-tags integration`
for `pkg/cs3`, `pkg/cs3state`, `pkg/backup` (ocis tests), `pkg/restore`
(Path B), `pkg/api` (admin). Pin `opencloudeu/opencloud-rolling:7.3.0`.
Runtime is several minutes; run it on PRs to `main` and nightly.

**Option B — nightly only.** Cheaper; regressions found a day late. Acceptable
fallback if runner minutes are a concern.

### Tasks

1. CI job `integration-opencloud` with the fixture; fail the job (do not
   skip) if `CS3_GATEWAY_ADDR` is unset in that job.
2. Convert `pathb_integration_test.go` to run against the fixture when
   present (keep the in-memory variant for speed): real `simple` PUT,
   `X-OC-Mtime` honoured or documented as best-effort, zero-byte file, empty
   directory, filename with spaces and unicode.
3. Phase-6 exit-criteria tests parametrised over `{memory, cs3state}`.
4. `cs3state` overwrite-semantics test (R1 task 5).
5. `grants` shape and role test (R3 task 1).
6. Add the F4 tests (R4), the setup-409 test (R2), the run-timeout test (R6).
7. Enable the `unused` linter for `pkg/` and delete what it finds
   (`pkg/s3target`, `Client.Walk`, `SpaceReader.Walk`, per-package
   `MemoryStore`s if the contract suites are rebased onto
   `state.NewMemoryStore()`).

---

## Cross-cutting: plan-level additions (for the phase index)

- **Phase 5b — Re-attach after rebuild (new, small).** Client-side flow: user
  supplies RK; browser fetches `recovery.ocbke` for an old space id from the
  granted target (via a new member-scoped endpoint that lists orphaned
  `keys/<id>/` prefixes the target holds — ids only, no content), unwraps the
  DK, and POSTs a setup for the *new* Space with a `repo_prefix` override
  pointing at the old repository. Server validates the DK opens the old repo
  before accepting. This turns "OpenCloud rebuilt ⇒ Path A only" into
  "OpenCloud rebuilt ⇒ Path B works after one ceremony". Depends on R1, R2.
  **DECISION NEEDED** whether this is v1 or backlog.
- **Phase 7 scope change.** Tier 1 moves to R7 (now). Phase 7 keeps Tier 2
  (credential split), Tier 3 (docs), the capability probe, and the retention
  floor moves to R5.
- **Rotation runbook** becomes a Phase-7 deliverable (R2 CLI + docs).

---

## Suggested sequencing

| Step | Plans | Why first |
|---|---|---|
| 1 | R1 (append-only state), R2 steps 1, 5, 7 (409, Argon floor, SRW≠TW) | Stops the two data-loss paths. Small, contained, no UI dependency. |
| 2 | R4 (ignore rules, checkpoints) | Restores the "failed run leaves nothing" and "success means complete" properties the threat model relies on. |
| 3 | R5 steps 5–6, 10, 14–15 (Recreate, fatal without state, audience, floor) | One-line fixes with outsized effect. |
| 4 | R3 (roles) + R9 task 5 (pin grants shape) | Needs the fixture test first; do them together. |
| 5 | R6 (runner/scheduler), R7 (prune job) | Robustness; R7 needs R4's checkpoint filtering. |
| 6 | R5 work-dir handling, R9 CI job | Infrastructure; can run in parallel with 4–5. |
| 7 | R2 rotation endpoints/CLI, R8 truth pass | Rotation needs R1's append-only layout; docs last so they describe what shipped. |
| 8 | Phase 7 (Tier 2/3), Phase 8 (UI) with the client invariants from R2 | Unchanged, on a fixed foundation. |

Decisions the owner needs to make before merging:

1. R1 Option D — publish the SRW envelope to S3? (recommendation: yes)
2. R3 task 3 — resolve group grants via graph per request, or document as
   unsupported? (recommendation: graph)
3. R5 task 10 — default `OIDC_AUDIENCE` to `web`, or require explicit?
   (recommendation: default with warning)
4. R8 — accept space ids in operator logs and document, or add redaction?
   (recommendation: accept and document)
5. Phase 5b — v1 or backlog? (recommendation: v1; it is the family's realistic
   "the server died" story and the pieces exist after R1/R2)
