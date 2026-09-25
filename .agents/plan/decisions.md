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
   *Status:* the **probe** is built (Phase 7): the service asks each target what
   it supports on every maintenance run, and logs the answer. The **enabled
   path** is not, and deliberately so — no backend in this project's test
   environment implements Object Lock, so code written for it would be untested
   from the day it landed. Issue #33 tracks closing that gap. There is no
   feature flag, because a flag no backend can exercise is the placeholder R9
   deleted, wearing a different hat.

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
   **no API-enforced append-only**. Immutability handled out-of-band. The
   grant table is now measured rather than asserted (see the Phase-7 amendment
   and `phase-0-findings.md`), and one part of it was measured to be the
   opposite of what this entry implied: `owner` is bucket administration and
   grants **no object access**, so it is not the "more powerful" grant a
   maintenance role could be built on.

9. **Immutability is layered:**
   - **Tier 1** (covers the primary threat fully): server-side worker; write key
     never on the client; deep time-based retention; prune runs as a **separate
     job** from backup. **Implemented** (see the R7 amendment), and since
     Phase 7 both of its claims are tested rather than argued.
   - **Tier 2** (credential separation): backup and maintenance runs resolve
     **separate credentials** from the target record, and neither run ever holds
     the other's. **Implemented as a separation, not as a bound** — on Garage
     both credentials must hold `read+write`, so a leaked backup credential can
     still delete. See the Phase-7 amendment for why the original design (a
     write-only worker key, an owner key for prune) cannot work on this backend.
   - **Tier 3** (the only real out-of-band WORM available today): ZFS/Btrfs
     snapshots of the Garage `data_dir`/`meta_dir`. **Not code this project
     ships**; it is an operator practice, and since Phase 7 it is written down —
     README, "Protecting backups from the credential that writes them".

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
    *Status:* the target store, the grant model, the server-side enforcement and
    the **admin API** exist — `/api/v1/admin/targets` and `.../grants` are real
    since sub-phase 8b, so the admin is no longer limited to the optional
    first-start seeding below. The **admin UI** lands in sub-phase 8e.

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
    enters credentials in the UI (Phase 8; today, first-start seeding); the app
    stores them **wrapped by a
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
  reopen locally. The copy therefore runs at kopia's *blob* level, which is what
  makes the artefact a well-formed kopia repository on disk rather than a private
  format. Copying blobs needs no Data Key, which is what keeps `takeout`
  structurally unable to decrypt.
  **What this does *not* promise is that a stock kopia release can open it.**
  The repository password is the Data Key — 32 raw random bytes — and kopia's own
  tooling takes a password as terminal input, an environment variable or a config
  file, none of which carries arbitrary binary. So the layout is standard and the
  credential is not: `decrypt` remains the supported way in. (An earlier version
  of this file claimed stock-kopia openability outright; it was never true and
  never tested. Making it true would mean encoding the Data Key as text before
  using it as a password, which re-keys every existing repository — not worth it
  for a property nobody needs.)

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
  run history survives a new store instance. Since R9 this is enforced: CI starts
  the pinned OpenCloud, runs these tests against it, and fails rather than skips
  when the fixture is missing. The four Phase-6 exit criteria run over this store
  as well as the in-memory one.
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
  get per-space events (run failed, backup stale). The **operator's notification
  records carry no space id and no user** (target unusable) — enforced in
  `pkg/notify` by validation, not convention. Telling an admin *in a
  notification* that this Space is failing would hand them exactly the visibility
  #15 denies them.
  Delivery: events are always **recorded** durably and delivered best-effort.
  Member events are served by the API; operator events are recorded, logged and
  mailed, and there is no read endpoint for them yet (the admin surface is
  Phase 8). v1 sinks are structured logs and SMTP for operator events.
  OpenCloud's own notification service was the plan's first choice but no
  Phase-0 spike established a usable API for an external plugin, so it is not
  implemented on speculation; it becomes another sink when verified. Member
  events have no email path yet (that needs the user directory) — they are
  recorded and served, not dropped.

