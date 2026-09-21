# Phase 7 — Immutability Hardening

**Goal:** implement the tiered immutability strategy (decisions.md #9) on a target
(Garage) that has **no Object Lock / versioning today**.

**Depends on:** Phase 4 (pipeline), Phase 6 (job separation exists).

## Threat recap (drives the tiers)

- **Primary:** client ransomware → synced up → bad snapshot. Defense = worker
  runs server-side (attacker has no S3 credentials) + deep snapshot history.
  Tier 1 fully covers this.
- **Secondary:** server/cluster compromise leaking S3 credentials. Tiers 2/3
  reduce blast radius / provide out-of-band recovery.

## Tier 1 — verify what's already designed in (no new code expected)

Checklist-audit + tests, not features:

- [x] Write credentials exist only in the cluster (K8s secret), never client.
      *(Asserted over the whole route table for the most privileged caller;
      structurally guaranteed besides — the API server holds an `Authorizer`,
      never a target store.)*
- [x] Retention is time-based `keep-within`, deep default (>= 90d), config
      floor prevents accidental `1d` foot-gun. *(R5: floor is 7d, enforced at
      the API boundary and again where prune reads the window.)*
- [x] Prune/maintenance runs as a **separate job type**, never inline in a
      backup run. *(R7, issue #24: `jobs.KindPrune`, scheduled on its own slow
      cadence, holding the same per-Space run lock. Same process and same
      credentials — that is what Tier 2 below changes.)*
- [x] Test: a "bad" snapshot (simulated ransomware content) does not evict
      good history within the retention window.
      *(`TestIntegration_RansomwareDoesNotEvictGoodHistory`, plus
      `TestIntegration_RansomwareCannotShortenRetention` for the attack on the
      window itself.)*

## Tier 2 — credential separation (main work of this phase)

- **Two Garage keys per deployment:**
  - `backup-writer`: `write` grant only (Garage: write implies overwrite/
    delete — API cannot prevent it; this is blast-radius reduction + **no
    read**: a stolen writer key cannot exfiltrate existing backups).
  - `prune-owner`: full grant; lives in a **separate K8s secret**, mounted
    only by the prune job (CronJob / separate deployment), not by `backupd`.
- Split `backupd` runtime so kopia maintenance/prune runs in the prune context
  on its own schedule; backup worker never loads the owner credential.
- Verify kopia works split-role: snapshot-create with writer key; maintenance
  with owner key. (kopia may need read for repo open — determine the true
  minimal grant set experimentally; document what Garage's coarse grants allow.
  If kopia requires read+write for snapshotting, accept and document: the
  guarantee then is "worker key cannot be used to prune via our tooling" +
  Tier 3.)
- Deploy manifests updated: two secrets, prune CronJob.

## Tier 3 — out-of-band FS snapshots (documentation deliverable)

No code; operator runbook (`/docs/operations.md` or deploy README):

- ZFS/Btrfs snapshot schedule for Garage `meta_dir` + `data_dir` on the buddy
  host; retention recommendation; restore-from-FS-snapshot procedure.
- Explicit statement of what this defends against (owner-key compromise,
  Garage bugs) and what it needs (snapshots not deletable with the same
  credentials that run Garage).

## Capability probe (future Garage Object Lock)

- `/pkg/s3target`: startup probe — attempt `GetBucketVersioning` /
  `GetObjectLockConfiguration`; record capabilities (currently: Garage returns
  stub/501).
- Feature flag `immutability.object_lock` (default auto): when the backend
  reports support, enable kopia's Object-Lock mode (`retention-mode` +
  maintenance lock extension) — path stays dormant on Garage today but is
  tested against MinIO in CI (MinIO supports Object Lock) so it isn't bit-rot.
- Watch upstream: Garage #166 (versioning), #1127 (Object Lock).

## Testing

- Tier 2 integration: run with writer key only → snapshot OK, prune attempt
  fails; prune job with owner key succeeds.
- Negative: writer key cannot GET repo objects (if no-read holds).
- Capability probe: Garage → object-lock disabled; MinIO (CI container) →
  enabled path exercised end-to-end.
- Retention-floor config test.

## Exit criteria

