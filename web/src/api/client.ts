// The user-facing API client: one typed method per route.
//
// HTTP itself, the token handling and the failure mapping live in
// transport.ts, shared with the admin client (admin.ts). This file only knows
// routes and wire types.

import type { RotateRecoveryKeyRequest, SetupRequest } from '../crypto'
import { ApiTransport, type BackupApiOptions, type RequestOptions } from './transport'
import type {
  BackupConfig,
  BackupConfigPatch,
  BackupConfigRequest,
  BackupStatus,
  Job,
  KeyStatus,
  Notification,
  RecoveryEnvelope,
  RestoreAccepted,
  RunAccepted,
  Schedule,
  ScheduleRequest,
  Snapshot,
  Space,
  Target
} from './types'

export { DEFAULT_TIMEOUT_MS, type BackupApiOptions, type TokenSource } from './transport'

/**
 * BackupApi is the typed surface of the user-facing API.
 *
 * One method per route, named after what it does rather than after its verb, and
 * returning the DTO rather than a Response. The admin routes are not here:
 * they are AdminApi's (admin.ts), so they load with the admin view only.
 */
export class BackupApi {
  private readonly transport: ApiTransport

  constructor(options: BackupApiOptions) {
    this.transport = new ApiTransport(options)
  }

  // --- spaces and targets ------------------------------------------------

  /** listSpaces returns the Spaces the caller may back up. */
  async listSpaces(): Promise<Space[]> {
    const body = await this.request<{ spaces?: Space[] }>('/spaces')
    return body.spaces ?? []
  }

  /**
   * listTargets returns the targets granted to the caller, name and id only.
   *
   * An empty list is a legitimate state, not an error: it means the admin has
   * not granted this user a destination yet, and the UI has to say so rather
   * than offering a setup that cannot complete.
   */
  async listTargets(): Promise<Target[]> {
    const body = await this.request<{ targets?: Target[] }>('/targets')
    return body.targets ?? []
  }

  // --- key ceremony ------------------------------------------------------

  /** keyStatus reports whether a Space has keys. */
  keyStatus(spaceId: string): Promise<KeyStatus> {
    return this.request<KeyStatus>(this.space(spaceId, '/backup/keystatus'))
  }

  /**
   * setupKeys completes the key ceremony for a Space. Manager role.
   *
   * Answers 409 (`conflict`) for a Space that already has both wraps, and that
   * is a success state to report, not an error to retry: a second ceremony
   * would orphan every backup the Space has ever written (decisions.md #17).
   */
  setupKeys(spaceId: string, body: SetupRequest): Promise<KeyStatus> {
    return this.request<KeyStatus>(this.space(spaceId, '/backup/setup'), {
      method: 'POST',
      body
    })
  }

  /** recoveryEnvelope fetches the RK-wrapped Data Key. Any member. */
  recoveryEnvelope(spaceId: string): Promise<RecoveryEnvelope> {
    return this.request<RecoveryEnvelope>(this.space(spaceId, '/backup/recovery-envelope'))
  }

  /**
   * rotateRecoveryKey installs a new envelope around the same Data Key.
   *
   * The body carries no Data Key — the re-wrap happens in the browser
   * (decisions.md #18) — and this client could not add one if it wanted to.
   */
  rotateRecoveryKey(spaceId: string, body: RotateRecoveryKeyRequest): Promise<KeyStatus> {
    return this.request<KeyStatus>(this.space(spaceId, '/backup/recovery-key/rotate'), {
      method: 'POST',
      body
    })
  }

  // --- configuration and schedule ---------------------------------------

  /** backupConfig reads a Space's target binding and retention window. */
  backupConfig(spaceId: string): Promise<BackupConfig> {
    return this.request<BackupConfig>(this.space(spaceId, '/backup/config'))
  }

  /** setBackupConfig binds a Space to a granted target. Editor role. */
  setBackupConfig(spaceId: string, body: BackupConfigRequest): Promise<BackupConfig> {
    return this.request<BackupConfig>(this.space(spaceId, '/backup/config'), {
      method: 'PUT',
      body
    })
  }