- **The service log is a different surface, and it does name Spaces** (R8). Every
  runner, scheduler and monitor line carries `space=<id>`, and the always-wired
  log sink logs member events too. #15 is about what the *product* shows an
  administrator — the admin UI, the notifications they receive, the API they can
  call — not about what a process writes to stdout. In this deployment the
  operator is the person who owns the machine, has the service account, and can
  read every Space in plaintext anyway; withholding space ids from the logs would
  make failures undiagnosable to the only person who can fix them, in exchange
  for a boundary that person is already on the wrong side of.
  *So the honest statement of the property is:* the operator's notification
  records, and the mail built from them, never identify a Space or a user; the
  logs do. Anything that changes that — a hosted deployment where the operator is
  not the household — has to revisit this, and the place to start is the log sink
  and the space-id fields in the runner and scheduler.

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
  *One exception, by the operator's own choice:* when first-start seeding is
  enabled the same credentials are also in the deployment's environment
  (`BOOTSTRAP_S3_*`), for the life of the process and readable to anything that
  can read `/proc/<pid>/environ`. That is the ordinary custody of a Secret-backed
  variable, no worse than `SRW_KEY` beside it, but it is not "two places" and is
  worth knowing before enabling it.
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

### Amendments from the September 2026 review — R8 (docs-to-code truth pass)

R8 changed prose, not behaviour, with one exception noted below. What it corrected
is listed here so the corrections are themselves on the record:

- **Unbuilt things now say so.** Immutability Tiers 2 and 3, the Object-Lock
  capability probe, and the admin/target UI were all written in the present
  tense. Each now carries its status. The threat model no longer credits Tier 2
  with bounding a blast radius it does not bound yet, and no longer mentions a
  "prune key", which has never existed.
- **"Openable by a stock kopia release" was never true** and is retracted, with
  the reason (the repository password is 32 raw bytes) and the cost of making it
  true (re-keying every repository) recorded so it is not re-proposed.
- **"The operator is never told which Space is failing" was true of the wrong
  half.** It holds for notification records and mail; the service log names
  Spaces throughout, deliberately. The property is restated as what it is.
- **"Validated against OpenCloud 7.3.0" meant a test run by hand.** None of those
  tests ran in CI, because CI started no OpenCloud. R9 fixed that; the wording
  here is left as the record of what it was.
- **The service-account secret joined the trust and threat models** as the
  highest-value credential in the deployment. It was documented in the manifests
  and absent from the model those manifests implement.
- **One behavioural change:** the shipped manifest's `REPLACE_ME` placeholders for
  the OIDC audience and the state Space id started up happily, contradicting R5's
  "fails loudly". Startup now refuses a value that still says `REPLACE_ME`. A
  deployment that came up and then rejected every token was worse than one that
  did not come up.
- **Structural, not conventional:** the offline decrypt code moved into a package
  of its own (`pkg/takeout/decrypt`) so the admin's Take-Out binary is *built*
  without it, asserted by a dependency-graph test. `pkg/keys` is still linked
  there — extraction reads envelope headers — and that limit is stated rather
  than glossed.

### Amendments from the September 2026 review — R9 (real-OpenCloud CI)

- **A skipped test is now a failed one, where a fixture was promised.** Every
  OpenCloud-dependent test skipped itself when no OpenCloud was present, and CI
  never started one — so they all skipped, every run, and the summary was green.
  `OPENCLOUD_FIXTURE_REQUIRED` inverts that for the CI job that does start the
  fixture: a missing variable, or a Space that is not there, fails and says
  which. Locally the skip stays, because a unit-test run should not depend on
  Docker.

