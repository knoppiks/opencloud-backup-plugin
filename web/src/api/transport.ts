// The one place this extension speaks HTTP.
//
// Everything is injected — the base URL, the token source, `fetch` and the
// timeout — so the transport is testable without a DOM, without OpenCloud and
// without a server, and so no module below it has to know those things exist.
// `src/crypto` in particular must never import this file (enforced in
// eslint.config.ts): it shapes request bodies and this sends them.
//
// Two clients sit on top: BackupApi (client.ts), for everyone, and AdminApi
// (admin.ts), for the admin view only. They share this file rather than a base
// class so the admin routes live in the admin chunk and nowhere else.
//
// Two properties are load-bearing and both are tested:
//
//   - **The token is attached per request, never cached here.** OpenCloud's auth
//     store renews it; a copy taken at construction would expire and every call
//     would 401 until the page reloaded.
//   - **No request body is ever logged or attached to an error.** Setup carries
//     the Data Key and the recovery envelope, target administration carries S3
//     secrets, and an error that quoted the body it failed on would put either
//     into a console and a bug report.

import { ApiError, apiErrorFromResponse } from './errors'

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

/** BackupApiOptions configures a client. */
export interface BackupApiOptions {
  /** baseUrl is an absolute, same-origin base with no trailing slash. */
  baseUrl: string
  getToken: TokenSource
  /** fetchImpl defaults to the global fetch; tests pass their own. */
  fetchImpl?: typeof fetch
  timeoutMs?: number
}

/** RequestOptions describes one request beyond its path. */
export interface RequestOptions {
  method?: string
  body?: unknown
  query?: Record<string, string | number | undefined>
  /** expectNoContent is set for the routes that answer 204. */
  expectNoContent?: boolean
}

/** ApiTransport sends one JSON request and turns every failure into an ApiError. */
export class ApiTransport {
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

  async request<T>(path: string, options: RequestOptions = {}): Promise<T> {
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