  /**
   * patchBackupConfig changes only the fields given. Editor role.
   *
   * Prefer this to setBackupConfig for edits: PUT replaces the whole record,
   * so re-sending fields read a moment ago silently undoes a concurrent edit
   * to them. Answers 404 for a Space with no binding yet.
   */
  patchBackupConfig(spaceId: string, body: BackupConfigPatch): Promise<BackupConfig> {
    return this.request<BackupConfig>(this.space(spaceId, '/backup/config'), {
      method: 'PATCH',
      body
    })
  }

  /** schedule reads a Space's schedule. */
  schedule(spaceId: string): Promise<Schedule> {
    return this.request<Schedule>(this.space(spaceId, '/backup/schedule'))
  }

  /** setSchedule sets a Space's schedule. Editor role. */
  setSchedule(spaceId: string, body: ScheduleRequest): Promise<Schedule> {
    return this.request<Schedule>(this.space(spaceId, '/backup/schedule'), {
      method: 'PUT',
      body
    })
  }

  // --- status, runs, notifications --------------------------------------

  /** status is everything the status board needs, in one request. */
  status(spaceId: string): Promise<BackupStatus> {
    return this.request<BackupStatus>(this.space(spaceId, '/backup/status'))
  }

  /** runBackup starts a manual run. Editor role. Answers 202. */
  runBackup(spaceId: string): Promise<RunAccepted> {
    return this.request<RunAccepted>(this.space(spaceId, '/backup/run'), { method: 'POST' })
  }

  /** listRuns returns recent run history, newest first. */
  async listRuns(spaceId: string, limit?: number): Promise<Job[]> {
    const body = await this.request<{ runs?: Job[] }>(this.space(spaceId, '/backup/runs'), {
      query: { limit }
    })
    return body.runs ?? []
  }

  /**
   * run reads one run of a Space by its id. Answers 404 (`not_found`) for an
   * id that is not a run of *this* Space, however it came to be asked for.
   *
   * It is how a started restore is followed to its end: `status().current_job`
   * is whatever runs now, which may be a backup that began a moment later.
   */
  run(spaceId: string, jobId: string): Promise<Job> {
    return this.request<Job>(this.space(spaceId, `/backup/runs/${encodeURIComponent(jobId)}`))
  }

  /** listNotifications returns member-facing events for a Space. */
  async listNotifications(spaceId: string, limit?: number): Promise<Notification[]> {
    const body = await this.request<{ notifications?: Notification[] }>(
      this.space(spaceId, '/backup/notifications'),
      { query: { limit } }
    )
    return body.notifications ?? []
  }

  // --- restore (Path B) --------------------------------------------------

  /** listSnapshots returns the restorable points in time. */
  async listSnapshots(spaceId: string): Promise<Snapshot[]> {
    const body = await this.request<{ snapshots?: Snapshot[] }>(this.space(spaceId, '/snapshots'))
    return body.snapshots ?? []
  }

  /**
   * restore starts a full restore into `Restore/<timestamp>/`. Answers 202.
   *
   * The request carries a snapshot id and nothing else: the destination is not
   * the client's to choose, and never overwrites (decisions.md #3).
   */
  restore(spaceId: string, snapshotId: string): Promise<RestoreAccepted> {
    return this.request<RestoreAccepted>(this.space(spaceId, '/restore'), {
      method: 'POST',
      body: { snapshot_id: snapshotId }
    })
  }

  // --- plumbing ----------------------------------------------------------

  /** space builds a space-scoped path with the id encoded. */
  private space(spaceId: string, suffix: string): string {
    // Space ids contain "$" and "!" and are not URL-safe as-is.
    return `/spaces/${encodeURIComponent(spaceId)}${suffix}`
  }

  private request<T>(path: string, options?: RequestOptions): Promise<T> {
    return this.transport.request<T>(path, options)
  }
}
