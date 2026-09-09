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

### Outcome (implemented, issue #16)

Option A, with one row of the role table struck by the owner and three findings
from pinning the grant shape.

- **Owner decision — restore stays at viewer.** The plan argued restore should be
  manager/owner because it writes into the Space. decisions.md #7 says plainly
  that any member may trigger a restore, and that sentence was kept rather than
  amended: disaster recovery is a member capability. Only setup and RK rotation
  moved up. The residual risk — a viewer can cause files to appear in a
  `Restore/<timestamp>/` folder of a Space they cannot write to — is recorded in
  the decisions.md R3 amendment as accepted, not overlooked.
- **Owner decision — group grants are resolved via the graph API**, as
  recommended, using `GET /graph/v1.0/me?$expand=memberOf`. Two deviations from
  the plan's "graph call per request": OpenCloud 7.3.0 has **no `/me/memberOf`
  route** (404), so the expanded `/me` document is the supported source; and the
  call is made **lazily**, only when the caller's direct grant falls short and
  the Space actually carries a group grant. A Space granted to users only never
  triggers it. Missing or failing resolution answers 503/502 rather than
  guessing (new decision #20).
- **Finding — the grant value is a permission set, not a role name.** Task 1's
  fixture test found `grants` to be
  `{"<principal>": <provider.ResourcePermissions>}` — a bag of booleans
  (`initiate_file_upload`, `add_grant`, …). The old `roleLabel` helper, which
  looked for a `"role"`/`"name"`/`"type"` string, would have returned the whole
  compact JSON blob for every principal. Roles are therefore derived by
  *capability* (may grant → manager, may write → editor, may read → viewer),
  which survives reva adding a permission; matching an exact permission set would
  not.
- **Finding — the sidecar maps are separate and sparse.** `groups` is
  `{"<gid>":{}}` (a set, listing which grant keys are groups) and
  `grants_expirations` is `{"<principal>":{"seconds":<unix>}}` — both absent
  entirely when empty. A group id and a user id are otherwise indistinguishable,
  so without `groups` a group grant would be matched against caller subjects.
- **Finding — reva prunes expired grants itself.** A grant seen in `grants`
  before its expiry is gone from both `grants` and `grants_expirations` on the
  next read afterwards. The local expiry check was kept anyway: the pruning is
  lazy and is not a documented guarantee.
- **Deviation — `Space.Owner` is useless on project Spaces.** OpenCloud sets a
  project Space's owner to the Space's own id, so `RoleOwner` only ever matches
  on a personal Space. Ownership is still checked first (it is the personal-Space
  case), but authority on a shared Space comes exclusively from grants.
- **Addition — the role table is itself a test.**
  `TestRoleTable_EveryRouteEnforcesItsMinimumRole` walks every route × every
  caller role, asserting both directions (below the minimum → 403, at or above →
  not 403). Adding a route without a row is the mistake it exists to catch.
- **Addition — the fixture seeds the grants it pins.** `seed.sh` now creates a
  project Space with a viewer, an editor, a manager, a group grant and an
  expiring grant, exporting `OC_SHARED_*` into `fixture.env`. This also covers
  R9 task 5.
- **Task 5 delivered as a per-request `access` checker** (`pkg/api/access.go`)
  holding the memoised Space list and caller groups, installed by middleware
  after `Authenticate`. `GET /targets` now lists Spaces once, not twice.

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

### Outcome (implemented, issue #18)

All six steps, with two deviations that widen the cleanup, one addition, and one
note about what makes the tests worth having.

- **Deviation — cleanup deletes every incomplete manifest of the source, not
  only the current run's.** The plan scopes the delete to manifests whose
  `StartTime` falls within this run. That leaves anything a *killed* process
  wrote behind forever, which is the one case the cleanup cannot otherwise
  reach — a run that ends cleans up after itself, so a leftover by definition
  belongs to a run that did not end. A Space is backed up under a run lock, so no
  other run can be writing one concurrently, and the time comparison (repo clock
  vs. run start) buys nothing in exchange for its edge cases. Same reasoning in
  `Prune`: step 6 says incomplete manifests are "always eligible", implemented as
  *always expired* rather than "expired if older than the cutoff", since an
  incomplete manifest newer than the window would otherwise be immortal.
- **Deviation — the hash-cache input is deliberately *not* filtered.** Step 2
  says "every consumer"; `previous` (fed to `Uploader.Upload`) is excluded on
  purpose and says so in a comment. It is the one place an incomplete manifest is
  legitimately useful — a run resuming after a crash reuses its own checkpoint's
  hashes instead of re-reading the Space from CS3 — and it is read, never served.
  In practice there is rarely anything there to use, because the previous run
  deleted its own; it matters exactly when the previous process was killed.
- **Addition — `NewEngine` rejects a `CheckpointInterval` above kopia's 45-minute
  maximum.** kopia validates it inside `Upload`, i.e. after a backup has started
  and a repository has been opened. Failing at construction turns that into a
  startup error.
- **Note — the two behavioural tests each needed a companion to have teeth.**
  "Every file was snapshotted despite a `.kopiaignore`" passes just as happily if
  the ignore conventions never applied to a virtual source at all, and "no
  incomplete manifest remains" passes if no checkpoint was ever produced. Both
  are therefore paired with a white-box run of kopia's uploader that asserts the
  *unguarded* behaviour — ignore rules really do drop files, a 1-second
  checkpoint interval really does leave manifests behind. The second one drives a
  genuine multi-second upload (`slowSource`) and is skipped under `-short`.
- **Note — `assertComplete` was never the gap.** It rejects the run's final
  manifest correctly; what it could not see is everything kopia had already saved
  and flushed on its own initiative before that point. Nothing about it changed.

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

### Outcome (implemented, issue #20)

All fifteen steps, with the work-dir investigation answered differently than
either branch of step 1 anticipated, and two owner decisions taken against the
plan's recommendation.

- **Step 1 — the answer is "no, but".** kopia v0.23.1 has no exported way to open
  a repository without `repo.Connect` writing `repository.config`
  (`openWithConfig` says "avoiding the need for a config file" and is
  unexported). It does export `blob.AddSupportedStorage`, so the repository is
  connected through a **storage handle**: our own storage type whose serialised
  connection info is a random per-run token resolved against an in-process
  registry (`pkg/snapshot/handle.go`). Credentials never reach a filesystem at
  all — not tmpfs, not after a SIGKILL — which is strictly stronger than step 2's
  fallback and independent of how the operator mounts anything. Step 2 was done
  anyway, for kopia's *cache*: it holds repository content, so the work directory
  must be memory-backed and startup refuses otherwise.
- **Owner decision — `OIDC_AUDIENCE` is required, not defaulted.** The plan
  recommended defaulting to OpenCloud's `web` client id with a warning. A default
  audience is a security control that silently exists and is easy to leave wrong,
  and the manifest already ships placeholders that must be filled; one more costs
  a deployer nothing and removes a way to be quietly insecure.
- **Owner decision — the service can terminate TLS itself.** Step 13's optional
  half was taken: `TLS_CERT_FILE`/`TLS_KEY_FILE`, both or neither. Documentation
  alone leaves a deployment with nothing in front of it serving a Data Key over
  plain HTTP with no way to fix it in place.
- **Addition — `ErrKeySetUnavailable` separates "cannot check" from "invalid".**
  Step 11 asks for 503 while discovery has not succeeded; that only works if the
  validator distinguishes an unreachable identity provider from a bad token.
  Without it a client would discard a perfectly good session over an IdP hiccup.
  The same error covers a failed JWKS refresh, not just startup discovery.
- **Deviation — the retention floor is enforced twice.** The plan places it at
  the API boundary. `Config.EffectiveRetentionWindow` raises anything below the
  floor as well, silently, because that is the path prune reads: a record written
  before the floor existed must not make prune delete history the API would
  refuse to give up.
- **Deviation — `DiscoverKeySet` was deleted rather than kept alongside the lazy
  one.** Two ways to build the same key set, one of which makes startup depend on
  the IdP, is the bug this step exists to remove.
- **Step 14 needed no work** — `SRW_KEY == TW_KEY` was already refused by R2.
- **Note — the leaky-driver test fake cannot be bypassed silently.** The fake
  storage used to prove credentials never reach the config file registers no
  kopia storage type, so if the handle substitution were ever removed the
  repository would fail to reopen rather than quietly leaking. A test that can
  only fail loudly was worth the extra ten lines.

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

### Outcome (implemented, issue #21)

All nine items, with one item narrowed by what Go's HTTP transport can actually
do, two placed differently than the plan said, and one addition without which
item 1 cannot work.

- **Addition — the outcome write is detached from the run's context.** Not in
  the plan, and load-bearing: item 1 bounds a scheduled run by cancelling its
  context, so the failure it causes cannot be recorded *with that context*. Item
  4's retry therefore lives in `jobs.RecordOutcome`, which writes on
  `context.WithoutCancel` with a per-attempt deadline. Without it, item 1 would
  have replaced "a run that never ends" with "a run that ends and says nothing" —
  the exact wedge item 3 exists to clean up.
- **Deviation — the run lock is released by the runner's `finish`, not deferred
  by its callers.** "Keep the lease when the outcome cannot be recorded" is not
  expressible while a `defer pending.release()` two frames up always fires. Both
  runners (backup and restore) now release only after `RecordOutcome` succeeds.
  Restore was not in the plan's scope; it had the identical bug, and fixing one
  copy of a two-copy bug is not fixing it.
- **Deviation — item 3's sweep runs on a slow cadence inside `Recover`, not on
  every tick.** Nothing in a job's key says whether it finished, so "list running
  jobs" means reading documents — from CS3, over the network, for every Space's
  whole history. Once a minute that is worse than the bug. It therefore runs
  hourly (`LeaseOptions.OrphanSweep`), is deliberately *not* run at startup (the
  expired-lease pass already covers everything a crash leaves behind, and a
  service should not read a year of history before accepting a request), and
  `jobs.StateStore` remembers the keys it has already found terminal — a
  finished run never becomes unfinished, so later sweeps read only what is new.
- **Deviation — `Store.ListRunning` lost its Space parameter.** Recovery has to
  visit every Space, and the per-Space variant the plan names had no production
  caller at all. It is now `ListRunning(ctx)` on the interface, implemented by
  both stores.
- **Item 2 narrowed, and the narrowing is the interesting part.** The guard fails
  a transfer that moves no bytes for two minutes, which covers a gateway that
  stops sending or stops accepting. It does **not** cover a source that blocks
  forever inside a single `Read`: Go's transport waits for its write loop before
  returning, so cancelling the request cannot unwedge it — the run timeout is
  what covers that case, and the comment in `put` says so rather than implying a
  guarantee that does not exist. The guard also stops watching at end of input,
  so a server spending a while finalising a large upload is not cut short by it.
- **Deviation — item 5 changed `spacecfg.Store.List`, not `Documents.All`.** The
  plan names `All`, which has no production caller; the place a corrupt document
  actually disappears is `Versions.Latest`. `state.ErrMalformed` now separates
  "corrupt" from "missing" — the two want opposite reactions — and `List` returns
  the unreadable keys as a second value, so a caller has to look at them. The
  scheduler logs each key; the monitor raises an operator event carrying only a
  count, because the key contains a space id (#15).
- **Deviation — the token cache is a wrapper, not a change to
  `ServiceAccountAuth`.** `cs3.CachedAuth` wraps any `Authenticator`, keeping the
  minter stateless and testable, and reads the token's own `exp` (a token this
  service minted for itself a moment ago, never verified and never used for an
  authorization decision) rather than guessing a lifetime. A token reva rejects
  with `UNAUTHENTICATED` is dropped; the plan's "re-authenticate on
  UNAUTHENTICATED" is deliberately not an automatic retry — the *next* call
  re-mints, and one refused call surfaces rather than being papered over.
- **Deviation — item 8's `ListRecent(1)` widens when it has to.** One record
  answers "when did the last backup start" almost always, but not when the newest
  run was a restore; flat `ListRecent(1)` would then fall back to the
  configuration timestamp and schedule a spurious immediate run. The read widens
  to the old lookback only in that case.
- **`OnTick` became `OnSweep`,** with its own interval (15 min). A hook named for
  the tick that no longer runs on every tick is a comment waiting to go stale.
- **Item 9 defaults to `time.Local`,** so `TZ` — which a deployment sets anyway
  for its logs — decides. The manifest ships `TZ: "UTC"` uncommented, stated as
  the thing to change, rather than a commented-out override nobody reads.
- **R1's revision-growth finding was not folded in here.** It is a slow leak
  rather than a failure, the fix depends on what OpenCloud permits (there may be
  no delete-revision RPC at all), and mixing an open investigation into a
  robustness plan would have delayed both. It is now **R10**.

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

### Outcome (implemented, issue #24)

Option A, all five tasks, with two owner decisions, one addition the plan did
not scope, and one test the plan asked for that cannot be written as written.

- **Owner decision — the cadence is deployment-wide, not per Space.** Task 1
  reads "`PruneInterval` per Space (config default 24 h)". It is a
  `scheduler.Options` value (`PRUNE_INTERVAL_HOURS`, default 24 h) applied to
  every Space and staggered by a stable per-Space offset inside an eighth of the
  interval. The *window* is the user's setting and already per Space; how often
  it is enforced is the operator's, and making it per Space would have meant a
  new `spacecfg` field, API validation and a UI knob nobody asked for.
- **Owner decision — a prune failure does not notify members.** `OnRunFinished`
  is not called for prune runs. It is the path that tells a family their backup
  failed; a prune failure means storage was not reclaimed, which is true of
  nothing they can act on. Job record and operator log only.
- **Addition — the job's kind moved into its document key.** Not in the plan,
  and the plan does not work without it. Prune records share the one history
  stream per Space, so a daily prune halves the reach of every bounded scan that
  looks for the last *backup* (schedule baseline, staleness check, status board)
  and makes R6's "widen from 1 record to 10 when the newest run is not a backup"
  fire on almost every tick — a roughly fivefold increase in the reads an idle
  deployment makes. The layout is now `jobs/<space>/<nanos>-<kind>-<job-id>`
  with a `Store.ListRecentOfKind`; pre-existing records are read and classified
  as before, costing one read each until they age out. The widening workaround
  is gone.
- **Deviation — `Engine.Prune` returns `PruneStats{Deleted, Kept}` and
  `jobs.Job`/`Outcome` carry them.** Task 2 says "record counts", which was not
  expressible: the engine returned only an error. Without this a prune's history
  entry says "it worked", which cannot distinguish a run that reclaimed a year
  of snapshots from one that found nothing to do — the exact question an
  operator watching a target fill up is asking.
- **Task 3's "skips a Space with no complete snapshot" is enforced in the
  scheduler, from run history**, not in the runner: a Space with no *successful*
  backup has no repository to open, so the check costs nothing (the same history
  read that answers the schedule baseline answers this) and never produces a job
  at all. A Space whose runs only ever reach a checkpoint lands here too, since
  R4 made an incomplete run a failure.
- **Task 4 needed a second half the plan did not mention.** Dropping `force`
  turns "owned by somebody else" from silently-ignored into an error, so the
  error had to be given a sentinel and stripped of the other owner's
  username-at-host before it reaches a job record a user can read.
- **The plan's headline integration test cannot be written.** "Two backups,
  second older than window after clock advance, prune job → one snapshot
  remains" needs the *repository's* clock to move, and the retention floor is a
  week (R5), so no runner-driven prune can expire a fresh snapshot. Expiry is
  proven at the engine level with a nanosecond window (as it already was); the
  runner-level Garage test proves what only a real target can — ownership
  claimed against a real S3 repository, `RunExclusive` running with the check on,
  two full maintenance cycles, and every snapshot still listed and still
  restoring byte-identically afterwards.

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

## R10 — Unbounded revision and trash growth in the state Space

### Problem

Found while implementing R1, deliberately left out of R6 so it gets its own
treatment. `InitiateFileUpload` over an existing path overwrites silently on
OpenCloud 7.3.0, and every overwrite leaves a **file revision that nothing
reclaims** (measured on the fixture: five writes to one path left four revision
nodes). Deleting does not help either — `cs3state.Delete` moves the document to
the Space's trash, and nothing empties that.

Three records are written by replacement, at very different rates:

| Record | Cadence | Revisions/year, one pod |
|---|---|---|
| `instances/<id>` | every TTL/3 ≈ 40 s, for the life of the process | ~790,000 |
| `leases/<space>` | every TTL/3 ≈ 3 min 20 s, while a run lives | ~430/day of running time, per Space |
| `jobs/<space>/<...>` | once per run (`Finish`) | one per run |

The instance heartbeat dominates by two orders of magnitude and is the one the
R1 note did not name. The objects are tiny, but the growth is unbounded, it
never stops while the service is up, and it accumulates in the one Space that
also holds every wrapped Data Key — the Space an operator is least likely to
want to go poking around in with a bulk-delete tool.

Nothing here is a correctness bug today. It is a slow leak with no end state,
which is why it is a plan item rather than a `TODO`.

### Options

**Option A — write less often.** Raise the instance TTL and lease TTL so the
heartbeats are minutes apart rather than seconds. Cheap, entirely inside this
codebase, and reduces the rate by an order of magnitude — but the growth is
still unbounded, and a longer lease TTL directly lengthens how long a crashed
run wedges a Space (the two are the same number). Mitigation, not a fix.

**Option B — reclaim from the service.** Requires an RPC that deletes a file
revision. CS3 exposes `ListFileVersions` and `RestoreFileVersion`; there is no
delete-version call, so this may be impossible through the supported API.
`PurgeRecycle` does exist, so the *trash* half is reachable. **A spike must
establish what OpenCloud 7.3.0 actually permits before this option can be
costed.**

**Option C — stop replacing.** Make the heartbeats append-only (`Versions`, as
R1 did for the records that matter) and prune old versions on a slow cadence.
Trades one revision per write for one document per write plus one trash entry
per prune — probably worse, unless the prune can be made rare (write a version
per hour, not per heartbeat, and carry the liveness in the *newest version's*
timestamp rather than in a rewritten document).

**Option D — document an OpenCloud-side cleanup** the operator runs (or a
policy they configure), and state the growth rate honestly in the runbook so
the number is not a surprise. Cheap, and may be the only *complete* answer if
the spike says the API cannot reclaim revisions.

**DECISION NEEDED** once the spike lands. Provisional recommendation: A + D
together (slow the bleeding, tell the truth about the rest), with C considered
only for the instance record, which is the sole one whose write rate is
independent of what the service is actually doing.

### Tasks

1. Spike against the OpenCloud fixture: can a revision be reclaimed at all
   (`ListFileVersions`, any delete path, storage-driver setting, server-side
   policy)? Can `PurgeRecycle` be reached with the service account, and does it
   apply to a state Space? Record findings in `phase-0-findings.md`; pin
   whatever is learned with an integration test.
2. Measure, do not guess: a fixture test that writes a heartbeat N times and
   reports revision count and on-disk bytes, so the projection above becomes a
   number this repo owns rather than an estimate.
3. Implement the option chosen after step 1, including the TTL/liveness
   trade-off if Option A is taken (a longer lease TTL must not silently make
   the R6 recovery window worse than what the phase-6 doc promises).
4. Whatever is chosen, state it in the state-Space runbook: expected growth per
   year for an idle deployment, and what an operator does about it.

### Tests

Integration (OpenCloud fixture): the measurement in task 2, and the reclaim
path in task 1 if one exists. Unit: whatever TTL/liveness arithmetic changes —
in particular that a lease still expires strictly before recovery treats it as
abandoned.

### Docs

`decisions.md` #16 (the R1 amendment already records this as an open item —
close it with the outcome), `README.md` state-Space runbook,
`phase-0-findings.md` for the spike.

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
| — | R10 (revision growth) | Not on the critical path: a slow leak, not a failure. Needs a spike before it can be costed, so it sits outside the sequence until that lands. |

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
6. R10 — which option, once the spike says what OpenCloud permits?
   (provisional recommendation: slow the heartbeats and document the residual)
