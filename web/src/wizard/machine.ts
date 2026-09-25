// The setup wizard as a state machine, with no Vue in it.
//
// One instance belongs to one mounted wizard. It reads where the Space's setup
// has got to from the server, never from browser storage, and moves through the
// fixed write order of 8d decision 3:
//
//   PUT /backup/config (enabled: false)  ->  POST /backup/setup  ->  PUT /backup/schedule (enabled: true)
//
// `enabled` turns on at the last step and not before, because the scheduler
// does not look at keys: an enabled Space without them fails every night and
// says so every night (8d.2 decision 1).
//
// Key hygiene, which the tests hold this module to:
//
//   - The Recovery Key is in `state` only between the ceremony and the setup
//     POST settling (or, after an ambiguous failure, until it has been checked
//     against what the server stored). It is never passed to the API.
//   - The Data Key is never in `state`. It is held privately and zeroized as
//     soon as the setup POST settles, whichever way, and on dispose.
//   - A 409 from setup is "already protected" and there is no path back to the
//     ceremony from it (decisions.md #17).

import { ApiError, asApiError, mayHaveLanded } from '../api'
import type {
  BackupConfig,
  BackupConfigRequest,
  BackupStatus,
  KeyStatus,
  RecoveryEnvelope,
  Schedule,
  SchedulePreset,
  ScheduleRequest,
  Space,
  Target
} from '../api'
import { base64Decode, zeroize, type SetupCeremony, type SetupRequest } from '../crypto'
import { gateMatches, pickGateGroups } from '../recoverykey/gate'
import { canManageKeys, canOperate } from '../status/roles'
import { nextStep } from '../status/setupstep'

export { nextStep, type Step } from '../status/setupstep'
export {
  cryptoRandomInt,
  GATE_GROUPS,
  gateMatches,
  nextFrame,
  pickGateGroups
} from '../recoverykey/gate'

/** WizardApi is the slice of the client the wizard uses. */
export interface WizardApi {
  listSpaces(): Promise<Space[]>
  listTargets(): Promise<Target[]>
  status(spaceId: string): Promise<BackupStatus>
  setBackupConfig(spaceId: string, body: BackupConfigRequest): Promise<BackupConfig>
  setupKeys(spaceId: string, body: SetupRequest): Promise<KeyStatus>
  recoveryEnvelope(spaceId: string): Promise<RecoveryEnvelope>
  setSchedule(spaceId: string, body: ScheduleRequest): Promise<Schedule>
}

/** WizardDeps is everything the machine does not decide for itself. */
export interface WizardDeps {
  api: WizardApi
  /** ceremony is performSetupCeremony in production; slow, so tests fake it. */
  ceremony: () => Promise<SetupCeremony>
  /** keyOpens is recoveryKeyOpens in production. */
  keyOpens: (envelope: Uint8Array, recoveryKey: string) => Promise<boolean>
  /** randomInt returns an integer in [0, bound). */
  randomInt: (bound: number) => number
  /**
   * yieldToRender lets the page paint "this takes a moment" before Argon2id
   * holds the main thread (8d.2 decision 4).
   */
  yieldToRender: () => Promise<void>
  onChange: (state: WizardState) => void
}

/** ScheduleChoice is what the schedule step offers: no cron, ever. */
export interface ScheduleChoice {
  kind: 'daily' | 'weekly'
  hour: number
  minute: number
  /** weekday is 0 (Sunday) to 6; used by weekly only. */
  weekday: number
}

/** DEFAULT_SCHEDULE is daily at 02:30 in the service's zone (8d decision 5). */
export const DEFAULT_SCHEDULE: ScheduleChoice = { kind: 'daily', hour: 2, minute: 30, weekday: 0 }

/**
 * WizardState is what the wizard shows. `step` is the screen; the rest is what
 * that screen needs and nothing more.
 */
