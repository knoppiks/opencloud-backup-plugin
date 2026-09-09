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

- [ ] Write credentials exist only in the cluster (K8s secret), never client.
- [x] Retention is time-based `keep-within`, deep default (>= 90d), config
      floor prevents accidental `1d` foot-gun. *(R5: floor is 7d, enforced at
      the API boundary and again where prune reads the window.)*
- [x] Prune/maintenance runs as a **separate job type**, never inline in a
      backup run. *(R7, issue #24: `jobs.KindPrune`, scheduled on its own slow
      cadence, holding the same per-Space run lock. Same process and same
      credentials — that is what Tier 2 below changes.)*
- [ ] Test: a "bad" snapshot (simulated ransomware content) does not evict
      good history within the retention window.

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

- [ ] Two-key deployment in manifests + compose dev env.
- [ ] Split-role kopia flow proven against Garage; minimal grants documented.
- [ ] Object-Lock code path green against MinIO, dormant on Garage.
- [ ] Tier 3 runbook written.
- [ ] Threat→mitigation table in docs matches implementation.
