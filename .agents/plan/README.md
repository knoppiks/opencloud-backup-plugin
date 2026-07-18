# Planning Docs — opencloud-backup-plugin

Per-phase planning documents.

- **[decisions.md](decisions.md)** is the canonical record of locked decisions,
  the trust/key model, threat model, and success criteria. Read it first.
- The per-phase docs below break each phase into concrete deliverables, tasks,
  and exit criteria.

## Phase index & dependencies

| Phase | Doc | Depends on | Produces |
|---|---|---|---|
| 0 | [phase-0-spikes.md](phase-0-spikes.md) | — | Validated assumptions, test fixtures |
| 1 | [phase-1-scaffolding.md](phase-1-scaffolding.md) | 0 (partially parallel) | Module layout, CI, K8s manifests |
| 2 | [phase-2-auth-spaces.md](phase-2-auth-spaces.md) | 0 (CS3 spike), 1 | OIDC middleware, space listing API |
| 3 | [phase-3-keys.md](phase-3-keys.md) | 1 | Key service (DK/RK/SRW) |
| 4 | [phase-4-snapshot-pipeline.md](phase-4-snapshot-pipeline.md) | 0 (kopia spike), 2, 3 | Backup pipeline CS3→kopia→Garage |
| 5 | [phase-5-restore.md](phase-5-restore.md) | 4 | Take-Out CLI, decrypt CLI, Path B API |
| 6 | [phase-6-scheduling.md](phase-6-scheduling.md) | 4 | Scheduler, job store, notifications |
| 7 | [phase-7-immutability.md](phase-7-immutability.md) | 4, 6 | Tier 2/3 hardening, capability probe |
| 8 | [phase-8-web-ui.md](phase-8-web-ui.md) | 0 (extension spike), 2, 3, 5, 6 | OpenCloud Web extension |

## Rules that apply to every phase

- **Untested code is incomplete.** Every phase ships unit tests; integration
  tests run against the Garage fixture (Phase 0) and, where relevant, a test
  OpenCloud instance.
- **A backup path is not done until its restore path is tested** (decisions.md).
- **No hand-rolled crypto or dedup** — kopia does that (decisions.md #5).
- **Keys are never logged** and never appear in the admin UI (decisions.md).
- Retention is always **time-based (`keep-within`)**, never count-based.
- Design for testability: dependency injection, interfaces at the boundaries
  (CS3 client, S3 target, key store, clock).

## Glossary

- **DK** — per-Space Data Key (encrypts snapshot data; = kopia repo password).
- **RK** — Recovery Key (user-held, wraps DK; stored only in user's password
  manager).
- **SRW** — Server Runtime Wrap (server-held wrapped DK for unattended runs).
- **Path A** — admin Take-Out: ciphertext out of S3, user decrypts offline.
- **Path B** — user-triggered full restore into `Restore/<ts>/` via GUI.
- **Space** — oCIS storage space; unit of backup, keying, and scheduling.
- **Garage** — S3-compatible target store (no Object Lock/versioning today).
