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
   as overkill for the family scenario.

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

---

## Trust & key model

- **DK (Data Key):** 256-bit random, per Space; doubles as the kopia repo
  password; plaintext only in memory during a run; never stored plaintext.
- **RK (Recovery Key):** generated **client-side** at setup, shown once, stored by
  the user externally (password manager). Plaintext RK **never crosses the wire**.
  Wraps the DK via an Argon2id-derived KEK.
- **SRW (Server Runtime Wrap):** server-held wrapped DK enabling unattended runs;
  KEK from K8s secret / KMS.
- **TW (Target Wrap):** a cluster/KMS-held key that wraps **S3 target
  credentials** at rest (decision #14). Same custody class as SRW — lives in a
  K8s secret / KMS, never in the admin UI, never logged. Distinct key from SRW so
  the two concerns (data-key custody vs. target-credential custody) rotate
  independently. The wrap uses the same maintained AEAD-envelope primitive as the
  DK wraps; **no hand-rolled crypto.**
- **Envelope format is a long-term compatibility promise** — versioned from day
  one (the standalone decrypt CLI must parse it forever). The TW-wrapped
  credential blob is versioned for the same reason.

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
