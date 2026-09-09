# Locked Decisions & Threat Model

Canonical record of design decisions and their rationale. This file preserves
the reasoning that was worked out during planning. If a phase doc and this 
file disagree, **this file wins** — reopen the decision explicitly rather 
than drifting.

---

## Product framing

- **Target user:** the self-hosting parent running OpenCloud for the whole
  family. Not enterprise. Optimize for "one click, then forget", admin-supported
  recovery, and simple mental models over feature breadth.
- **Core promise:** a user clicks "back up my data" once; backups then run
  **scheduled and unattended** to an external S3 target ("Buddy S3"). Targets are
  **managed in-app by an admin** (decision #12), who grants them to all or
  specific users. The admin operates targets/grants but **never sees plaintext**
  and cannot reach users' backups (decision #15).
- **Non-goals:** client↔OpenCloud sync (the normal OpenCloud clients do that;
  this backs up what is *already in* OpenCloud); enterprise scale/SLAs; HSM key
  custody.

---

## Decisions (locked)

1. **SRW (Server Runtime Wrap) accepted.** Unattended scheduled backups require a
   server-held wrapped copy of the Data Key. This is **not** pure zero-knowledge
   — a server compromise that also leaks the SRW-wrapping key could decrypt data.
   Accepted trade-off for "runs while everyone sleeps". The SRW-wrapping key lives
   in a K8s secret / KMS, never in the admin UI, never logged.

2. **Restore roles split.** The admin can **only extract ciphertext** (Take-Out,
   Path A). Only the **user** restores into OpenCloud (Path B, via GUI, authorized
   by their own session). There is **no admin restore into a user's space**.

3. **Restore granularity.** v1 = **full restore** into a dedicated
   `Restore/<timestamp>/` folder (never silent overwrite). Single-file/partial
   restore and overwrite-in-place are **backlog**.

4. **Backup scope: files + folder structure + mtime only.** No shares,
   permissions, versions, or trash. Rationale: oCIS shares/permissions bind to
   internal space/user IDs; after disaster recovery OpenCloud is rebuilt with
   *different* IDs, so restored permissions would be meaningless or dangerous.
   Users re-upload and re-share manually.

5. **Engine: kopia as a library** (`github.com/kopia/kopia/repo`, `/snapshot`).
   Importable Go packages, mandatory client-side encryption, content-addressed
   dedup, native snapshots, per-file restore. restic rejected (logic in
   `internal/`, subprocess-only). **We do not hand-roll crypto or dedup.**
   kopia's own Object-Lock ransomware feature is **not usable on Garage** (no
   Object Lock) → kopia used for snapshot/dedup/crypto only; immutability handled
   out-of-band (see threat model). Keep the Object-Lock path capability-flagged.

6. **Key/snapshot scope: per Space.** One Data Key + one kopia repo + one snapshot
   chain per Space. A shared space is backed up **once**, not once per member.

7. **Shared-space Recovery Key: shared / retrievable by space members.** No single
   owner. A shared space's confidentiality is already distributed across its
   members, and the backup's job is disaster recovery + ransomware mitigation, not
   a new confidentiality boundary. Any member may perform the Take-Out decrypt or
   trigger restore. **Shamir N-of-M secret sharing was considered and rejected**
   as overkill for the family scenario. See "Amendments from … R3" for what
   "member" means precisely (role, expiry, group) and which actions moved above
   plain membership.

8. **Target: Garage** (self-hosted buddy S3). As of v2.3.0 it has **no S3 Object
   Lock and no versioning** (upstream Garage #166 versioning, #1127 Object Lock;
   no ETA). Only read/write/owner grants, and `write` implies overwrite/delete →
   **no API-enforced append-only**. Immutability handled out-of-band and
   capability-flagged for when Garage gains support.

9. **Immutability is layered:**
   - **Tier 1** (covers the primary threat fully): server-side worker; write key
     never on the client; deep time-based retention; prune runs as a **separate
     job** from backup.
   - **Tier 2** (limits leaked-write-key blast radius): worker gets a
     **write-only** Garage key; prune/GC runs from a **separate trusted context**
     with the owner key.
   - **Tier 3** (optional, real out-of-band WORM): ZFS/Btrfs snapshots of the
     Garage `data_dir`/`meta_dir`.

10. **Retention: time-based (`keep-within`), never count-based.** A count-based
    policy can be weaponized — an attacker injecting many bogus recent snapshots
    could push legitimate ones out of a "keep last N" window.

11. **Unattended worker authenticates with an OpenCloud service account**
    (`auth-service`, CS3 auth type `"serviceaccounts"`). See "Amendments from
    Phase 0" below for the full rationale. Listed here so the locked-decision
    numbering is contiguous.

