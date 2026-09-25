// "Check my Recovery Key" and the envelope download, as a state machine with no
// Vue in it (8d decision 7; 8d.4 decisions 4, 6 and 7).
//
// Any member may use it: the recovery envelope is ciphertext every member may
// read (decisions.md #7). The check runs entirely in this browser. The key is
// decoded and tried against the envelope here, and the answer goes to the
// screen and nowhere else.
//
// Key hygiene, which the tests hold this module to:
//
//   - The Recovery Key being checked is an argument to check() and is never
//     stored: not in `state`, not in a field. The Data Key it opens is zeroized
//     by recoveryKeyOpens before the answer comes back.
//   - A result is about the key only when the envelope is known to be good.
//     An envelope that cannot be fetched or parsed is "could not check", never
//     "wrong key": that would tell someone holding the right key to stop
//     trusting it.

import { ApiError, asApiError } from '../api'
import type { BackupStatus, RecoveryEnvelope, Space } from '../api'
import { base64Decode, decodeRecoveryKey, inspect, WrapKind, zeroize } from '../crypto'
import { canManageKeys } from '../status/roles'

/** ENVELOPE_FILE_NAME matches the Take-Out's, so the file drops into one as is. */
export const ENVELOPE_FILE_NAME = 'recovery.ocbke'

/** CheckApi is the slice of the client the check page uses. */
export interface CheckApi {
  listSpaces(): Promise<Space[]>
  status(spaceId: string): Promise<BackupStatus>
  recoveryEnvelope(spaceId: string): Promise<RecoveryEnvelope>
}

/** CheckDeps is everything the machine does not decide for itself. */
export interface CheckDeps {
  api: CheckApi
  /** keyOpens is recoveryKeyOpens in production; Argon2id, so tests fake it. */
  keyOpens: (envelope: Uint8Array, recoveryKey: string) => Promise<boolean>
  /** saveFile hands bytes to the browser as a download (saveBytes). */
  saveFile: (bytes: Uint8Array, fileName: string) => void
  /** yieldToRender paints "checking…" before Argon2id holds the thread. */
  yieldToRender: () => Promise<void>
  onChange: (state: CheckState) => void
}

/** CheckResult is what the page says about the key that was tried. */
export type CheckResult =
  /** It opens this Space's backups. */
  | { kind: 'opens' }
  /** Decode refused it: a typo, or not a Recovery Key at all. */
  | { kind: 'malformed' }
  /** A well-formed key that does not open this Space's envelope. */
  | { kind: 'wrong' }
  /** No answer about the key: the envelope could not be fetched or read. */
  | { kind: 'cannot_tell'; error: ApiError }

/** DownloadState is the download button's state. */
export type DownloadState =
  { kind: 'idle' } | { kind: 'working' } | { kind: 'done' } | { kind: 'failed'; error: ApiError }

export type CheckState =
  | { step: 'loading' }
  | { step: 'load_failed'; error: ApiError }
  /** The Space has no keys yet, so there is nothing to check or download. */
  | { step: 'not_set_up' }
  | {
      step: 'ready'
      /** mayReplace: manager and up see the link to the replacement. */
      mayReplace: boolean
      checking: boolean
      result: CheckResult | undefined
      download: DownloadState
    }
  /** After dispose. Holds nothing. */
  | { step: 'closed' }

/** RecoveryKeyCheck drives one check page. Create per mount; dispose() on unmount. */
export class RecoveryKeyCheck {
  private current: CheckState = { step: 'loading' }
  private name: string | undefined

  constructor(
    private readonly spaceId: string,
    private readonly deps: CheckDeps
  ) {}

  get state(): CheckState {
    return this.current
  }

  /** spaceName is the Space's display name once start() has read it. */
  get spaceName(): string | undefined {
    return this.name
  }