export type WizardState =
  | { step: 'loading' }
  | { step: 'load_failed'; error: ApiError }
  /** A viewer: the server would refuse every write, so none is offered. */
  | { step: 'not_allowed' }
  /** No granted target: only an administrator can change that. */
  | { step: 'no_targets' }
  | {
      step: 'pick_target'
      targets: Target[]
      selected: string | undefined
      saving: boolean
      error: ApiError | undefined
    }
  /** An editor has done what an editor can; the keys need a manager. */
  | { step: 'needs_manager' }
  | {
      step: 'key_intro'
      failure: KeyFailure | undefined
      /** discardPrevious: a key shown earlier never took effect. */
      discardPrevious: boolean
    }
  | { step: 'generating' }
  | { step: 'show_key'; recoveryKey: string }
  | {
      step: 'confirm'
      recoveryKey: string
      /** groups are 0-based indices into the key's seven groups, ascending. */
      groups: number[]
      mismatch: boolean
      submitting: boolean
    }
  /**
   * The setup POST may or may not have landed (offline, timeout, 5xx). The
   * key the user saved is kept so a retry can test it against what the server
   * stored (8d.2 decision 5).
   */
  | { step: 'setup_uncertain'; recoveryKey: string; error: ApiError; checking: boolean }
  /** Terminal: the Space has keys this wizard did not make. Never re-run. */
  | { step: 'already_protected' }
  | {
      step: 'schedule'
      choice: ScheduleChoice
      timezone: string | undefined
      saving: boolean
      error: ApiError | undefined
    }
  | { step: 'done'; status: BackupStatus }
  /** After dispose. Holds nothing. */
  | { step: 'closed' }

/** KeyFailure is why the key step is being shown again. */
export type KeyFailure =
  /** The ceremony refused its own output (self-verify) or could not run. */
  | { kind: 'ceremony' }
  /** The server refused setup outright (4xx other than 409): nothing landed. */
  | { kind: 'refused'; error: ApiError }

/**
 * SetupWizard drives one Space's setup. Create it per mounted wizard, call
 * start(), and dispose() on unmount.
 */
export class SetupWizard {
  private current: WizardState = { step: 'loading' }
  private role = 'viewer'
  private name: string | undefined
  private status: BackupStatus | undefined
  /** pending holds the Data Key between ceremony and POST. Never in state. */
  private pending: SetupCeremony | undefined

  constructor(
    private readonly spaceId: string,
    private readonly deps: WizardDeps
  ) {}

  get state(): WizardState {
    return this.current
  }

  /** spaceName is the Space's display name once start() has read it. */
  get spaceName(): string | undefined {
    return this.name
  }

  /** start reads the Space's role and status and resumes where setup is. */
  async start(): Promise<void> {
    this.set({ step: 'loading' })
    try {
      const [spaces, status] = await Promise.all([
        this.deps.api.listSpaces(),
        this.deps.api.status(this.spaceId)
      ])
      const space = spaces.find((s) => s.id === this.spaceId)
      if (!space) {
        throw new ApiError('not_found', 'space not in the caller’s list')
      }
      this.role = space.role
      this.name = space.name
      this.status = status
    } catch (err: unknown) {
      this.set({ step: 'load_failed', error: asApiError(err) })
      return
    }
    await this.advance()
  }

  // --- target -------------------------------------------------------------

  /** selectTarget changes the picked target; nothing is written yet. */
  selectTarget(targetId: string): void {
    const s = this.current
    if (s.step === 'pick_target' && !s.saving) {
      this.set({ ...s, selected: targetId, error: undefined })
    }
  }

  /** confirmTarget binds the Space to the picked target, runs still off. */
  async confirmTarget(): Promise<void> {
    const s = this.current
    if (s.step !== 'pick_target' || s.saving || s.selected === undefined) {
      return
    }
    await this.bindTarget(s.targets, s.selected)
  }

  // --- keys ---------------------------------------------------------------

