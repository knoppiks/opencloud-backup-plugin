// Replacing a Recovery Key, as a state machine with no Vue in it (flow item 6;
// decisions.md #18; 8d.4 decisions 1, 2, 5 and 8).
//
//   current key -> GET envelope -> unwrap, re-wrap, self-verify (in the browser)
//     -> show the new key once -> gate -> POST rotate (envelope + digest only)
//
// The Data Key never reaches this module: performRecoveryKeyRotation recovers
// it, re-wraps it and zeroizes it before returning. What is held here is two
// Recovery Keys, and only for as long as each has a purpose:
//
//   - the new key, from the ceremony until the POST settles (it is shown and
//     gated in between, so it is in `state` for those steps);
//   - the old key, privately, from the ceremony until the POST settles. After
//     an ambiguous failure both stay, privately, until re-reading the envelope
//     has decided which of them the Space now uses (decision 1).
//
// Neither is ever passed to the API. The request carries the new envelope and
// the digest of the old one, and nothing else.

import { ApiError, asApiError, isApiError, mayHaveLanded } from '../api'
import type { BackupStatus, KeyStatus, RecoveryEnvelope, RunAccepted, Space } from '../api'
import {
  base64Decode,
  CeremonyError,
  EnvelopeError,
  RecoveryKeyError,
  UnwrapError,
  type RotateRecoveryKeyRequest,
  type RotationCeremony,
  type RotationOptions
} from '../crypto'
import { canManageKeys, canOperate } from '../status/roles'
import { gateMatches, pickGateGroups } from './gate'

/** RotationApi is the slice of the client the replacement uses. */
export interface RotationApi {
  listSpaces(): Promise<Space[]>
  status(spaceId: string): Promise<BackupStatus>
  recoveryEnvelope(spaceId: string): Promise<RecoveryEnvelope>
  rotateRecoveryKey(spaceId: string, body: RotateRecoveryKeyRequest): Promise<KeyStatus>
  runBackup(spaceId: string): Promise<RunAccepted>
}

/** RotationDeps is everything the machine does not decide for itself. */
export interface RotationDeps {
  api: RotationApi
  /** rotate is performRecoveryKeyRotation in production: Argon2id, three times. */
  rotate: (options: RotationOptions) => Promise<RotationCeremony>
  /** keyOpens is recoveryKeyOpens in production. */
  keyOpens: (envelope: Uint8Array, recoveryKey: string) => Promise<boolean>
  randomInt: (bound: number) => number
  yieldToRender: () => Promise<void>
  onChange: (state: RotationState) => void
}

/** EntryFailure is why the current-key form is shown again. */
export type EntryFailure =
  /** Decode refused the current key: a typo. */
  | { kind: 'malformed' }
  /** A well-formed key that does not open this Space's envelope. */
  | { kind: 'wrong' }
  /** The ceremony refused its own output. Nothing was sent. */
  | { kind: 'ceremony' }
  /** The stored envelope could not be read; no answer about the key. */
  | { kind: 'envelope' }
  /** A request failed before anything was changed. */
  | { kind: 'request'; error: ApiError }

/** BackupNow is the done screen's "Back up now" button. */
export type BackupNow =
  | { kind: 'idle' }
  | { kind: 'starting' }
  | { kind: 'started' }
  | { kind: 'failed'; error: ApiError }

export type RotationState =
  | { step: 'loading' }
  | { step: 'load_failed'; error: ApiError }
  /** No keys yet: nothing to replace. */
  | { step: 'not_set_up' }
  /** Below manager: the server would refuse the rotation. */
  | { step: 'needs_manager' }
  | {
      step: 'enter_current'
      failure: EntryFailure | undefined
      /** discardNew: a new key was shown but never took effect. */
      discardNew: boolean
    }
  | { step: 'working' }
  | { step: 'show_key'; recoveryKey: string }
  | {
      step: 'confirm'
      recoveryKey: string
      groups: number[]
      mismatch: boolean
      submitting: boolean
    }
  /** The POST may or may not have landed. Both keys are held privately. */
  | { step: 'uncertain'; error: ApiError; checking: boolean }
  /**
   * Terminal: someone else replaced the key since this page read it. Neither
   * key shown here is the one to keep.
   */
  | { step: 'replaced_elsewhere' }
  | { step: 'done'; mayRunBackup: boolean; backup: BackupNow }
  /** After dispose. Holds nothing. */
  | { step: 'closed' }

