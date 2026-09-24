// The one place this extension speaks HTTP.
//
// Everything is injected — the base URL, the token source, `fetch` and the
// timeout — so the client is testable without a DOM, without OpenCloud and
// without a server, and so no module below it has to know those things exist.
// `src/crypto` in particular must never import this file (enforced in
// eslint.config.ts): it shapes request bodies and this sends them.
//
// Two properties are load-bearing and both are tested:
//
//   - **The token is attached per request, never cached here.** OpenCloud's auth
//     store renews it; a copy taken at construction would expire and every call
//     would 401 until the page reloaded.
//   - **No request body is ever logged or attached to an error.** Setup carries
//     the Data Key and the recovery envelope, and an error that quoted the body
//     it failed on would put key material into a console and a bug report.

import type { RotateRecoveryKeyRequest, SetupRequest } from '../crypto'
import { ApiError, apiErrorFromResponse } from './errors'
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

/** DEFAULT_TIMEOUT_MS bounds a request. Snapshot listings are the slow ones. */
export const DEFAULT_TIMEOUT_MS = 30_000

/**
 * TokenSource yields the caller's current OIDC access token.
 *
 * Returning undefined means "not signed in yet", which becomes an `unauthorized`
 * ApiError rather than an unauthenticated request the server would reject
 * anyway — the difference being that this way the UI can say why.
 */
export type TokenSource = () => string | undefined | Promise<string | undefined>

/** BackupApiOptions configures the client. */
export interface BackupApiOptions {
  /** baseUrl is an absolute, same-origin base with no trailing slash. */
  baseUrl: string
  getToken: TokenSource
  /** fetchImpl defaults to the global fetch; tests pass their own. */
  fetchImpl?: typeof fetch
  timeoutMs?: number
}

interface RequestOptions {
  method?: string
  body?: unknown
  query?: Record<string, string | number | undefined>
  /** expectNoContent is set for the routes that answer 204. */
  expectNoContent?: boolean
}

/**
 * BackupApi is the typed surface of the user-facing API.
 *
 * One method per route, named after what it does rather than after its verb, and
 * returning the DTO rather than a Response. The admin routes are not here; they
 * arrive with the admin view in 8e.
 */
export class BackupApi {
  private readonly baseUrl: string
  private readonly getToken: TokenSource
  private readonly fetchImpl: typeof fetch
  private readonly timeoutMs: number

  constructor(options: BackupApiOptions) {
    this.baseUrl = options.baseUrl.endsWith('/') ? options.baseUrl.slice(0, -1) : options.baseUrl
    this.getToken = options.getToken
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch.bind(globalThis)
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS
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

  private async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
    const token = await this.resolveToken()
    const url = this.url(path, options.query)

    const headers: Record<string, string> = {
      Accept: 'application/json',
      Authorization: `Bearer ${token}`
    }
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json'
    }

    // Built conditionally rather than with an undefined `body`:
    // exactOptionalPropertyTypes draws a distinction between "absent" and
    // "present and undefined", and fetch's own types only accept the former.
    const init: RequestInit = { method: options.method ?? 'GET', headers }
    if (options.body !== undefined) {
      init.body = JSON.stringify(options.body)
    }

    const response = await this.send(url, init)

    if (!response.ok) {
      throw apiErrorFromResponse(response.status, await this.readJson(response))
    }
    if (options.expectNoContent || response.status === 204) {
      return undefined as T
    }

    const body = await this.readJson(response)
    if (body === undefined) {
      throw new ApiError(
        'malformed_response',
        'the backup service sent a response that was not JSON',
        response.status
      )
    }
    return body as T
  }

  /** resolveToken fails closed, and says which of the two problems it is. */
  private async resolveToken(): Promise<string> {
    const token = await this.getToken()
    if (token === undefined || token === '') {
      throw new ApiError('unauthorized', 'not signed in to OpenCloud')
    }
    return token
  }

  private url(path: string, query?: Record<string, string | number | undefined>): string {
    const search = new URLSearchParams()
    for (const [key, value] of Object.entries(query ?? {})) {
      if (value !== undefined) {
        search.set(key, String(value))
      }
    }
    const suffix = search.size > 0 ? `?${search.toString()}` : ''
    return `${this.baseUrl}${path}${suffix}`
  }

  /**
   * send performs the request and turns a failure to get *any* response into a
   * typed error.
   *
   * The thrown value from fetch is deliberately discarded rather than wrapped:
   * for a same-origin request it carries nothing actionable, and on some engines
   * it stringifies the URL, which would put a space id into a message.
   */
  private async send(url: string, init: RequestInit): Promise<Response> {
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), this.timeoutMs)
    try {
      return await this.fetchImpl(url, { ...init, signal: controller.signal })
    } catch {
      if (controller.signal.aborted) {
        throw new ApiError('timeout', 'the backup service did not answer in time')
      }
      throw new ApiError('offline', 'the backup service could not be reached')
    } finally {
      clearTimeout(timer)
    }
  }

  /** readJson returns the parsed body, or undefined when there is not one. */
  private async readJson(response: Response): Promise<unknown> {
    const text = await response.text().catch(() => '')
    if (text === '') {
      return undefined
    }
    try {
      return JSON.parse(text)
    } catch {
      // Not JSON: an ingress error page, or a proxy that answered instead of
      // the service. The content is not shown — it is not ours.
      return undefined
    }
  }
}