  /** start reads the caller's role and whether the Space has keys. */
  async start(): Promise<void> {
    this.set({ step: 'loading' })
    let space: Space
    let status: BackupStatus
    try {
      const [spaces, loaded] = await Promise.all([
        this.deps.api.listSpaces(),
        this.deps.api.status(this.spaceId)
      ])
      const found = spaces.find((s) => s.id === this.spaceId)
      if (!found) {
        throw new ApiError('not_found', 'space not in the caller’s list')
      }
      space = found
      status = loaded
    } catch (err: unknown) {
      this.set({ step: 'load_failed', error: asApiError(err) })
      return
    }
    this.name = space.name
    if (!status.keys_configured) {
      this.set({ step: 'not_set_up' })
      return
    }
    this.set({
      step: 'ready',
      mayReplace: canManageKeys(space.role),
      checking: false,
      result: undefined,
      download: { kind: 'idle' }
    })
  }

  /** clearResult forgets the last answer, e.g. when the input is edited. */
  clearResult(): void {
    const s = this.current
    if (s.step === 'ready' && !s.checking && s.result !== undefined) {
      this.set({ ...s, result: undefined })
    }
  }

  /**
   * check tries a Recovery Key against the Space's current envelope, locally.
   * The key is used and dropped; it is not kept anywhere.
   */
  async check(recoveryKey: string): Promise<void> {
    const s = this.current
    if (s.step !== 'ready' || s.checking) {
      return
    }
    // Decode is cheap and needs no envelope: a typo is answered before any
    // request and before Argon2id.
    if (!isWellFormed(recoveryKey)) {
      this.set({ ...s, result: { kind: 'malformed' } })
      return
    }
    this.set({ ...s, checking: true, result: undefined })
    const result = await this.tryKey(recoveryKey)
    const now = this.current
    if (now.step === 'ready') {
      this.set({ ...now, checking: false, result })
    }
  }

  /** download saves the Space's current envelope as recovery.ocbke. */
  async download(): Promise<void> {
    const s = this.current
    if (s.step !== 'ready' || s.download.kind === 'working') {
      return
    }
    this.set({ ...s, download: { kind: 'working' } })
    let next: DownloadState
    try {
      const envelope = await this.fetchEnvelope()
      this.deps.saveFile(envelope, ENVELOPE_FILE_NAME)
      next = { kind: 'done' }
    } catch (err: unknown) {
      next = { kind: 'failed', error: asApiError(err) }
    }
    const now = this.current
    if (now.step === 'ready') {
      this.set({ ...now, download: next })
    }
  }

  dispose(): void {
    this.set({ step: 'closed' })
  }

  // --- internals ----------------------------------------------------------

  private async tryKey(recoveryKey: string): Promise<CheckResult> {
    let envelope: Uint8Array
    try {
      envelope = await this.fetchEnvelope()
    } catch (err: unknown) {
      return { kind: 'cannot_tell', error: asApiError(err) }
    }
    await this.deps.yieldToRender()
    try {
      return (await this.deps.keyOpens(envelope, recoveryKey))
        ? { kind: 'opens' }
        : { kind: 'wrong' }
    } catch {
      return { kind: 'cannot_tell', error: unreadableEnvelope() }
    }
  }

  /**
   * fetchEnvelope reads the current envelope and refuses anything that is not
   * a Recovery Key envelope. A download of garbage would be found out on the
   * worst possible day, so it is refused on this one.
   */
  private async fetchEnvelope(): Promise<Uint8Array> {
    const stored = await this.deps.api.recoveryEnvelope(this.spaceId)
    let bytes: Uint8Array
    try {
      bytes = base64Decode(stored.envelope)
      if (inspect(bytes).kind !== WrapKind.RK) {
        throw unreadableEnvelope()
      }
    } catch {
      throw unreadableEnvelope()
    }
    return bytes
  }

  private set(state: CheckState): void {
    if (this.current.step === 'closed' && state.step !== 'closed') {
      return
    }
    this.current = state
    this.deps.onChange(state)
  }
}

/** isWellFormed: decode accepts it. The decoded secret is wiped at once. */
function isWellFormed(recoveryKey: string): boolean {
  try {
    zeroize(decodeRecoveryKey(recoveryKey))
    return true
  } catch {
    return false
  }
}

function unreadableEnvelope(): ApiError {
  return new ApiError('malformed_response', 'the stored recovery envelope is not readable')
}
