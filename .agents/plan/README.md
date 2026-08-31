# Planning Docs — opencloud-backup-plugin

Per-phase planning documents.

- **[decisions.md](decisions.md)** is the canonical record of locked decisions,
  the trust/key model, threat model, and success criteria. Read it first.
- **[key-envelope-format.md](key-envelope-format.md)** specifies the key-envelope
  and Recovery-Key wire formats. It is a **long-term compatibility promise**: the
  standalone decrypt CLI must parse every version forever.
- The per-phase docs below break each phase into concrete deliverables, tasks,
  and exit criteria.

## Phase index & dependencies

| Phase | Doc | Depends on | Produces |
|---|---|---|---|
| 0 | [phase-0-spikes.md](phase-0-spikes.md) | — | Validated assumptions, test fixtures |
| 0 | [phase-0-admin-role-spike.md](phase-0-admin-role-spike.md) | 0 fixture (Spikes 3 & 4) | Admin-role detection (server + client), pinned |
| 1 | [phase-1-scaffolding.md](phase-1-scaffolding.md) | 0 (partially parallel) | Module layout, CI, K8s manifests |
| 2 | [phase-2-auth-spaces.md](phase-2-auth-spaces.md) | 0 (CS3 spike, admin-role spike), 1 | OIDC + admin middleware, space & granted-target APIs |
| 3 | [phase-3-keys.md](phase-3-keys.md) | 1 | Key service (DK/RK/SRW/TW) |
| 4 | [phase-4-snapshot-pipeline.md](phase-4-snapshot-pipeline.md) | 0 (kopia spike), 2, 3 | Backup pipeline CS3→kopia→Garage |
| 5 | [phase-5-restore.md](phase-5-restore.md) | 4 | Take-Out CLI, decrypt CLI, Path B API |
| 6 | [phase-6-scheduling.md](phase-6-scheduling.md) | 4 | Scheduler, job store, notifications |
| 7 | [phase-7-immutability.md](phase-7-immutability.md) | 4, 6 | Tier 2/3 hardening, capability probe |
| 8 | [phase-8-web-ui.md](phase-8-web-ui.md) | 0 (extension spike, admin-role spike), 2, 3, 5, 6 | OpenCloud Web extension (incl. admin target mgmt) |

**Target management (decisions.md #12–#15):** backup targets are managed in-app
by an OpenCloud admin, who grants each target to all or specific users. The
target store, access grants, and at-rest credential encryption (TW key) live in
`pkg/targets`; admin identity reuses OpenCloud's admin role (validated by the
admin-role spike). See the per-phase docs for where each piece is implemented.

**Which target a Space uses** is the user's choice, not the admin's, so the
binding lives separately in `pkg/spacecfg` (target id + time-based retention
window). It is only stored after a server-side grant check.

## Rules that apply to every phase

- **Untested code is incomplete.** Every phase ships unit tests; integration
  tests run against the Garage fixture (Phase 0) and, where relevant, a test
  OpenCloud instance.
- **A backup path is not done until its restore path is tested** (decisions.md).
- **No hand-rolled crypto or dedup** — kopia does that (decisions.md #5).
- **Keys are never logged** and never appear in the admin UI (decisions.md).
  **S3 target credentials are key-class:** write-only in the UI/API, stored only
  as TW-wrapped ciphertext, decrypted only in worker memory (decisions.md #14).
- Retention is always **time-based (`keep-within`)**, never count-based.
- Design for testability: dependency injection, interfaces at the boundaries
  (CS3 client, S3 target, key store, clock).

## Glossary

- **DK** — per-Space Data Key (encrypts snapshot data; = kopia repo password).
- **RK** — Recovery Key (user-held, wraps DK; stored only in user's password
  manager).
- **SRW** — Server Runtime Wrap (server-held wrapped DK for unattended runs).
- **TW** — Target Wrap (cluster/KMS key wrapping S3 target credentials at rest;
  decisions.md #14).
- **Target** — an admin-managed S3 destination ("Buddy-S3"), granted to users.
- **Grant** — an access grant binding a target to all users or specific
  users/spaces.
- **Path A** — admin Take-Out: ciphertext out of S3, user decrypts offline.
- **Path B** — user-triggered full restore into `Restore/<ts>/` via GUI.
- **Space** — oCIS storage space; unit of backup, keying, and scheduling.
- **Garage** — S3-compatible target store (no Object Lock/versioning today).
- **Repo layout** — one kopia repository per Space, at
  `s3://<bucket>/<prefix>spaces/<space-id>/`.
