// Failures, in the shape the UI has to react to.
//
// The service answers every error with `{"error":{"code","message"}}` and
// nothing else — no internal detail, no upstream status, no credentials. This
// module turns that into something typed, and adds the one case the envelope
// cannot describe: the request never arrived.
//
// The `message` is written for a person and is safe to show, but it is written
// by the *server*, in English, and cannot be translated. Views should branch on
// `code` and supply their own wording; `message` is the fallback and the thing
// worth logging when a code is not one we know.

/**
 * ApiErrorCode is the complete set of codes `pkg/api` emits.
 *
 * Kept exhaustive on purpose: an unrecognised code means the server grew a
 * failure mode the UI has no wording for, which `isKnownApiErrorCode` makes
 * visible instead of letting it fall through a switch as "something went wrong".
 */
export type ApiErrorCode =
  | 'bad_request'
  | 'unauthorized'
  | 'forbidden'
  | 'not_found'
  | 'conflict'
  | 'not_configured'
  | 'run_in_progress'
  | 'target_unavailable'
  | 'target_in_use'
  | 'unavailable'
  | 'upstream_error'
  | 'internal_error'

const KNOWN_CODES: readonly string[] = [
  'bad_request',
  'unauthorized',
  'forbidden',
  'not_found',
  'conflict',
  'not_configured',
  'run_in_progress',
  'target_unavailable',
  'target_in_use',
  'unavailable',
  'upstream_error',
  'internal_error'
]

/** isKnownApiErrorCode reports whether the server sent a code this build knows. */
export function isKnownApiErrorCode(code: string): code is ApiErrorCode {
  return KNOWN_CODES.includes(code)
}

/**
 * TransportErrorCode covers the failures that happen instead of a response.
 *
 * These are distinct from anything the server can say, because the server said
 * nothing. "The backup service is not reachable" and "the backup service
 * refused this" need different wording and different advice.
 */
export type TransportErrorCode = 'offline' | 'timeout' | 'malformed_response'

/** ApiFailureCode is anything the client can fail with. */
export type ApiFailureCode = ApiErrorCode | TransportErrorCode | 'unknown'

/**
 * ApiError is every failure of an API call, including the ones with no response.
 *
 * `status` is absent for transport failures — that is the signal that nothing
 * was ever answered, rather than a 0 pretending to be a status code.
 */
export class ApiError extends Error {
  readonly code: ApiFailureCode
  readonly status?: number
  /** serverMessage is the untranslated text the service sent, if any. */
  readonly serverMessage?: string

  constructor(code: ApiFailureCode, message: string, status?: number, serverMessage?: string) {
    super(message)
    this.name = 'ApiError'
    this.code = code
    if (status !== undefined) {
      this.status = status
    }
    if (serverMessage !== undefined) {
      this.serverMessage = serverMessage
    }
  }

  /** isAuth reports a failure the user can only fix by signing in again. */
  get isAuth(): boolean {
    return this.code === 'unauthorized'
  }

  /** isPermission reports a failure caused by the caller's role on the Space. */
  get isPermission(): boolean {
    return this.code === 'forbidden'
  }

  /**
   * isRetryable reports a failure worth offering a retry for.
   *
   * Deliberately narrow: a 400 or a 403 will fail identically however many
   * times it is repeated, and a retry button next to one is a lie. `unavailable`
   * and `upstream_error` are the service or OpenCloud being temporarily unable,
   * which is exactly what retrying is for.
   */
  get isRetryable(): boolean {
    return (
      this.code === 'offline' ||
      this.code === 'timeout' ||
      this.code === 'unavailable' ||
      this.code === 'upstream_error'
    )
  }
}

/** isApiError narrows an unknown caught value. */
export function isApiError(err: unknown): err is ApiError {
  return err instanceof ApiError
}

/**
 * asApiError is what a flow keeps of any thrown value: the ApiError itself,
 * or a generic one. The generic one carries no text from the thrown value,
 * which may have come from anywhere.
 */
export function asApiError(err: unknown): ApiError {
  return isApiError(err) ? err : new ApiError('unknown', 'something went wrong')
}

/**
 * mayHaveLanded reports a write whose outcome is unknown: nothing answered, or
 * the server failed after it may already have acted. A flow must find out
 * what happened before it repeats such a write (8d.2 decision 5).
 */
export function mayHaveLanded(error: ApiError): boolean {
  return error.status === undefined || error.status >= 500
}

/** errorEnvelope is the service's error body. */
interface ErrorEnvelope {
  error?: {
    code?: unknown
    message?: unknown
  }
}

/**
 * apiErrorFromResponse builds an ApiError from a non-2xx response body.
 *
 * A body that is not the documented envelope is not treated as a mystery: the
 * HTTP status still carries meaning, and an ingress returning its own HTML 502
 * is a real and ordinary thing to hit. What is never done is showing the body,
 * which at that point is somebody else's error page.
 */
export function apiErrorFromResponse(status: number, body: unknown): ApiError {
  const envelope = body as ErrorEnvelope | null
  const rawCode = envelope?.error?.code
  const rawMessage = envelope?.error?.message

  const code = typeof rawCode === 'string' && rawCode !== '' ? rawCode : codeFromStatus(status)
  const serverMessage = typeof rawMessage === 'string' && rawMessage !== '' ? rawMessage : undefined

  const failureCode: ApiFailureCode = isKnownApiErrorCode(code) ? code : 'unknown'
  const message = serverMessage ?? `the backup service answered ${status}`

  return new ApiError(failureCode, message, status, serverMessage)
}

/** codeFromStatus is the fallback when no envelope was sent. */
function codeFromStatus(status: number): string {
  switch (status) {
    case 400:
      return 'bad_request'
    case 401:
      return 'unauthorized'
    case 403:
      return 'forbidden'
    case 404:
      return 'not_found'
    case 409:
      return 'conflict'
    case 502:
      return 'upstream_error'
    case 503:
      return 'unavailable'
    default:
      return status >= 500 ? 'internal_error' : 'unknown'
  }
}