  /** createKey runs the ceremony. Manager only; the server checks too. */
  async createKey(): Promise<void> {
    if (this.current.step !== 'key_intro' || !canManageKeys(this.role)) {
      return
    }
    this.set({ step: 'generating' })
    await this.deps.yieldToRender()

    let ceremony: SetupCeremony
    try {
      ceremony = await this.deps.ceremony()
    } catch {
      // A CeremonyError means the key would not have opened its own envelope:
      // it must not be shown, and nothing is sent. Any other failure here is
      // the same outcome for the user.
      this.set({ step: 'key_intro', failure: { kind: 'ceremony' }, discardPrevious: false })
      return
    }
    if (this.step() !== 'generating') {
      // Disposed while Argon2id ran.
      zeroize(ceremony.dataKey)
      return
    }
    this.pending = ceremony
    this.set({ step: 'show_key', recoveryKey: ceremony.recoveryKey })
  }

  /** keySaved moves from showing the key to the gate. */
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

  /** showKeyAgain goes back from the gate to the key. */
  showKeyAgain(): void {
    const s = this.current
    if (s.step === 'confirm' && !s.submitting) {
      this.set({ step: 'show_key', recoveryKey: s.recoveryKey })
    }
  }

  /**
   * confirmKey is the gate. Only answers that match both groups send setup;
   * there is no other way to reach the POST.
   */
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
    await this.submitSetup(s.recoveryKey)
  }

  /**
   * retryUncertain resolves an ambiguous setup failure by asking the server
   * what it has, and testing the saved key against it.
   */
  async retryUncertain(): Promise<void> {
    const s = this.current
    if (s.step !== 'setup_uncertain' || s.checking) {
      return
    }
    this.set({ ...s, checking: true })
    try {
      const status = await this.deps.api.status(this.spaceId)
      this.status = status
      if (!status.keys_configured) {
        // It never landed. The key the user saved protects nothing.
        this.set({ step: 'key_intro', failure: undefined, discardPrevious: true })
        return
      }
      const stored = await this.deps.api.recoveryEnvelope(this.spaceId)
      const opens = await this.deps.keyOpens(base64Decode(stored.envelope), s.recoveryKey)
      if (!opens) {
        this.set({ step: 'already_protected' })
        return
      }
    } catch (err: unknown) {
      this.set({ ...s, error: asApiError(err), checking: false })
      return
    }
    await this.advance()
  }

  // --- schedule -----------------------------------------------------------

  /** chooseSchedule edits the schedule form. */
  chooseSchedule(choice: ScheduleChoice): void {
    const s = this.current
    if (s.step === 'schedule' && !s.saving) {
      this.set({ ...s, choice, error: undefined })
    }
  }

  /** saveSchedule stores the schedule and switches scheduled runs on. */
  async saveSchedule(): Promise<void> {
    const s = this.current
    if (s.step !== 'schedule' || s.saving) {
      return
    }
    this.set({ ...s, saving: true, error: undefined })
    try {
      await this.deps.api.setSchedule(this.spaceId, {
        enabled: true,
        preset: toPreset(s.choice)
      })
      const status = await this.deps.api.status(this.spaceId)
      this.status = status
      this.set({ step: 'done', status })
    } catch (err: unknown) {
      this.set({ ...s, saving: false, error: asApiError(err) })
    }
  }

  // --- lifecycle ----------------------------------------------------------

  /** dispose forgets the Recovery Key and zeroizes any Data Key still held. */
  dispose(): void {
    this.forgetKeys()
    this.set({ step: 'closed' })
  }

  // --- internals ----------------------------------------------------------

  /** advance shows the first step the server has not seen. */
  private async advance(): Promise<void> {
    const status = this.status
    if (!status) {
      return
    }
    if (!canOperate(this.role)) {
      this.set({ step: 'not_allowed' })
      return
    }
    switch (nextStep(status)) {
      case 'target':
        await this.offerTargets()
        return
      case 'keys':
        this.set(
          canManageKeys(this.role)
            ? { step: 'key_intro', failure: undefined, discardPrevious: false }
            : { step: 'needs_manager' }
        )
        return
      case 'schedule':
        this.set({
          step: 'schedule',
          choice: scheduleFrom(status),
          timezone: status.timezone,
          saving: false,
          error: undefined
        })
        return
      case 'done':
        this.set({ step: 'done', status })
    }
  }

  /** offerTargets skips the picker when there is exactly one target. */
  private async offerTargets(): Promise<void> {
    let targets: Target[]
    try {
      targets = await this.deps.api.listTargets()
    } catch (err: unknown) {
      this.set({ step: 'load_failed', error: asApiError(err) })
      return
    }
    if (targets.length === 0) {
      this.set({ step: 'no_targets' })
      return
    }
    if (targets.length === 1) {
      await this.bindTarget(targets, (targets[0] as Target).id)
      return
    }
    this.set({
      step: 'pick_target',
      targets,
      selected: undefined,
      saving: false,
      error: undefined
    })
  }

  private async bindTarget(targets: Target[], targetId: string): Promise<void> {
    this.set({ step: 'pick_target', targets, selected: targetId, saving: true, error: undefined })
    try {
      // retention_days 0 is the default window; enabled stays off until the
      // schedule step, because keys do not exist yet (8d.2 decision 1).
      await this.deps.api.setBackupConfig(this.spaceId, {
        target_id: targetId,
        retention_days: 0,
        enabled: false
      })
    } catch (err: unknown) {
      this.set({
        step: 'pick_target',
        targets,
        selected: targetId,
        saving: false,
        error: asApiError(err)
      })
      return
    }
    this.status = { ...(this.status as BackupStatus), configured: true, enabled: false }
    await this.advance()
  }

  private async submitSetup(recoveryKey: string): Promise<void> {
    const ceremony = this.pending as SetupCeremony
    let failure: ApiError | undefined
    try {
      await this.deps.api.setupKeys(this.spaceId, ceremony.request)
    } catch (err: unknown) {
      failure = asApiError(err)
    } finally {
      // Settled, either way: the Data Key has done its one job or cannot do
      // it again, since a retry needs a fresh ceremony.
      zeroize(ceremony.dataKey)
      this.pending = undefined
    }

    if (this.step() === 'closed') {
      return
    }
    if (failure === undefined) {
      this.status = { ...(this.status as BackupStatus), keys_configured: true }
      await this.advance()
      return
    }
    if (failure.code === 'conflict') {
      this.set({ step: 'already_protected' })
      return
    }
    if (mayHaveLanded(failure)) {
      this.set({ step: 'setup_uncertain', recoveryKey, error: failure, checking: false })
      return
    }
    this.set({
      step: 'key_intro',
      failure: { kind: 'refused', error: failure },
      discardPrevious: true
    })
  }

  private forgetKeys(): void {
    if (this.pending) {
      zeroize(this.pending.dataKey)
      this.pending = undefined
    }
  }

  /** step reads the current step without the narrowing an await invalidates. */
  private step(): WizardState['step'] {
    return this.current.step
  }

  private set(state: WizardState): void {
    if (this.current.step === 'closed' && state.step !== 'closed') {
      return
    }
    this.current = state
    this.deps.onChange(state)
  }
}

/** scheduleFrom preselects what the Space has, or daily at 02:30. */
function scheduleFrom(status: BackupStatus): ScheduleChoice {
  const preset = status.preset
  if (preset && (preset.kind === 'daily' || preset.kind === 'weekly')) {
    return {
      kind: preset.kind,
      hour: preset.hour,
      minute: preset.minute,
      weekday: preset.weekday ?? 0
    }
  }
  return { ...DEFAULT_SCHEDULE }
}

/** toPreset is the wire form; weekday only travels with weekly. */
function toPreset(choice: ScheduleChoice): SchedulePreset {
  return choice.kind === 'weekly'
    ? { kind: 'weekly', hour: choice.hour, minute: choice.minute, weekday: choice.weekday }
    : { kind: 'daily', hour: choice.hour, minute: choice.minute }
}
