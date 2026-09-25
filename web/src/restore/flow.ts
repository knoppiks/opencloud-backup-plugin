// The restore flow (Path B) as a state machine, with no Vue and no timers.
//
//   pick a backup  ->  confirm  ->  POST /restore  ->  follow the run  ->  done / failed
//
// The run is followed by its own id (`GET /backup/runs/{jobId}`), never through
// `status().current_job`: that is whatever runs *now*, which can be a backup
// that started a moment after the restore ended (8d.3 decision 1). Polling is
// the view's job; this module says whether there is anything left to poll.
//
// A restore is never started twice by accident. Two situations could do it:
//
//   - **The page is opened while a restore runs** (a reload, a second tab):
//     start() finds the running restore in the status and follows it instead
//     of offering the picker.
//   - **The POST fails in a way that says nothing about whether it landed**
//     (offline, timeout, 5xx), or it is refused because a run is in progress:
//     the status is read again, and a running restore is followed rather than
//     another one being offered. The server's run lock already prevents two
//     at once; this prevents a second one *afterwards*, which would be a
//     second full copy in the member's storage.

import { ApiError, asApiError, isApiError, mayHaveLanded } from '../api'
import type { BackupStatus, Job, RestoreAccepted, Snapshot, Space } from '../api'

/** RestoreApi is the slice of the client the flow uses. */
export interface RestoreApi {
  listSpaces(): Promise<Space[]>
  status(spaceId: string): Promise<BackupStatus>
  listSnapshots(spaceId: string): Promise<Snapshot[]>
  restore(spaceId: string, snapshotId: string): Promise<RestoreAccepted>
  run(spaceId: string, jobId: string): Promise<Job>
}

/** RestoreDeps is everything the flow does not decide for itself. */
export interface RestoreDeps {
  api: RestoreApi
  onChange: (state: RestoreState) => void
}

/** PickNotice is why the picker is being shown again. */
export type PickNotice =
  /** The chosen backup was gone by the time it was asked for (retention). */
  'snapshot_gone'

/**
 * RestoreState is what the page shows. `step` is the screen; the rest is what
 * that screen needs.
 */
export type RestoreState =
  | { step: 'loading' }
  | { step: 'load_failed'; error: ApiError }
  /** No binding or no keys: there is nothing to restore from yet. */
  | { step: 'not_set_up' }
  | {
      step: 'pick'
      snapshots: Snapshot[]
      selected: string | undefined
      notice: PickNotice | undefined
    }
  | { step: 'confirm'; snapshot: Snapshot; starting: boolean; error: ApiError | undefined }
  | {
      step: 'running'
      jobId: string
      /** job is the last record read; absent until the first read lands. */
      job: Job | undefined
      /** snapshot is known when this page started the run, not when it found one. */
      snapshot: Snapshot | undefined
      refreshFailed: boolean
    }
  | { step: 'succeeded'; job: Job; snapshot: Snapshot | undefined }
  | { step: 'failed'; job: Job; snapshot: Snapshot | undefined }
  /** The run's record is gone (not_found while following it). */
  | { step: 'lost' }
  /** After dispose. Holds nothing. */
  | { step: 'closed' }

/**
 * RestoreFlow drives one Space's restore page. Create it per mounted page, call
 * start(), and dispose() on unmount.
 */
export class RestoreFlow {
  private current: RestoreState = { step: 'loading' }
  private name: string | undefined

  constructor(
    private readonly spaceId: string,
    private readonly deps: RestoreDeps
  ) {}

  get state(): RestoreState {
    return this.current
  }

  /** spaceName is the Space's display name once start() has read it. */
  get spaceName(): string | undefined {
    return this.name
  }

  /** following reports whether there is a run to keep reading. */
  get following(): boolean {
    return this.current.step === 'running'
  }