- **Path B is tested against reva, and the first run found a bug.** A zero-length
  upload is *completed by `InitiateFileUpload` itself* on OpenCloud 7.3.0; the
  PUT that used to follow is answered 500. Every restore would have failed on the
  first empty file in a Space. The in-memory Space that stood in for reva could
  not have shown this — it agreed with the code by construction. Empty files,
  empty directories, names with spaces and non-Latin names, and modification
  times are now round-tripped through a real Space; `X-OC-Mtime` is honoured, so
  the mtime in backup scope (#4) is asserted rather than assumed.
  *The general lesson, worth more than the fix:* a fake at a boundary tests the
  code's idea of the boundary. It is worth having for speed and for failure
  injection, and it is not evidence about the other side.

- **The Phase-6 exit criteria run over the real state store.** Unattended run,
  restart safety, crash recovery and stale reporting are claims about
  durability, and were only ever exercised against the in-memory store — which
  has none of the properties that make the real one hard. Each now runs twice,
  the second time against an OpenCloud Space over CS3.

- **Dead code was removed rather than documented.** A capability-probe package
  nothing imported and a `Walk` method on the CS3 read boundary that no
  production path called, plus the six test fakes that implemented it. Both were
  described in this file as if they existed for a reason.

### Amendments from Phase 7 (immutability hardening, issue #32)

- **Tier 2 shipped as a separation of actors, not as a limit on either actor,
  because Garage cannot express the limit.** The plan was a write-only key for
  the worker ("a stolen writer key cannot exfiltrate existing backups") and the
  owner key for prune. Measuring the backend first — which the plan asked for and
  which nobody had done — showed both halves fail:
  - `--write` includes `DeleteObject`, so a "writer" key destroys as easily as it
    writes. #8 already said this; now a test says it.
  - `--owner` is *bucket administration* and grants no object access at all. A
    prune key holding only the owner grant could not have deleted one snapshot.
    This was not a weak assumption, it was an inverted one.
  - A key without `--read` cannot open a kopia repository — the format blob and
    the indexes are reads, before any deduplication decision is made. So the
    write-only worker is not a worker with less reach; it is a worker that fails
    on its first run.

  Both roles therefore hold `read+write`: the same capability. What was built is
  still worth building — a target carries one credential per role, a backup run
  never holds the maintenance one and a prune run never holds the backup one, and
  the two rotate and revoke independently — but **it is not a blast-radius bound
  and this file will not call it one.** The value is realised the day a backend
  with real IAM policies is in scope; the seam is what makes that a configuration
  change rather than a rewrite.

- **A target with one credential stays correct, and is the default.** The sealed
  credential record gained an optional second pair rather than a second record:
  old blobs are flat JSON and keep opening, an absent maintenance pair means both
  roles use the one key, and nothing has to be migrated or re-sealed. A
  *half-configured* maintenance pair is refused at seal time instead of falling
  back, because silently falling back produces a deployment that looks separated
  and is not.

- **Capabilities are observed, not assumed, and only what is observable is
  reported.** The service asks each target on every maintenance run — the run
  that would use Object Lock if there were anything to use, and slow enough that
  the question keeps being asked for the life of the deployment rather than once
  at first start. "Supported" is logged at info because an operator can act on
  it; everything else at debug, because a daily line per Space restating the
  expected answer is how a log becomes unread.
  The probe reports Object Lock as a *capability* (Garage answers
  `NotImplemented`, a real S3 answers with a configuration or
  `ObjectLockConfigurationNotFoundError` — three distinguishable states) and
  versioning only as a *status*. It cannot be a capability: Garage answers
  `GetBucketVersioning` successfully with an empty status, identical to a real
  bucket that never enabled it, and the only call that tells them apart is a
  write this service will not make against a backup bucket.

- **Tier 1's two claims are now tests rather than arguments.** That a ransomware
  snapshot does not evict good history was the sentence the whole project rests
  on and nothing checked it: a Space is backed up, every file is replaced with
  random bytes and a ransom note, it is backed up again, retention runs, and the
  pre-attack snapshot still restores byte-identically. That target credentials
  never reach a client is asserted over the whole route table, against a target
  holding real sealed credentials, for the *most* privileged caller — credentials
  are write-only for everyone, not just for viewers. Both were true; neither
  would have stayed true by accident.

- **The retention floor is what stops the attack that the depth defence invites.**
  Tested with the shortest window a stored record can hold: without the floor
  that prune deletes the pre-attack snapshot and leaves the household with the
  ransomware snapshot as its only copy. The floor is why it does not.

- **Deployment defect found while documenting it:** the shipped manifest's
  commented-out bootstrap credentials sat under `envFrom:`, where an entry with
  `name`/`valueFrom` is not valid. An operator following the comment got a
  rejected Deployment. They are in `env:` now, and `kubeconform` is run over the
  uncommented form as well as the shipped one.

### Amendments from Phase 8 — 8c (the state Space was unobtainable)

- **#16's constraint said "no end user is a member"; the code checked "no
  member at all", and no Space satisfying the second one can be created.**
  Measured against the OpenCloud 7.3.0 fixture while standing `backupd` up for
  the web extension:
  - A project Space created through graph (the path README's runbook told the
    operator to use) leaves its **creator** — a real admin user — holding a
    manager grant.
  - That grant cannot be removed. Graph answers `403 accessDenied`, *"cannot
    remove the last share with manager permissions on a space root"*.
  - So a Space with zero member grants does not exist, `cs3state.Check` refused
    every candidate, and since R5 made `STATE_SPACE_ID` required, **a default
    deployment with durable state could not start at all.** The runbook had been
    impossible to follow since it was written, and nothing caught it because
    every integration test constructs its own store and never calls `Check`.

  The predicate is what was wrong. A Space created **over CS3 by the service
  account** carries exactly one grant, held by the service account itself — no
  end user can reach it, which is the property #16 actually asks for. `Check`
  now discounts a grant held by that one principal and refuses every other, so
  an admin's grant is still refused and a personal Space is still refused
  whoever holds what. With no service-account id configured the check stays
  strict, because silence must not widen a security predicate.

  *The exemption is for one named principal, not for "a single manager".* The
  rejected alternative — allow any lone manager grant — permits precisely the
  case R1 added the check to prevent, since the admin who would hold it is an
  end user.

- **Provisioning is an operator command, because it cannot be a UI step.**
  `backupd provision-state-space` creates the Space as the service account,
  verifies it with the same `Check` startup uses, and prints the id. It refuses
  to run while `STATE_SPACE_ID` is already set: a second state Space leaves the
  first holding every wrapped Data Key with nothing pointing at it. Startup
  deliberately does **not** provision implicitly — on a mistyped
  `STATE_SPACE_ID` that would quietly create a fresh Space and begin writing to
  it, which is the same silent-orphaning failure #17 exists to prevent.

- **The general lesson, which is R9's again in a new place.** `Check` was
  covered by unit tests against a fake Space, and they all passed: the fake
  agreed with the code's idea of what a Space looks like. Nothing had ever run
  the predicate against a Space OpenCloud actually produces. A startup check is
  a claim about the environment, and a claim about the environment tested only
  against a fake is untested.

### Amendments from Phase 8 — 8d.1 (what the member's UI is told)

- **`GET /spaces` carries the caller's own role.** This is not a disclosure of
  membership. Other members' grants stay server-side. The caller's own role is
  what they could learn by trying an action and reading the 403. It is resolved
  under #20: if a group grant could raise the role and groups cannot be
  resolved, the listing fails rather than understating the role.
- **Staleness has one definition** (`notify.StaleRule`). The member
  notification and the status board both use it, over the same history window,
  so they cannot disagree.
- **A job record may carry one path: the restore folder**, which the service
  names itself. It never carries paths from the user's data, which is what the
  earlier rule was protecting.

### Amendments from Phase 8 — 8d.2 (setup order and the schedule's zone)

- **A Space is enabled only once it has keys.** The scheduler never looks at
  keys, so an enabled Space without them fails at every due time and fires a
  `run_failed` notification each time. The wizard therefore binds the target
  with `enabled: false` and switches runs on at the schedule step, which is
  reachable only after the ceremony. Only the client enforces this. The API
  still lets a caller enable a keyless Space (see the 8d.2 outcome).
- **The schedule's zone is part of the API.** `/backup/status` and
  `/backup/schedule` carry `timezone`, the IANA name presets are read in (R6),
  or omit it when the service cannot name it. A preset hour with no zone
  beside it is a time the user cannot interpret.
- **`PUT /backup/schedule` refuses unknown fields.** `enabled` is a plain bool,
  so a misspelt body used to answer 200 while switching backups off.

### Amendments from Phase 8 — 8d.3 (following a restore)

- **Any member can read a single run of their Space**, through
  `GET /spaces/{id}/backup/runs/{jobId}`. It returns the same record the
  history list already gives a viewer, so it discloses nothing new. For a
  run of another Space it answers the same 404 as for an id that never
  existed, so a member of one Space cannot use it to probe the runs of
  another.
  The lookup is scoped by the Space's own history (`jobs.Store.GetInSpace`)
  and not by the process-wide id index. The index can be stale in a process
  that did not create the job, and there a stale index would report the
  restore as gone.

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
- **Service-account secret (`OC_SERVICE_ACCOUNT_SECRET`):** not a key in this
  scheme, and the most valuable secret in the deployment. It carries **owner
  scope on every Space**, which is to say plaintext — it is how the worker reads
  the data it backs up. SRW and TW only ever unlock *ciphertext*; this one does
  not need to unlock anything. Whoever holds it does not need a Data Key, a
  target, or this service. Same custody as SRW/TW (cluster Secret, never in the
  admin UI, never logged), rotated in OpenCloud rather than here.
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

**Secondary threat: server/cluster compromise** leaking the S3 target
credentials. Less likely in the family scenario, and **still not bounded by
anything in S3**: a leaked target credential can delete a repository as well as
write to it, because Garage has no grant that writes without deleting
(decision #8, measured). Tier 2 shipped, and what it changed is *which* actor
holds which key, not what a key can do — so it narrows who to suspect and what to
revoke, and it does not narrow the damage. What still holds: the credential opens
ciphertext only. What actually bounds the damage is Tier 3, the operator's own
filesystem snapshots of the storage host, which is documented in the README and
is not something this project can ship.

**The worst single secret to leak is not any of the keys.** It is
`OC_SERVICE_ACCOUNT_SECRET`: owner scope on every Space, i.e. plaintext, with no
Data Key, target or backup involved. An attacker holding it does not need to
attack this service at all — they read OpenCloud directly. It is listed here
because the key model's careful separation of SRW, TW and DK can otherwise read
as if those were the crown jewels; they unlock ciphertext, and this one unlocks
the data itself.

**Secondary-threat delta from in-app target management (decisions #12/#14):**
moving target credentials into an app-managed store widens this threat: one
compromise of the service's state could expose *all* targets' credentials instead
of custody being limited to K8s secrets. (That store is a dedicated OpenCloud
Space, not a database — decision #16.) Mitigations that keep the delta
acceptable:

- The state Space holds **only ciphertext** (TW-wrapped credential blobs). The TW
  key itself stays in the cluster secret / KMS, **not** in the state Space, so
  stolen state alone does not yield plaintext credentials.
- Credentials are **write-only** across the API and UI (decision #14): never
  returned by any read path, so an admin-session or API compromise cannot
  exfiltrate existing credentials, only overwrite them.
- Plaintext credentials exist **only in worker memory at run time** and are
  zeroized after use where Go allows; they are never logged. (Plus the
  deployment's environment, when first-start seeding is used — R5 amendment.)
- The blast radius is bounded by **Tier 3 only** — the operator's out-of-band
  filesystem snapshots. Tier 2 exists and separates the actors, but on Garage
  both credentials hold the same grants, so it bounds nothing by itself. Absent
  Tier 3, the radius is bounded only by how quickly a leak is noticed.

---

## Restore paths (contract)

- **Path A — Admin Take-Out (primary, acceptance-critical).** Must work with
  OpenCloud **fully down**: admin extracts an encrypted, self-contained blob from
  S3 (ciphertext only, no key input possible); user decrypts locally with the RK
  via the standalone `decrypt` CLI, independent of OpenCloud and the server.
- **Path B — User restore into OpenCloud.** User-only, via GUI (Phase 8; the API
  it will call exists today); full restore into `Restore/<ts>/`; requires
  OpenCloud to be up.
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