/** RecoveryKeyRotation drives one replacement. Create per mount; dispose() on unmount. */
export class RecoveryKeyRotation {
  private current: RotationState = { step: 'loading' }
  private role = 'viewer'
  private name: string | undefined
  /** oldKey is what the user typed; held until the rotation is settled. */
  private oldKey: string | undefined
  /** pending is the ceremony's result; its new key is also in state while shown. */
  private pending: RotationCeremony | undefined

  constructor(
    private readonly spaceId: string,
    private readonly deps: RotationDeps
  ) {}

  get state(): RotationState {
    return this.current
  }

  get spaceName(): string | undefined {
    return this.name
  }

  async start(): Promise<void> {
    this.set({ step: 'loading' })
    let status: BackupStatus
    try {
      const [spaces, loaded] = await Promise.all([
        this.deps.api.listSpaces(),
        this.deps.api.status(this.spaceId)
      ])
      const space = spaces.find((s) => s.id === this.spaceId)
      if (!space) {
        throw new ApiError('not_found', 'space not in the caller’s list')
      }
      this.role = space.role
      this.name = space.name
      status = loaded
    } catch (err: unknown) {
      this.set({ step: 'load_failed', error: asApiError(err) })
      return
    }
    if (!status.keys_configured) {
      this.set({ step: 'not_set_up' })
    } else if (!canManageKeys(this.role)) {
      this.set({ step: 'needs_manager' })
    } else {
      this.set({ step: 'enter_current', failure: undefined, discardNew: false })
    }
  }

  /**
   * useCurrentKey unwraps the Space's envelope with the key the user typed and,
   * if it opens, prepares a new key around the same Data Key. A wrong key fails
   * here, before a new key exists.
   */
  async useCurrentKey(currentKey: string): Promise<void> {
    const s = this.current
    if (s.step !== 'enter_current' || !canManageKeys(this.role)) {
      return
    }
    this.set({ step: 'working' })

    let stored: RecoveryEnvelope
    try {
      stored = await this.deps.api.recoveryEnvelope(this.spaceId)
    } catch (err: unknown) {
      this.retryEntry({ kind: 'request', error: asApiError(err) })
      return
    }
    const envelope = decodeEnvelope(stored)
    if (!envelope) {
      this.retryEntry({ kind: 'envelope' })
      return
    }

    await this.deps.yieldToRender()
    let ceremony: RotationCeremony
    try {
      ceremony = await this.deps.rotate({
        currentEnvelope: envelope,
        currentRecoveryKey: currentKey
      })
    } catch (err: unknown) {
      this.retryEntry(entryFailure(err))
      return
    }
    if (this.step() !== 'working') {
      // Disposed while Argon2id ran; the ceremony already wiped its Data Key.
      return
    }
    this.oldKey = currentKey
    this.pending = ceremony
    this.set({ step: 'show_key', recoveryKey: ceremony.recoveryKey })
  }

  keySaved(): void {
    const s = this.current
    if (s.step === 'show_key') {
      this.set({
        step: 'confirm',
        recoveryKey: s.recoveryKey,
        groups: pickGateGroups(this.deps.randomInt),
        mismatch: false,
        submitting: false
      })
    }
  }

  showKeyAgain(): void {
    const s = this.current
    if (s.step === 'confirm' && !s.submitting) {
      this.set({ step: 'show_key', recoveryKey: s.recoveryKey })
    }
  }

  /** confirmKey is the gate. Only matching answers send the rotation. */
  async confirmKey(answers: string[]): Promise<void> {
    const s = this.current
    if (s.step !== 'confirm' || s.submitting || !this.pending) {
      return
    }
    if (!gateMatches(s.recoveryKey, s.groups, answers)) {
      this.set({ ...s, mismatch: true })
      return
    }
    this.set({ ...s, mismatch: false, submitting: true })
    await this.submit(this.pending)
  }