12. **Backup targets are in-app, admin-managed (not a single deploy-time
    config).** An in-app admin creates/edits multiple S3 targets through the UI
    and grants each to **all users** or **specific users**. This supersedes the
    earlier "single pre-configured Buddy-S3" framing. The user experience is
    unchanged when exactly one target is granted (it is auto-selected — "one
    click" preserved); a picker appears only when a user has more than one.
    Rationale: a family admin wants to point different people at different buddy
    stores without redeploying the cluster. **Enforcement is server-side** (a
    user sees/uses only granted targets); client input is never trusted.

13. **The in-app admin identity is the OpenCloud admin role, reused — we do not
    build our own admin user store.** Admin status is derived from OpenCloud
    (server-side via the graph `appRoleAssignments`; client-side via the web
    SDK's ability/CASL model). **Validated by the Phase-0 admin-role spike**
    (see `phase-0-findings.md`): on 7.3.0 the OIDC token carries no role claim,
    so detection uses `GET /graph/v1.0/me?$expand=appRoleAssignments` and maps the
    Admin app-role id (`71881883-1768-46bd-a24d-a356a2afdf7f`, configurable) →
    admin. Graph detection proved reliable; the fallback (operator-provided
    allow-list of admin subject IDs in config) is still implemented in Phase 2 as
    an alternative resolver. The client-side CASL gate is deferred to Phase 8.

14. **Target S3 credentials are app-managed and encrypted at rest.** The admin
    enters credentials in the UI; the app stores them **wrapped by a
    cluster/KMS "Target-Wrap" (TW) key** — the same custody class as the SRW key
    (K8s secret / KMS, never in the admin UI, never logged). Credential fields
    are **write-only**: no API `GET` ever returns them, and they are decrypted
    **only in worker memory at run time** and zeroized after use where Go allows.
    Threat-model delta from this is recorded below.

15. **Admin scope is targets + grants only.** The admin can manage targets and
    who may use them, and nothing else. The admin gets **no plaintext, no restore
    into a user's Space, and no visibility into or control over users' backup
    jobs/contents for Spaces they are not a member of.** This reaffirms
    decision #2 rather than weakening it; the admin is a *configuration* actor,
    not a *data* actor.

16. **The service's own state lives in OpenCloud, over CS3** — a dedicated Space
    no end user belongs to, not a database. See "Amendments from Phase 6" for the
    rationale, the validation, and the constraints it imposes (single instance;
    no transactions; state Space must not be a user's Space).

17. **Establishing a Space's keys is a once-only act.** Setup is refused for a
    Space that already has them; there is no override. A second ceremony would
    orphan every backup that Space has ever written. See "Amendments from the
    September 2026 review — R2".

18. **Every key is replaceable, and replacing one never rewrites data.** The
    Recovery Key rotates in the browser, the server's SRW and TW keys rotate
    through an operator command; the Data Key itself does not rotate. Same
    amendment section for the constraints (resumable, not atomic; not alongside
    backups).

19. **Client-produced Recovery Key envelopes must meet a minimum work factor.**
    The server cannot see the Recovery Key, so how hard it is to derive from is
    the only property of the client's ceremony it can check.

20. **A grant that cannot be evaluated is refused, not guessed.** Group
    membership is resolved from OpenCloud's graph API; when that is unavailable
    the affected callers get an error rather than a decision. See "Amendments
    from the September 2026 review — R3".

21. **A bearer token is accepted only if it was issued for this service.**
    Audience is required configuration and expiry is required in the token. See
    "Amendments from the September 2026 review — R5".

22. **Retention has a floor.** A member may shorten their Space's history, but
    not below a week: depth is the defence the threat model rests on, and a
    browser session is not enough authority to remove it. Same amendment section.

---

## Still open (must be resolved in Phase 0, may amend decisions)

**All Phase-0 open items are now resolved.** See `phase-0-findings.md`.

- ~~Exact OpenCloud Web extension packaging + auth-forwarding mechanism.~~
  **RESOLVED** (Spike 4). Vite 8 + `@opencloud-eu/extension-sdk` module-
  federation bundle; drop under `web/assets/apps/<id>/`; OpenCloud injects it
  into `config.json`. Auth = user's OIDC browser session (implicit forwarding).
- ~~kopia-as-library API surface, incl. per-file restore.~~ **RESOLVED** (Spike 2,
  kopia v0.23.1). Full lifecycle validated against Garage via importable
  packages; per-file restore works (`snapshotfs.GetNestedEntry` + `restore.Entry`).
  restic fallback not needed.
- ~~CS3 gRPC + data-provider read path … which token/credential the unattended
  worker uses.~~ **RESOLVED** (Spike 3). Read path validated end-to-end;
  worker uses a **service account** (see decision #11 below).
- ~~How CS3 exposes shared-space membership.~~ **RESOLVED** (Spike 3). Read the
  space's `Opaque` map `grants` / `groups` / `grants_expirations` from
  `ListStorageSpaces`.

### Amendments from Phase 0

- **Decision #8 confirmed by test:** Garage v2.3.0 returns `NotImplemented` for
  both Object Lock and bucket versioning (`internal/testutil` integration tests
  guard this).
- **Decision #10 implementation pinned:** kopia's own retention policy is
  **count-based only** (no native `keep-within`). Time-based retention is
  therefore implemented **above** kopia (list snapshots → delete manifests older
  than the window → full maintenance GC), never via kopia's policy engine. This
  reinforces #10 rather than changing it. See `phase-0-findings.md`.
- **New decision #11 (locked): the unattended worker authenticates with an
  OpenCloud service account** (`auth-service`, CS3 auth type `"serviceaccounts"`).
  Rationale: reva grants service accounts an *owner scope* and they may read all
  spaces, so a single credential backs up any space with no per-user
  impersonation. Machine-auth (impersonation) was considered and rejected as
  unnecessary coupling. The service-account secret lives in the cluster
  (`OC_SERVICE_ACCOUNT_ID`/`SECRET`), never in the admin UI, never logged — same
  handling class as the SRW key. This validates the Phase 6 scheduler / SRW model
  (the highest-risk assumption held).
- **Membership for shared-space RK retrieval (decisions.md #7):** query the
  space grants via the `ListStorageSpaces` opaque map; no separate sharing API
  needed.

### Amendments from Phase 4

These refine *how* locked decisions are implemented; none reopens one.

- **CS3 → kopia uses a virtual filesystem, not a staging directory**
  (phase-4 "Option B"). kopia's `fs` interfaces are implemented directly over
  CS3, so no Space is ever staged on disk. Files are exposed as **`fs.File`**
  rather than `fs.StreamingFile`, because kopia's cache-hit check skips the size
  comparison for streaming files — a content change that preserved mtime would
  otherwise be silently missed. This couples us to a non-guaranteed kopia API,
  so kopia stays pinned and the adapter is thin and behaviour-tested.

- **Decision #10, implementation hardened.** It is not enough to *not call*
  kopia's count-based retention: the uploader applies it automatically while
  checkpointing long runs. Every backup therefore pins a source-level retention
  policy with all counters set to zero, which kopia interprets as "keep
  everything". Expiry happens exclusively in the separate prune job, by
  time-based `keep-within`.

- **Prune never deletes the newest snapshot**, even when it is older than the
  retention window. A Space whose backups stopped must not silently lose its last
  copy. Retention depth is the protection (threat model); zero copies is not a
  retention outcome anyone asked for.

- **A partially-read Space is a failed run, not a thin backup.** kopia records
  per-entry read failures in the manifest and still returns success. We reject
  such a manifest and do **not** persist it, so a run that could not read every
  file is reported as failed rather than producing a snapshot that silently
  omits data. The error carries counts only, never the failing paths.

- **The Space→target binding is user-owned and lives in `pkg/spacecfg`,**
  separate from the admin-owned `pkg/targets`. A binding is only stored after a
  server-side grant check (`targets.Authorizer.MayUse`); a client-supplied target
  id is never trusted, and "not granted" is indistinguishable from "no such
  target". This keeps decision #12's enforcement boundary explicit in the
  package layout.

- **Optional first-start seeding of one default target** (`targets.Bootstrap`,
  disabled by default). It only runs when the target store is empty, so it can
  never override admin configuration, and the seeded credentials are TW-sealed
  exactly like admin-entered ones (#14). Rationale: preserve "one click, then
  forget" on a fresh deployment before the admin UI (Phase 8) exists.

- **Ownership is skipped on restore.** Permissions and ownership are out of
  backup scope (#4) and the virtual source carries no real uid/gid, so restoring
  ownership would only attempt — and fail — a chown to root.

### Amendments from Phase 5

These refine *how* the restore paths are implemented; none reopens a locked
decision.

- **The RK-wrapped envelope is published to the S3 target on every backup run**
  (`<prefix>keys/<space-id>/recovery.ocbke`). Path A must work with OpenCloud
  fully down, and `takeout` has S3 access only, so the envelope cannot live
  exclusively in the service's key store. What is published is ciphertext the
  server itself cannot open (the RK is never held or learned server-side), so
  this does not widen what the buddy store can read: it is the same trust class
  as the encrypted repository already there. Publication failure is logged, not
  fatal — the snapshot is still valid and still restorable via Path B.
  The envelope deliberately sits **outside** the kopia repo prefix: everything
  under a repository prefix is kopia-owned and maintenance may reclaim blobs it
  does not recognise.
  *Threat-model delta:* a leaked S3 **write** credential can overwrite or delete
  the envelope, exactly as it can the repository (decision #8 already accepts
  this; immutability Tiers 2/3 bound it). It cannot *read* anything new.

- **A Take-Out is a standard kopia filesystem repository, not a raw object
  copy.** kopia's S3 driver stores flat blob ids while its filesystem driver
  shards and suffixes them, so objects synced verbatim out of a bucket would not
  reopen locally. The copy therefore runs at kopia's *blob* level. The pleasant
  consequence: a Take-Out is openable by our `decrypt` CLI **and** by a stock
  kopia release — worth having for a last-resort artefact. Copying blobs needs no
  Data Key, which is what keeps `takeout` structurally unable to decrypt.

- **`takeout` has no key input at all, and this is enforced by test.** Not a
  convention: `cmd/takeout` is audited for key-accepting flags and for any
  reference to the unwrap APIs. S3 credentials come from the environment, never
  from flags (command lines are world-readable).

- **The Recovery Key is never a command-line argument.** `decrypt` prompts
  without echo, or reads one line from stdin when piped.

- **Path B streams from kopia straight into CS3 (no staging).** The snapshot
  engine grew a `Walk` that yields entries with lazy readers, mirroring the
  Phase-4 decision that no Space is ever staged on disk.

- **Path B restores never overwrite.** Each run writes into its own
  `Restore/<timestamp>/` folder (timestamp is filename-safe, no colons). A
  restore and a backup for one Space share the per-Space run lock, so they cannot
  overlap.

- **Path B is member-only, with no admin variant.** The restore endpoints are
  gated on CS3 membership exactly like the rest of the space-scoped API; an
  OpenCloud admin who is not a member gets 403 (reaffirms #2/#15). The client
  cannot choose a restore destination — the request carries a snapshot id and
  nothing else.

- **Path B unwraps via SRW, not the Recovery Key.** The plaintext RK still never
  crosses the network; a user restoring through the UI is authorized by their
  session, and the worker uses the server wrap it already holds (decision #1).

### Amendments from Phase 6

- **New decision #16 (locked): the service's own state lives in OpenCloud, over
  CS3 — not in a database.** Schedules, run history, wrapped key envelopes and
  target records are documents in a **dedicated Space** reached with the service
  account (`pkg/cs3state`), behind the small `pkg/state` document-store
  interface. This *supersedes the phase-6 plan's "backend: SQLite"*.
  Rationale: the deployment already has one durable, operated, backed-up storage
  system; adding a database and its volume for a family-scale plugin is
  infrastructure nobody asked for.
  **Validated against OpenCloud 7.3.0** (`pkg/cs3state` integration test): the
  service account can create folders, write, *overwrite*, list and delete, and
  run history survives a new store instance.
  *Constraints this imposes, which are binding:*
  - The state Space must be one **no end user is a member of**. A member could
    delete the service's memory, and it is not a user's document. Since the R1
    remediation this is **enforced at startup**: the service refuses to start
    against a personal Space or one carrying any member grant.
  - A CS3 Space is a filesystem: **no transactions, no compare-and-set**. Nothing
    is built on pretending otherwise (see the lock decision below).
  - **Records whose loss is unrecoverable are append-only; only re-derivable
    records may be replaced in place.** See the R1 amendment below.
  - Only ciphertext (SRW/TW envelopes) and metadata are stored — never plaintext
    key material. That is defence in depth, not a licence to relax the first
    constraint.

- **Mutual exclusion is process-local; the durable lease only covers crashes.**
  Two runs racing inside one process are prevented by a mutex — exact and cheap.
  The persisted lease exists so a process that *died* holding a Space is cleaned
  up: it carries an expiry, is renewed while the run lives, and `Recover` marks
  the abandoned run failed and frees the Space. The durable half deliberately
  does **not** attempt cross-process exclusion, because the backend has no CAS
  and faking it would be the subtle bug rather than a fix for it.
  **Running two instances against one state Space is unsupported** — the
  single-instance deployment target is now a correctness requirement, not just a
  simplification.

- **Due-ness is derived from the last *attempt*, not the last success**, and from
  stored history rather than an in-memory registry. Two consequences fall out and
  both are wanted: downtime catches up **exactly once** (one overdue occurrence,
  which then becomes the new baseline), and a Space whose runs keep failing
  retries on its normal schedule instead of hammering a broken target every tick.
  The stale-backup notification, not a retry loop, is what stops that failure
  being silent.

- **Jitter is derived from the Space id, not randomised.** Spaces sharing a
  schedule are spread across the window, and each keeps its slot across restarts
  instead of wandering.

- **Notifications split by audience, and decision #15 wins over the phase plan.**
  The phase-6 plan said "notify space owner + admin" on failure. Space members
  get per-space events (run failed, backup stale). The **operator gets
  operational events only, carrying no space id and no user** (target unusable) —
  enforced in `pkg/notify` by validation, not convention. Telling an admin that
  *this* Space is failing would hand them exactly the visibility #15 denies them.
  Delivery: events are always **recorded** (durable, served by the API) and
  delivered best-effort. v1 sinks are structured logs and SMTP for operator
  events. OpenCloud's own notification service was the plan's first choice but no
  Phase-0 spike established a usable API for an external plugin, so it is not
  implemented on speculation; it becomes another sink when verified. Member
  events have no email path yet (that needs the user directory) — they are
  recorded and served, not dropped.

- **Live progress during a run is not tracked.** Job records carry the file and
  byte counts a run *processed*, written when it finishes. Streaming progress
  would mean wiring kopia's uploader-progress interface through the snapshot
  engine boundary; the status board reports "running since <time>" plus the last
  completed run's counts instead. Deferred to Phase 8 if the UI proves it needs
  more.

- **Retention of run history is time-based too** (`JOB_HISTORY_DAYS`, default one
  year), pruned on its own slow cadence. Consistent with decision #10: nothing in
  this system expires by count.

- **The history endpoint stayed `GET .../backup/runs`** (the phase plan wrote
  `.../backup/jobs`). It is the Phase-4 route, it now takes `?limit=`, and
  renaming a live contract to match a planning doc would be churn for nothing.

### Amendments from the September 2026 review — R1 (durable state)

- **#16 amended: key, target and Space-config records are append-only and are
  never replaced.** A write adds an immutable document named after a fixed-width
  nanosecond timestamp; a read takes the newest. Layouts:
  `keyenvelopes/<space-id>/{rk,srw}/<nanos>`, `targetrecords/<id>/<nanos>`,
  `targetgrantlists/<id>/<nanos>`, `spaceconfigs/<space-id>/<nanos>`.
  Superseded versions are kept: they are tiny, they are ciphertext where they
  hold key material, and they are the only audit trail a rotation leaves.
  Rationale: replacing a document in place on a backend with no transactions is
  destructive, and for a wrapped Data Key the loss is permanent — no envelope,
  no restore, ever.
  Records the service can re-derive after a restart — **leases and job
  records** — keep replace-in-place semantics. The distinction is enforced by
  the `state.Store` interface, which offers `Create` (refuses to overwrite) and
  `Replace` (explicitly destructive) rather than a single `Put`.
  The pre-versioned layouts (`keys/`, `targets/`, `targetgrants/`, `spacecfg/`)
  are still read when a record has no version yet, and are never rewritten.

- **Pinned against OpenCloud 7.3.0: `InitiateFileUpload` over an existing path
  overwrites silently** — it does *not* return `ALREADY_EXISTS`
  (`TestIntegration_CS3StateOverwriteSemantics`). Consequences, both recorded
  because they are load-bearing:
  - Nothing in the transport prevents a write from destroying a stored envelope,
    so `cs3state.Create` checks the folder itself before writing. "The upload
    would have refused" is not a guarantee anything may rely on.
  - **Each overwrite creates a file revision that is never reclaimed** (measured
    on the fixture: five writes to one path left four revision nodes). Leases are
    renewed every few minutes, so a long-lived deployment accumulates revisions
    for the lease and job documents. They are small, but the growth is unbounded
    and there is no purge in this codebase yet. **Open item**, not addressed by
    R1: either purge revisions on a slow cadence, or document the OpenCloud-side
    cleanup an operator must run.

- **The SRW envelope is published to the target** as
  `<prefix>keys/<space-id>/server.ocbke`, next to the RK-wrapped
  `recovery.ocbke` (R1 option D). Rationale: the state Space was the only place
  the service's own copy existed, so losing it cost every unattended backup for
  that Space rather than a re-configuration.
  **This is a trust-model change and is recorded as such:** an attacker holding
  both the target's contents *and* the cluster's SRW key can now decrypt without
  also holding the state Space. The marginal exposure is small — the SRW key
  already lives in the same cluster as the service account that can read every
  Space in plaintext — but it is real. The recovery envelope's guarantee is
  unchanged: it remains openable only with the user's Recovery Key.
  Recovering *from* `server.ocbke` is currently a manual operator step (fetch
  the object, unwrap with the SRW key, re-seed the state Space); no code path
  reads it back yet. Phase 5b would make it a flow.

### Amendments from the September 2026 review — R2 (ceremony, rotation)

- **New decision #17 (locked): establishing a Space's keys is a once-only act.**
  `POST .../backup/setup` answers 409 for a Space that already holds both
  envelopes, and there is no override flag.
  Rationale: a second ceremony installs a new Data Key. Every snapshot already
  written stays encrypted under the previous one, which nobody holds any more —
  the backups remain listed, intact and permanently unreadable, with no attacker
  involved and no warning. That is the exact outcome this project exists to
  prevent, so it is not something a member gets to do by clicking twice.
  A *half-finished* setup (one envelope, from a crash between the two writes) can
  still be completed by re-running it: there are no snapshots behind it to
  orphan. The predicate is "both present", not "any".

- **New decision #18 (locked): every key in the system is replaceable, and
  replacing one never rewrites data.**
  - The **Recovery Key** rotates through `POST .../backup/recovery-key/rotate`:
    the browser unwraps with the old RK, re-wraps the *same* DK under a new one,
    and posts the envelope alone. The DK is not sent on this path.
  - The **SRW and TW keys** rotate through `backupd rotate-srw` / `rotate-tw`
    (`pkg/rotate`), which visit every Space or target and re-wrap in place.
  - The **Data Key** does not rotate. Re-keying it would mean re-uploading every
    backup, and no threat this system models is answered by it.
  Rationale: a key that cannot be replaced forces the wrong choice after a
  suspected exposure — keep using it, or destroy the history. Both were the only
  options before this.
  *Constraints, which are binding:*
  - **Rotation is resumable, not atomic.** No transactions (#16), so an
    interrupted rotation leaves records split across two keys. Re-running
    finishes it; a record that opens with *neither* key stops the run rather
    than being rewritten under an assumption that already failed once.
  - **Rotation must not run alongside backups.** The operator asserts it
    (`-service-stopped`) and the command verifies no run lease is live. Neither
    is a lock — there is no CAS — so this catches the mistake that happens, not
    every possible race.
  - **The old key must be removed from the deployment afterwards.** Nothing
    opens with it any more; leaving it configured only widens what a leak costs.

- **New decision #19 (locked): client-produced Recovery Key envelopes must meet
  a minimum work factor** (`keys.MinArgonParams`: Argon2id, 2 passes, 19 MiB,
  1 lane — OWASP's low-memory baseline), checked at the API boundary on setup
  and rotation.
  Rationale: the server cannot see the Recovery Key, so the work factor is the
  only thing about the client's ceremony it can check at all. The envelope will
  sit in a state Space, be published to an S3 target, and end up in whatever
  Take-Out an operator hands over; if it is cheap to brute-force, nothing else in
  the design matters. Distinct from the existing Argon2id *ceilings*, which are
  DoS protection for the server and stay exactly as they were.
  The floor is checked **on the way in only**. An envelope already stored keeps
  unwrapping whatever its costs — the alternative is a Recovery Key that stops
  working because the server changed its mind.

- **What the server still cannot verify, and says so:** that a client's envelope
  wraps the DK it claims, or opens with the key the user was shown. Checking
  either would need the Recovery Key. The obligation is written into
  `key-envelope-format.md` §5 as a binding client invariant, and Phase 8's
  interop test is where it gets enforced for the shipped client. The mitigation
  for a client that gets it wrong is #16's append-only storage: the superseded
  envelope is still there.

### Amendments from the September 2026 review — R3 (roles, expiry, groups)

- **Decision #7 clarified: "member" is now a role, not a boolean.** A CS3 grant
  carries a *permission set*, not a role name, and may be held by a group and/or
  carry an expiry. All three were previously ignored: presence of any key in the
  `grants` map was full authority. The parsed grant now yields
  `{Role, ExpiresAt, Group}` and each route states a minimum role. The table is
  in `phase-2-auth-spaces.md`; the parsing is pinned against a live OpenCloud by
  `TestIntegration_SpaceGrantsShape`.

- **#7 stands as written for retrieval *and* restore.** Any non-expired member —
  viewer included — may retrieve the recovery envelope and trigger a restore.
  Only the *management* actions moved up: key setup and Recovery-Key rotation
  now require the manager role, because they decide or replace what the whole
  Space's members can decrypt with.
  *Known consequence, accepted deliberately:* a restore writes into the Space
  through the service account, so a viewer can cause files to appear in a Space
  they cannot otherwise write to. The write is confined to a fresh
  `Restore/<timestamp>/` folder and never overwrites live data (#3), and #7's
  premise — that disaster recovery is a member capability rather than a
  management one — was judged to outweigh it for the family scenario. If this is
  revisited, the change is one row of the role table.

- **New decision #20 (locked): a grant that cannot be evaluated is refused, not
  guessed.** Group grants are resolved from
  `GET /graph/v1.0/me?$expand=memberOf` with the caller's own bearer token —
  OpenCloud 7.3.0 puts no groups claim on the access token and has no
  `/me/memberOf` route. When that lookup is unavailable or fails, a Space
  carrying a group grant answers 503/502 for the callers who would need it.
  Rationale: treating an unresolvable group as "no groups" silently denies a
  legitimate member; treating it as "member" silently grants a stranger. An
  error is the only answer that is not a wrong decision.
  *Constraint:* groups are resolved **lazily** — only when the caller's own
  grant falls short *and* the Space actually has a group grant — and memoised
  per request. A Space granted to users only, and a caller whose direct grant
  already suffices, cost no upstream call.

- **Reva prunes expired grants itself, but the check stays.** On OpenCloud 7.3.0
  an expired grant disappears from the `grants` map on the next read. The
  expiry check is nonetheless enforced locally: the pruning is lazy, undocumented
  as a guarantee, and relying on it would make correctness depend on a reva
  implementation detail this project does not control.

### Amendments from the September 2026 review — R4 (kopia correctness)

- **kopia's ignore conventions are disabled; a Space's contents are never a
  policy input.** kopia honours `.kopiaignore` files and `CACHEDIR.TAG` markers
  found *inside* the tree it is backing up. That is right for a laptop, where the
  person writing the rules is the person running the backup, and wrong for a
  user's Space, where anyone who can write a file could otherwise silence the
  backup of everything around it — a `.kopiaignore` containing `*` yields an
  empty snapshot reported as a success. `Uploader.DisableIgnoreRules` is
  therefore set on every run and the marker files are backed up as ordinary
  files. There is no opt-out: an exclusion mechanism a user's *files* can trigger
  is indistinguishable from an attack.

- **Checkpoint manifests are transient and removed when the run ends.** kopia
  saves a partial tree every `CheckpointInterval` (45 min by default) so a long
  upload survives a crash; the saved manifest is a normal snapshot manifest
  carrying `IncompleteReason`. This project never serves one: `List`, snapshot
  lookup for restore/walk, the offline `decrypt` tool's default selection and
  prune's "newest snapshot" protection all filter on completeness. Every run —
  successful or failed — deletes the source's incomplete manifests before
  returning, the failure path in a write session of its own because kopia's
  checkpoints flush themselves and outlive a rolled-back session. Prune deletes
  any that remain, regardless of the retention window, since only a killed
  process can leave one behind. The orphaned content they referenced is reclaimed
  by the next maintenance pass.
  *This is what makes the Phase-4 amendment "a partially-read Space is a failed
  run" true of what the repository holds, and not only of what the run returns.*

- **Prune's protection is on the newest *complete* snapshot.** Protecting the
  newest manifest of any kind would let a mid-upload checkpoint stand in for it,
  and the last genuinely restorable backup be deleted underneath it — retention
  destroying exactly what it promises to keep.

### Amendments from the September 2026 review — R5 (deployment hardening)

- **#14 made exact: target credentials never touch a filesystem.** kopia will not
  open a repository without a local config file, and `repo.Connect` serialises the
  storage's connection settings into it — for S3, the secret access key in clear
  text. The repository is therefore connected through a **storage handle**: a
  type registered with kopia's own `blob.AddSupportedStorage` whose serialised
  form is a random per-run token resolved against an in-process registry. The
  credentials exist in exactly two places — the TW-wrapped record in the state
  Space, and process memory for the duration of a run — on any filesystem, after
  any kind of crash.
  *Also true of kopia's per-run cache, by a different mechanism:* it holds
  repository content (ciphertext), so `BACKUP_WORK_DIR` must be memory-backed and
  the service refuses to start otherwise unless
  `BACKUP_WORK_DIR_ALLOW_DISK=true`. Run directories left by a killed process are
  swept at startup.

- **#16's single-instance constraint is enforced, not just documented.** The
  manifest deploys `strategy: Recreate` — the default rolling update ran two
  instances against one state Space on every image update — and each instance
  writes a short-lived record announcing itself, refusing to start while another
  one is live. **This is not a lock**: the same missing compare-and-set that stops
  the run lease from being one stops this from being one. It catches the rollout
  case, which is the one that actually happens.

- **Durable state is required.** `STATE_SPACE_ID` unset used to mean "keep
  everything in memory, with a warning". The loss is silent, arrives on an
  ordinary restart, and includes every wrapped Data Key; a warning in a log is not
  consent. A throwaway instance now says `STATE_BACKEND=memory` explicitly.

- **New decision #21 (locked): a token is accepted only if it was issued for this
  service.** `OIDC_AUDIENCE` is required whenever `OIDC_ISSUER` is set — one
  OpenCloud issuer signs tokens for several clients, so "signed by the right
  issuer" says nothing about who the token was for — and a token without an `exp`
  claim is rejected rather than being valid forever. JWKS refetches are
  rate-limited so an unknown key id, which anyone can mint unauthenticated, cannot
  be turned into load on the household's identity provider.
  *Corollary:* discovery moved to first use with background retry. An identity
  provider that is a few seconds late in a co-ordinated restart now costs a 503 on
  authenticated routes, not a crash-looping backup service.

- **New decision #22 (locked): retention has a floor of 7 days.** Retention depth
  is the entire defence against slow-burn ransomware, and setting it required no
  more than a member's browser session — which, in the threat this project exists
  for, is what the attacker has. The API refuses a shorter window with an
  explanation; `EffectiveRetentionWindow` also raises anything below the floor,
  so a record written before the floor existed cannot make prune delete history
  the API would not have given up.

- **The listener is plain HTTP and that is now stated where a deployer reads
  it** (README preconditions, manifest comment): it must sit behind a
  TLS-terminating ingress on the same origin as OpenCloud, because the DK crosses
  it once at setup. `TLS_CERT_FILE`/`TLS_KEY_FILE` cover deployments with nothing
  in front of them.

### Amendments from the September 2026 review — R6 (scheduler and runner)

- **A run gives up its lock only after its outcome is durable.** The order used
  to be the other way round, and one lost write was permanent: the job record
  stayed at `running`, the lease that would have recovered it was already gone,
  and every later tick saw a run in progress that did not exist — that Space
  never backed up again, silently. The outcome write is now retried, on a context
  detached from the run's (a run stopped by shutdown or by its own deadline must
  still be able to say why), and if it cannot be recorded the **lease is kept on
  purpose**: it expires, and recovery closes the run out.
  *Second line of defence:* recovery also sweeps, on a slow cadence, for jobs
  recorded as running that no live lease corresponds to — the one shape the
  expired-lease pass cannot see, since it has no lease to expire.

- **Unattended runs are bounded in time.** A scheduled run had no deadline at
  all, so a target that accepted a connection and then stalled held one of the
  service's few run slots until the process restarted. It now takes the same
  bound a backgrounded manual run has, and a run stopped that way records "the
  backup run timed out" rather than whichever read happened to be in flight.

- **Transfers must make progress, not finish quickly.** Reading a file out of
  OpenCloud has no overall timeout — a large file may legitimately take an hour —
  but a transfer that moves no bytes for two minutes is failed. The guard stops
  watching at end of input, so a server finalising an upload is not cut short.
  *It does not cover a source that blocks forever inside a single read:* Go's
  HTTP transport waits for its write loop, so cancelling cannot unwedge that.
  The run timeout covers it.

- **"Is a run already under way" is answered by the run lock, not by history.**
  A non-terminal job record is not evidence — it can outlive the run it describes,
  which is how the wedge above became permanent. The lease can not: it expires.
  Due-ness now reads one history record for the schedule baseline (widening only
  when the newest run was a restore) and asks the lock about the rest.

- **Silence is reserved for things that are fine.** A stored document that cannot
  be decoded used to be skipped without a trace, which for a Space configuration
  means that Space silently stops being scheduled. The store now reports
  unreadable records by key: the scheduler logs each one, and the monitor raises
  an operator notification carrying **only a count** — the key contains a space
  id, and #15 forbids naming a Space to the operator.

- **Everything the service keeps is trimmed.** Notification history grew forever;
  it is now pruned with the run history it describes, on the same cadence and to
  the same window.

- **An idle service is close to idle.** The staleness sweep runs every 15 minutes
  rather than every minute (staleness is measured in days), and the worker's reva
  token is reused until shortly before its own expiry instead of being minted
  once per gateway call — which had made a thousand-file backup a thousand extra
  round trips against the OpenCloud instance this service sits beside. A token
  reva rejects is dropped immediately.

- **Schedules default to the container's timezone (`TZ`), not UTC.** "Nightly at
  02:30" is about the family's night; a household that sets `TZ` has already said
  which zone it means. `SCHEDULE_TIMEZONE` still overrides it, and an unknown
  zone is refused at startup rather than at 02:30.

### Amendments from the September 2026 review — R7 (prune and maintenance)

- **Tier 1's prune job exists, and it runs in this process.** Everything above
  described retention as something a separate job did; nothing did it. Retention
  is now applied by a job of its own kind (`prune`), started by the scheduler on
  its own slow cadence (daily by default, `PRUNE_INTERVAL_HOURS`), holding the
  same per-Space run lock a backup takes. **What Tier 2 changes is the
  credentials, not the design:** the worker gets a write-only key and this job a
  key that may delete. The job kind, the lock and the cadence stay.

- **Retention has two numbers and they belong to different people.** How much
  history a Space keeps is the Space's setting, per Space, time-based, floored at
  a week (#10, #22). How often that window is *enforced* is the operator's, one
  value for the deployment, staggered per Space so a household does not run full
  maintenance against every repository at the same instant. Making the cadence a
  per-Space setting would have been a knob nobody wants and a UI surface nobody
  asked for.

- **A Space with no successful backup is never pruned.** There is no repository
  to open until one run has finished, so a prune could only fail — daily, for as
  long as the Space stays broken, filling its history with failures that describe
  a symptom rather than the cause. This also covers a Space whose runs never get
  past a checkpoint: an incomplete run is not a success (R4 amendment), so it
  never makes a Space prunable.

- **Maintenance ownership is enforced rather than announced.** The code claimed
  the repository's maintenance owner on first use and then passed kopia's
  `force` flag, which skips the very check the claim exists for. The flag is gone.
  A repository some other kopia client has claimed now fails the prune with a
  clear error instead of two uncoordinated processes rewriting the same indexes;
  the other owner's identity stays out of the job record, which a user can read.

- **A failed prune is not a failed backup, and is not reported as one.** The
  member-facing notification path is not called for prune runs: the data is
  exactly where it was, and only the reclamation did not happen. It is on the job
  record and in the operator's log, which is where somebody can act on it.

- **A job's kind is part of its key.** Run history is one stream per Space, and
  adding a daily prune to it would have halved the reach of every bounded scan
  that looks for "the last backup" — the schedule baseline, the staleness check,
  the status board — while roughly quintupling the reads an idle deployment makes
  per tick. The durable layout is now
  `jobs/<space-id>/<nanos>-<kind>-<job-id>`, so those questions are answered by
  one listing and one document read, whatever else happened in between. Records
  written under the old layout are still read; they simply cost a read to
  classify. This retires R6's "widen the read when the newest run was a restore"
  workaround, which prune would have made the common case.

---

## Trust & key model

- **DK (Data Key):** 256-bit random, per Space; doubles as the kopia repo
  password; plaintext only in memory during a run; never stored plaintext.
- **RK (Recovery Key):** generated **client-side** at setup, shown once, stored by
  the user externally (password manager). Plaintext RK **never crosses the wire**.
  Wraps the DK via an Argon2id-derived KEK. Replaceable without touching the DK
  (`POST .../backup/recovery-key/rotate`); the re-wrap happens in the browser and
  the DK is not sent on that path.
- **The DK *is* sent to the server, once, at setup, over TLS.** That is the
  price of decision #1: unattended runs need the server to reconstruct the DK, so
  it must receive it to produce the SRW wrap. It is held in memory for the
  duration of that request, zeroized after wrapping, and never stored in
  plaintext. Saying this plainly matters — "the Recovery Key never crosses the
  network" is true and is sometimes misread as "no key material ever does".
- **SRW (Server Runtime Wrap):** server-held wrapped DK enabling unattended runs;
  KEK from K8s secret / KMS. The wrapped DK is stored in the state Space **and
  published to the target** as `server.ocbke` (R1 amendment above), so the state
  Space is not a single point of failure for unattended runs. Retirable with
  `backupd rotate-srw` (R2 amendment below).
- **TW (Target Wrap):** a cluster/KMS-held key that wraps **S3 target
  credentials** at rest (decision #14). Same custody class as SRW — lives in a
  K8s secret / KMS, never in the admin UI, never logged. Distinct key from SRW so
  the two concerns (data-key custody vs. target-credential custody) rotate
  independently — and the service **refuses to start** if the two are configured
  to the same value, because that collapses the separation while looking like it
  works. Retirable with `backupd rotate-tw`. The wrap uses the same maintained AEAD-envelope primitive as the
  DK wraps; **no hand-rolled crypto.**
- **Envelope format is a long-term compatibility promise** — versioned from day
  one (the standalone decrypt CLI must parse it forever). The TW-wrapped
  credential blob is versioned for the same reason. The **Take-Out manifest**
  (`manifest.json`) joins the same promise: `decrypt` must keep reading every
  version of a Take-Out it has ever produced.

---

## Threat model

**Primary threat: a user's own machine gets ransomware-encrypted.** Chain:
ransomware encrypts local files → OpenCloud client syncs them up → OpenCloud holds
garbage → next scheduled run would snapshot the garbage.

Why we are still protected:

1. **The backup worker runs server-side**, holds the S3 write credentials in the
   cluster (never on the client). Client ransomware **cannot reach the backup
   store** — it has no credentials. Object Lock is not even required against this
   (the most likely) attacker.
2. **Snapshot history is the real protection.** The encrypted files become one bad
   snapshot; prior good snapshots remain. Recovery = restore yesterday.
3. **The critical property is retention depth, not WORM.** Slow-burn ransomware is
   defeated by deep, time-based retention.

**Secondary threat: server/cluster compromise** leaking S3 credentials (and
possibly the prune key). Less likely in the family scenario; addressed by
immutability Tiers 2/3.

**Secondary-threat delta from in-app target management (decisions #12/#14):**
moving target credentials into an app-managed store widens this threat: one
database compromise could expose *all* targets' credentials instead of custody
being limited to K8s secrets. Mitigations that keep the delta acceptable:

- The database stores **only ciphertext** (TW-wrapped credential blobs). The TW
  key itself stays in the cluster secret / KMS, **not** in the database, so a
  stolen database alone does not yield plaintext credentials.
- Credentials are **write-only** across the API and UI (decision #14): never
  returned by any read path, so an admin-session or API compromise cannot
  exfiltrate existing credentials, only overwrite them.
- Plaintext credentials exist **only in worker memory at run time** and are
  zeroized after use where Go allows; they are never logged.
- The blast radius is still bounded by immutability Tiers 2/3 (a leaked S3
  **write** credential cannot rewrite history when prune/GC runs from a separate
  trusted context with the owner key).

---

## Restore paths (contract)

- **Path A — Admin Take-Out (primary, acceptance-critical).** Must work with
  OpenCloud **fully down**: admin extracts an encrypted, self-contained blob from
  S3 (ciphertext only, no key input possible); user decrypts locally with the RK
  via the standalone `decrypt` CLI, independent of OpenCloud and the server.
- **Path B — User restore into OpenCloud.** User-only, via GUI; full restore into
  `Restore/<ts>/`; requires OpenCloud to be up.
- **A backup path is not "done" until its restore path is tested.** Path A is the
  acceptance-critical path.

---

## Success criteria (definition of done for v1)

1. Single static Go binary.
2. A scheduled run produces an encrypted, deduplicated snapshot of a Space on
   Garage — content and paths fully obfuscated; files + structure + mtime.
3. With OpenCloud down, admin Take-Out + offline `decrypt` (RK only) recovers the
   data.
4. User Path B restore lands a full snapshot in `Restore/<ts>/`.
5. Admin operates scheduling/storage/Take-Out **without ever seeing plaintext**
   and cannot restore into a user's space.
6. A shared Space is backed up exactly once.
7. UI looks natively integrated into OpenCloud.
8. Every backup path has a tested restore path.