- [x] Two-key deployment in manifests + compose dev env.
- [x] Split-role kopia flow proven against Garage; minimal grants documented.
- [ ] ~~Object-Lock code path green against MinIO, dormant on Garage.~~
      Descoped to the capability probe; the enabled path needs a backend that
      implements Object Lock and there is none in scope (issue #33).
- [x] Tier 3 runbook written.
- [x] Threat→mitigation table in docs matches implementation.

---

## Outcome (implemented, issue #32)

The phase's central assumption did not survive contact with the backend, so what
shipped is narrower than the plan and says so in every place a reader could
otherwise believe otherwise.

- **Tier 2's premise was measured before it was built, and it was wrong in both
  directions.** `garage bucket allow` offers `--read`, `--write` and `--owner`.
  `--write` includes `DeleteObject`, so the plan's "backup-writer" key destroys
  as easily as it writes. `--owner` is bucket administration and grants **no
  object access at all**, so the plan's "prune-owner" key could not have deleted
  one snapshot — the roles were assigned the wrong way round. And kopia cannot
  open a repository without reading it, so the "no read" property the plan wanted
  from the worker key yields a worker that fails rather than a worker with less
  reach. Full table in `phase-0-findings.md`, pinned by `TestGarageGrantMatrix`,
  `TestGarageWriteImpliesDelete` and
  `TestIntegration_BackupNeedsReadAsWellAsWrite`.
- **What shipped is the separation without the bound.** A target carries one
  credential per role; backup and restore resolve the backup role, prune resolves
  the maintenance role, and neither run holds the other's key. On Garage both
  hold `read+write`, so this separates actors and not capabilities — it is
  documented as organisational everywhere it appears, and the README says in
  those words not to write it down as a blast-radius bound. The seam is what
  makes a backend with real IAM policies a configuration change later.
- **Deviation — no separate prune process, and no separate K8s Secret for it.**
  The plan wanted prune in its own CronJob with its own mounted credential. That
  is a second process taking per-Space leases in one state Space, which
  decision #16 locks as unsupported and which the lease — with no compare-and-set
  underneath it — cannot make safe. Owner's call: prune stays the job kind R7
  built, in-process, and only the credential it resolves changed. The honest
  consequence is recorded: both credentials live in one pod's memory, so the
  split buys little against a cluster compromise, and Tier 3 is what does.
- **Deviation — no feature flag for Object Lock.** The plan wanted
  `immutability.object_lock` defaulting to auto. A flag whose enabled branch no
  backend in the test environment can exercise is exactly the placeholder R9
  deleted for implying a feature exists. The **probe** shipped; the enabled path
  did not, and issue #33 records the gap on the Roadmap milestone rather than
  leaving it implied.
- **Finding — versioning is not a detectable capability.** Garage answers
  `GetBucketVersioning` successfully with an empty status, byte for byte what a
  real S3 bucket that never enabled versioning returns. Only `PutBucketVersioning`
  distinguishes them, and that is a write this service will not make against a
  backup bucket. Object Lock *is* detectable (`NotImplemented` vs. a
  configuration vs. `ObjectLockConfigurationNotFoundError`), so the probe reports
  Object Lock as a capability and versioning only as a status.
- **Tier 1's remaining two boxes became tests.** "A bad snapshot does not evict
  good history" is now an end-to-end run: back up, replace every file with random
  bytes and a ransom note, back up again, run retention, restore the pre-attack
  snapshot byte-identically. "Write credentials never reach the client" is
  asserted across the whole route table for the most privileged caller, against a
  target holding real sealed credentials. The second one also found that the API
  cannot leak a credential structurally — the server holds an `Authorizer`, never
  a target `Store` — which is a better guarantee than the test, and the test is
  what keeps it true when the admin API lands in Phase 8.
- **Addition — the retention-floor test earned its place by being checked.** Its
  first version set a one-day window, which keeps a seconds-old snapshot whether
  or not the floor exists: it passed while testing nothing. With the shortest
  window a record can hold, removing the floor makes the prune delete the
  pre-attack snapshot and leave the ransomware snapshot as the household's only
  copy. That was verified by temporarily removing the floor and watching the test
  fail.
- **The rotation runbook the plan assigned to this phase already existed.** R2
  built the commands and R8 wrote the runbook; re-writing it here would have been
  churn. What this phase added to the README is the immutability section the
  project had never had.
- **Defect found while documenting the deployment:** the manifest's commented-out
  bootstrap credentials were positioned under `envFrom:`, where `name`/`valueFrom`
  is not a valid entry — an operator uncommenting them as instructed got a
  rejected Deployment. Moved into `env:`, and `kubeconform` now runs over the
  uncommented form too.
