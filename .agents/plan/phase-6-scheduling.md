# Phase 6 — Scheduling & Status

**Goal:** backups become "set and forget": per-Space schedules, unattended runs
via SRW, durable job state, failure notifications.

**Depends on:** Phase 4 (runnable pipeline), Phase 3 (SRW unwrap).

## Deliverables

1. **Scheduler** (`/pkg/scheduler`)
   - Per-Space cron-like schedule (v1 presets: daily / weekly + time window;
     no free-form cron in the UI, keep it family-simple; API accepts cron
     syntax internally).
   - Injected `Clock`; tick loop → due spaces → enqueue runs.
   - Jitter/stagger so all spaces don't fire simultaneously.
   - Concurrency limit (config, default e.g. 2 parallel space runs).
   - Missed-run policy: on service restart, run if last success older than
     schedule interval (catch-up once, don't storm).

2. **Job/state store** (`/pkg/jobs`)
   - Persistent: job id, space, type (backup/restore/prune), state
     (queued/running/done/failed), started/finished, bytes/files processed,
     error summary, snapshot id produced.
   - Per-space lock (no two concurrent runs on one space — Phase 4 requirement
     formalized here).
   - Backend: SQLite (single-instance deployment is the target; wrap behind
     `jobs.Store` so Postgres remains possible). Worker stays stateless per
     run — state lives here only.
   - Retention of job history (e.g. keep 1 year, prune older).

3. **Status API** (feeds the Phase 8 status board)
   - `GET /api/v1/spaces/{id}/backup/status` — last run, next run, running?,
     progress (bytes/files), last error.
   - `GET /api/v1/spaces/{id}/backup/jobs?limit=` — history.
   - `PUT /api/v1/spaces/{id}/backup/schedule` — set/change schedule
     (space member only).

4. **Failure notifications**
   - v1: oCIS notification via its notification service if the spike shows a
     usable API; fallback: email through admin-configured SMTP.
   - Notify space owner + admin on: run failed, run skipped N times,
     no successful backup for > threshold ("backup is stale" — the important
     one for silent failures).

## Testing

- Scheduler unit tests with fake clock: due calculation, jitter bounds,
  catch-up-once semantics, concurrency cap.
- Job store: crash-sim (kill mid-run → state recoverable, lock released via
  lease/timeout, next run OK).
- Integration: schedule `* * * * *`-equivalent against compose stack → two
  automatic runs happen, status endpoint reflects them.
- Notification: failed run (Garage stopped) produces a notification record.

## Exit criteria

- [ ] Unattended scheduled run completes end-to-end with **no user session**
      (SRW path proven — success metric relies on this).
- [ ] Restart-safety: kill/restart service, no duplicate or lost runs.
- [ ] Stale-backup notification fires in test.
- [ ] Status/history API consumed by tests (contract ready for Phase 8).

## Risks / notes

- Lease/lock correctness on crash is the classic subtle bug — cover with tests,
  keep it boring (SQLite transaction + expiry timestamp).
- Prune/maintenance is deliberately **not** scheduled here with the same
  credentials — that separation is Phase 7 Tier 2.