  /**
   * start reads the Space and its status. A Space that is not set up has
   * nothing to restore; a restore already running is followed; otherwise the
   * backups are listed.
   */
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
      this.name = space.name
      status = loaded
    } catch (err: unknown) {
      this.set({ step: 'load_failed', error: asApiError(err) })
      return
    }

    if (!status.configured || !status.keys_configured) {
      this.set({ step: 'not_set_up' })
      return
    }
    const running = runningRestore(status)
    if (running) {
      this.follow(running, undefined)
      await this.refresh()
      return
    }
    await this.loadSnapshots(undefined)
  }

  /** select changes the picked backup; nothing is sent. */
  select(snapshotId: string): void {
    const s = this.current
    if (s.step === 'pick' && s.snapshots.some((snap) => snap.id === snapshotId)) {
      this.set({ ...s, selected: snapshotId })
    }
  }

  /** review moves from the picker to the confirmation. */
  review(): void {
    const s = this.current
    if (s.step !== 'pick') {
      return
    }
    const snapshot = s.snapshots.find((snap) => snap.id === s.selected)
    if (snapshot) {
      this.set({ step: 'confirm', snapshot, starting: false, error: undefined })
    }
  }

  /** back returns from the confirmation to the picker, keeping the choice. */
  async back(): Promise<void> {
    const s = this.current
    if (s.step === 'confirm' && !s.starting) {
      await this.loadSnapshots(undefined, s.snapshot.id)
    }
  }

  /** confirm starts the restore. It is the only call that writes. */
  async confirm(): Promise<void> {
    const s = this.current
    if (s.step !== 'confirm' || s.starting) {
      return
    }
    const snapshot = s.snapshot
    this.set({ ...s, starting: true, error: undefined })

    let accepted: RestoreAccepted
    try {
      accepted = await this.deps.api.restore(this.spaceId, snapshot.id)
    } catch (err: unknown) {
      await this.startFailed(snapshot, asApiError(err))
      return
    }
    this.follow(accepted.job_id, snapshot)
    await this.refresh()
  }

  /**
   * refresh reads the followed run once. A failed read keeps the last known
   * state and says so; the view keeps polling while `following` holds.
   */
  async refresh(): Promise<void> {
    const s = this.current
    if (s.step !== 'running') {
      return
    }
    let job: Job
    try {
      job = await this.deps.api.run(this.spaceId, s.jobId)
    } catch (err: unknown) {
      if (this.current !== s) {
        return
      }
      if (isApiError(err) && err.code === 'not_found') {
        this.set({ step: 'lost' })
        return
      }
      this.set({ ...s, refreshFailed: true })
      return
    }
    if (this.current !== s) {
      return
    }
    if (job.state === 'succeeded') {
      this.set({ step: 'succeeded', job, snapshot: s.snapshot })
    } else if (job.state === 'failed') {
      this.set({ step: 'failed', job, snapshot: s.snapshot })
    } else {
      this.set({ ...s, job, refreshFailed: false })
    }
  }

  /** pickAnother returns to the picker after a run ended or was lost. */
  async pickAnother(): Promise<void> {
    const step = this.current.step
    if (step === 'succeeded' || step === 'failed' || step === 'lost') {
      await this.loadSnapshots(undefined)
    }
  }

  /** dispose ends the flow; late answers change nothing. */
  dispose(): void {
    this.set({ step: 'closed' })
  }

  // --- internals ----------------------------------------------------------

  private async loadSnapshots(notice: PickNotice | undefined, keep?: string): Promise<void> {
    this.set({ step: 'loading' })
    let snapshots: Snapshot[]
    try {
      snapshots = await this.deps.api.listSnapshots(this.spaceId)
    } catch (err: unknown) {
      this.set({ step: 'load_failed', error: asApiError(err) })
      return
    }
    // Newest first from the server; the newest is what "restore" usually
    // means, so it is preselected. The confirmation is the deliberate step.
    const selected = snapshots.some((s) => s.id === keep) ? keep : snapshots[0]?.id
    this.set({ step: 'pick', snapshots, selected, notice })
  }

  /**
   * startFailed decides what a refused or unanswered POST means.
   *
   * - not_found: the backup aged out between listing and asking. Re-list.
   * - run_in_progress, or no answer at all: re-read status. A running restore
   *   is followed — it may be this very request — instead of offering to
   *   start another.
   * - anything else: nothing started; say why and let them try again.
   */
  private async startFailed(snapshot: Snapshot, error: ApiError): Promise<void> {
    if (error.code === 'not_found') {
      await this.loadSnapshots('snapshot_gone')
      return
    }
    if (error.code === 'run_in_progress' || mayHaveLanded(error)) {
      const running = await this.runningRestoreNow()
      if (this.current.step === 'closed') {
        return
      }
      if (running) {
        this.follow(running, undefined)
        await this.refresh()
        return
      }
    }
    this.set({ step: 'confirm', snapshot, starting: false, error })
  }

  /** runningRestoreNow re-reads status; a failed read is "don't know". */
  private async runningRestoreNow(): Promise<string | undefined> {
    try {
      return runningRestore(await this.deps.api.status(this.spaceId))
    } catch {
      return undefined
    }
  }

  private follow(jobId: string, snapshot: Snapshot | undefined): void {
    this.set({ step: 'running', jobId, job: undefined, snapshot, refreshFailed: false })
  }

  private set(state: RestoreState): void {
    if (this.current.step === 'closed' && state.step !== 'closed') {
      return
    }
    this.current = state
    this.deps.onChange(state)
  }
}

/** runningRestore is the id of a restore the status reports as running. */
function runningRestore(status: BackupStatus): string | undefined {
  const job = status.current_job
  if (status.running && job && job.kind === 'restore' && !isTerminal(job.state)) {
    return job.id
  }
  return undefined
}

function isTerminal(state: string): boolean {
  return state === 'succeeded' || state === 'failed'
}