  /**
   * checkAgain settles an ambiguous POST by trying both keys against what the
   * server now stores (decision 1).
   */
  async checkAgain(): Promise<void> {
    const s = this.current
    if (s.step !== 'uncertain' || s.checking || !this.pending || this.oldKey === undefined) {
      return
    }
    this.set({ ...s, checking: true })
    const newKey = this.pending.recoveryKey
    const oldKey = this.oldKey
    try {
      const envelope = decodeEnvelope(await this.deps.api.recoveryEnvelope(this.spaceId))
      if (!envelope) {
        throw unreadableEnvelope()
      }
      await this.deps.yieldToRender()
      if (await this.deps.keyOpens(envelope, newKey)) {
        this.finish()
        return
      }
      if (await this.deps.keyOpens(envelope, oldKey)) {
        this.forgetKeys()
        this.set({ step: 'enter_current', failure: undefined, discardNew: true })
        return
      }
    } catch (err: unknown) {
      // A failed read, or an envelope that is not one: no answer about either
      // key, so the question stays open.
      const error = isApiError(err) ? err : unreadableEnvelope()
      if (this.step() === 'uncertain') {
        this.set({ step: 'uncertain', error, checking: false })
      }
      return
    }
    this.forgetKeys()
    this.set({ step: 'replaced_elsewhere' })
  }

  /** backUpNow starts a run, which publishes the new envelope to the target. */
  async backUpNow(): Promise<void> {
    const s = this.current
    if (s.step !== 'done' || !s.mayRunBackup || s.backup.kind === 'starting') {
      return
    }
    this.set({ ...s, backup: { kind: 'starting' } })
    let backup: BackupNow = { kind: 'started' }
    try {
      await this.deps.api.runBackup(this.spaceId)
    } catch (err: unknown) {
      const error = asApiError(err)
      // A run already under way publishes the envelope just the same.
      if (error.code !== 'run_in_progress') {
        backup = { kind: 'failed', error }
      }
    }
    const now = this.current
    if (now.step === 'done') {
      this.set({ ...now, backup })
    }
  }

  dispose(): void {
    this.forgetKeys()
    this.set({ step: 'closed' })
  }

  // --- internals ----------------------------------------------------------

  private async submit(ceremony: RotationCeremony): Promise<void> {
    let failure: ApiError | undefined
    try {
      await this.deps.api.rotateRecoveryKey(this.spaceId, ceremony.request)
    } catch (err: unknown) {
      failure = asApiError(err)
    }
    if (this.step() === 'closed') {
      return
    }
    if (failure === undefined) {
      this.finish()
      return
    }
    if (failure.code === 'conflict') {
      // The precondition failed: the envelope this rotation replaced is gone.
      this.forgetKeys()
      this.set({ step: 'replaced_elsewhere' })
      return
    }
    if (mayHaveLanded(failure)) {
      this.set({ step: 'uncertain', error: failure, checking: false })
      return
    }
    // Refused outright: nothing changed, the current key still holds.
    this.forgetKeys()
    this.set({
      step: 'enter_current',
      failure: { kind: 'request', error: failure },
      discardNew: true
    })
  }

  private finish(): void {
    this.forgetKeys()
    this.set({
      step: 'done',
      mayRunBackup: canOperate(this.role),
      backup: { kind: 'idle' }
    })
  }

  private retryEntry(failure: EntryFailure): void {
    if (this.step() === 'working') {
      this.set({ step: 'enter_current', failure, discardNew: false })
    }
  }

  private forgetKeys(): void {
    this.oldKey = undefined
    this.pending = undefined
  }

  private step(): RotationState['step'] {
    return this.current.step
  }

  private set(state: RotationState): void {
    if (this.current.step === 'closed' && state.step !== 'closed') {
      return
    }
    this.current = state
    this.deps.onChange(state)
  }
}

/** entryFailure classifies what performRecoveryKeyRotation threw. */
function entryFailure(err: unknown): EntryFailure {
  if (err instanceof RecoveryKeyError) {
    return { kind: 'malformed' }
  }
  if (err instanceof UnwrapError) {
    return { kind: 'wrong' }
  }
  if (err instanceof CeremonyError) {
    // bad_data_key: the key opened the envelope to something that is not a
    // Data Key, which recoveryKeyOpens also calls "does not open".
    return err.code === 'bad_data_key' ? { kind: 'wrong' } : { kind: 'ceremony' }
  }
  if (err instanceof EnvelopeError) {
    return { kind: 'envelope' }
  }
  return { kind: 'ceremony' }
}

/** decodeEnvelope is the stored envelope's bytes, or undefined for bad base64. */
function decodeEnvelope(stored: RecoveryEnvelope): Uint8Array | undefined {
  try {
    return base64Decode(stored.envelope)
  } catch {
    return undefined
  }
}

function unreadableEnvelope(): ApiError {
  return new ApiError('malformed_response', 'the stored recovery envelope is not readable')
}
