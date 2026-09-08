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

- [x] Unattended scheduled run completes end-to-end with **no user session**
      (SRW path proven — success metric relies on this).
      *`TestIntegration_ScheduledRunNeedsNoUserSession` (Garage).*
- [x] Restart-safety: kill/restart service, no duplicate or lost runs.
      *`TestIntegration_ScheduledRunSurvivesARestart`,
      `TestIntegration_CrashedRunIsRecovered`.*
- [x] Stale-backup notification fires in test.
      *`TestIntegration_StaleBackupIsReported`, plus `pkg/notify` unit tests.*
- [x] Status/history API consumed by tests (contract ready for Phase 8).
      *`pkg/api/schedule_test.go`. The history route stayed `.../backup/runs`
      (see decisions.md, Phase-6 amendments).*

## Outcome (what actually shipped)

Deviations from the plan above, all recorded in `decisions.md`:

- **Not SQLite.** State lives in OpenCloud over CS3 (`pkg/cs3state`, decision
  #16), behind the `pkg/state` document-store interface. Validated against
  OpenCloud 7.3.0. The single-instance target became a *correctness requirement*:
  the backend has no transactions, so cross-process mutual exclusion is not
  attempted.
- **Persistence reaches further than jobs.** Schedules, key envelopes and target
  records are durable too — otherwise a restarted service schedules runs it
  cannot perform.
- **Admin notifications narrowed** to operational events with no space or user
  identity, per locked decision #15.
- **Live progress not tracked**; counts are recorded when a run finishes.

### Hardened by R6 (September 2026 review)

The Phase-6 machinery worked and stopped working quietly. See the R6 amendments
in `decisions.md` for the reasoning; in short:

- A run releases its lock only once its outcome is durable, the outcome write is
  retried on a detached context, and a run that cannot record one keeps its lease
  so recovery closes it out. Recovery additionally sweeps for job records with no
  lock behind them.
- Scheduled runs are bounded in time, and data-gateway transfers must keep making
  progress.
- Due-ness asks the run lock instead of trusting a job record's state, and reads
  one history record instead of ten.
- Unreadable state documents are logged (by key) and reported to the operator (as
  a count).
- Notification history is pruned with the run history.
- The staleness sweep runs every 15 minutes; the worker's reva token is cached.
- Schedules default to the container's timezone.

## Risks / notes

- Lease/lock correctness on crash is the classic subtle bug — cover with tests,
  keep it boring (SQLite transaction + expiry timestamp).
- Prune/maintenance is deliberately **not** scheduled here with the same
  credentials — that separation is Phase 7 Tier 2.
