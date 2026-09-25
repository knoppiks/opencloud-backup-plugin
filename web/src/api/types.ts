// Wire types for the user-facing backup API.
//
// These mirror the Go DTOs in `pkg/api` field for field, including the snake_case
// names, because a rename on either side should be a compile error here rather
// than an `undefined` in a template. Where the Go struct uses `omitempty`, the
// field is optional here.
//
// Request bodies for the key ceremony are deliberately absent: `src/crypto`
// owns them (`SetupRequest`, `RotateRecoveryKeyRequest`), because the module
// that produces the envelope is the module that should decide what is sent.
//
// Nothing in this file describes the admin API. That is sub-phase 8e.

/**
 * SpaceRole is the caller's own authority on a Space, in the server's words.
 *
 * It decides what the UI *offers*, never what is allowed: every route enforces
 * its own minimum server-side. viewer reads and restores, editor configures and
 * runs, manager (and owner) set up and replace keys.
 */
export type SpaceRole = 'viewer' | 'editor' | 'manager' | 'owner'

/** Space is one OpenCloud Space the caller may back up. `GET /spaces`. */
export interface Space {
  id: string
  name: string
  /** type is the CS3 space type, e.g. "personal" or "project". */
  type: string
  /** role is the caller's own role; an unknown word is treated as viewer. */
  role: SpaceRole | string
}

/**
 * Target is a backup destination, as much of one as a user is ever told.
 *
 * Name and id only: endpoints, buckets and credentials are admin-managed and
 * never reach a user (decisions.md #12/#14). `GET /targets` returns only the
 * targets granted to the caller, decided server-side.
 */
export interface Target {
  id: string
  name: string
}

/** KeyStatus reports whether a Space has been through the key ceremony. */
export interface KeyStatus {
  space_id: string
  /** configured is true only when both wraps exist (decisions.md #17). */
  configured: boolean
  has_recovery_wrap: boolean
  has_server_wrap: boolean
  recovery_wrap_version?: number
  server_wrap_version?: number
  created_at?: string
  updated_at?: string
}

/**
 * RecoveryEnvelope is the RK-wrapped Data Key plus the public KDF parameters
 * needed to derive the key-encryption key from a Recovery Key.
 *
 * Ciphertext only. The server cannot open this and neither can anything else
 * without the user's Recovery Key.
 */
export interface RecoveryEnvelope {
  space_id: string
  /** envelope is the wrapped Data Key, standard base64. */
  envelope: string
  version: number
  kdf: string
  argon_time?: number
  argon_memory_kib?: number
  argon_lanes?: number
}

/** BackupConfigRequest binds a Space to a granted target. `PUT /backup/config`. */
export interface BackupConfigRequest {
  /** target_id must be granted to the caller; the server re-checks. */
  target_id: string
  /** retention_days is the time-based keep-within window, floored at 7 (#22). */
  retention_days: number
  /** enabled controls scheduled runs only; a manual run works regardless. */
  enabled: boolean
}

/**
 * BackupConfigPatch changes some fields of an existing binding.
 * `PATCH /backup/config`. Absent fields keep their stored value; the server
 * refuses fields it does not know, so a typo is an error rather than a no-op.
 */
export interface BackupConfigPatch {
  target_id?: string
  /** 0 restores the default; anything else is floored at 7 (#22). */
  retention_days?: number
  enabled?: boolean
}

/** BackupConfig is a Space's stored binding. */
export interface BackupConfig {
  space_id: string
  target_id: string
  retention_days: number
  /** schedule is the cron expression scheduled runs follow. */
  schedule: string
  enabled: boolean
  created_at?: string
  updated_at?: string
}

/** PresetKind is the family-legible shape of a schedule. */
export type PresetKind = 'daily' | 'weekly' | 'custom'

/** SchedulePreset is a schedule expressed the way the UI offers it. */
export interface SchedulePreset {
  kind: PresetKind
  /** hour and minute are local time, in the service's configured zone. */
  hour: number
  minute: number
  /** weekday is 0 (Sunday) to 6, and applies to the weekly preset only. */
  weekday?: number
}

/** ScheduleRequest sets a Space's schedule. `PUT /backup/schedule`. */
export interface ScheduleRequest {
  enabled: boolean
  /** preset is the ordinary path; cron covers what no preset expresses. */
  preset?: SchedulePreset
  cron?: string
}

/** Schedule is a Space's stored schedule. */
export interface Schedule {
  space_id: string
  enabled: boolean
  cron: string
  /** preset reads "custom" when the cron expression is not one a preset emits. */
  preset: SchedulePreset
  /** timezone is the IANA zone preset times are in; absent when unnamed. */
  timezone?: string
}

/** JobState is where a run got to. */
export type JobState = 'pending' | 'running' | 'succeeded' | 'failed'

/** JobKind distinguishes the three things a run can be. */
export type JobKind = 'backup' | 'restore' | 'prune'

/**
 * Job is one recorded run.
 *
 * Counts are what the run *processed*, written when it finished: live progress
 * is deliberately not tracked (Phase 6 amendment), so a running job carries a
 * start time and not a percentage.
 */
export interface Job {
  id: string
  kind: JobKind | string
  state: JobState | string
  /** trigger says whether a person or the schedule started this run. */
  trigger?: string
  created_at: string
  updated_at: string
  finished_at?: string
  file_count?: number
  total_bytes?: number
  snapshot_id?: string
  /** Prune runs only: how many snapshots aged out, and how many remain. */
  snapshots_deleted?: number
  snapshots_kept?: number
  /** error is the sanitized message the runner recorded, never a path. */
  error?: string
  /**
   * Restore runs only: the space-relative folder the snapshot is written into,
   * set from the moment the run starts, so a failed run's partial result can
   * be found too.
   */
  restore_folder?: string
}

/** BackupStatus is everything the status board needs in one request. */
export interface BackupStatus {
  space_id: string
  /** configured reports whether the Space is bound to a target at all. */
  configured: boolean
  /** keys_configured reports whether the key ceremony is complete. */
  keys_configured: boolean
  /**
   * stale is the same verdict the backup_stale notification is sent on, so
   * the board and the notification never disagree. stale_since is set only
   * when stale is.
   */
  stale: boolean
  stale_since?: string
  enabled: boolean
  cron?: string
  preset?: SchedulePreset
  /** timezone is the IANA zone preset times are in; absent when unnamed. */
  timezone?: string
  running: boolean
  current_job?: Job
  last_run?: Job
  last_successful_run?: Job
  next_run?: string
  last_error?: string
  retention_days?: number
}

/** Notification is a member-facing event for one Space. */
export interface Notification {
  id: string
  kind: string
  message: string
  created_at: string
}

/** Snapshot is one restorable point in time. */
export interface Snapshot {
  id: string
  taken_at: string
  file_count: number
  total_bytes: number
}

/** RunAccepted is the answer to `POST /backup/run` (202). */
export interface RunAccepted {
  job_id: string
  space_id: string
  state: string
}

/** RestoreAccepted is the answer to `POST /restore` (202). */
export interface RestoreAccepted {
  job_id: string
  space_id: string
  snapshot_id: string
  state: string
}
