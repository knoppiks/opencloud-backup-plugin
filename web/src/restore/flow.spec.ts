import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { fakeApi, job, snapshot, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import { RestoreFlow, type RestoreApi, type RestoreState } from './flow'

const OLDER = snapshot({ id: 'k-older', taken_at: '2026-09-22T01:30:00Z' })
const NEWER = snapshot({ id: 'k-newer', taken_at: '2026-09-23T01:30:00Z' })
const FOLDER = 'Restore/2026-09-25T09-00-00Z'

let api: FakeApi
let states: RestoreState[]
let flow: RestoreFlow

function restoreJob(overrides: Parameters<typeof job>[0] = {}) {
  return job({
    id: 'job-r',
    kind: 'restore',
    state: 'running',
    trigger: 'manual',
    restore_folder: FOLDER,
    ...overrides
  })
}

function step(): string {
  return flow.state.step
}

beforeEach(() => {
  api = fakeApi()
  states = []
  flow = new RestoreFlow(SPACE_ID, {
    api: api as unknown as RestoreApi,
    onChange: (s) => states.push(s)
  })
  api.listSpaces.mockResolvedValue([space({ role: 'viewer' })])
  api.status.mockResolvedValue(status())
  api.listSnapshots.mockResolvedValue([NEWER, OLDER])
})

/** atConfirm starts the flow and moves to the confirmation for a snapshot. */
async function atConfirm(snapshotId = NEWER.id) {
  await flow.start()
  flow.select(snapshotId)
  flow.review()
}

describe('RestoreFlow start', () => {
  it('lists the backups with the newest preselected', async () => {
    await flow.start()
    expect(flow.state).toEqual({
      step: 'pick',
      snapshots: [NEWER, OLDER],
      selected: NEWER.id,
      notice: undefined
    })
    expect(flow.spaceName).toBe('Family photos')
  })

  // decisions.md #7: disaster recovery is a member capability. A viewer is
  // offered the whole flow; the server allows it too.
  it('offers a viewer the picker', async () => {
    await flow.start()
    expect(step()).toBe('pick')
  })

  it('an empty list is a picker with nothing in it, not an error', async () => {
    api.listSnapshots.mockResolvedValue([])
    await flow.start()
    expect(flow.state).toMatchObject({ step: 'pick', snapshots: [], selected: undefined })
  })

  it.each([
    ['no binding', { configured: false }],
    ['no keys', { keys_configured: false }]
  ])('says there is nothing to restore with %s, and lists nothing', async (_, overrides) => {
    api.status.mockResolvedValue(status(overrides))
    await flow.start()
    expect(step()).toBe('not_set_up')
    expect(api.listSnapshots).not.toHaveBeenCalled()
  })

  it('follows a restore that is already running instead of offering another', async () => {
    api.status.mockResolvedValue(status({ running: true, current_job: restoreJob() }))
    api.run.mockResolvedValue(restoreJob())
    await flow.start()
    expect(flow.state).toMatchObject({
      step: 'running',
      jobId: 'job-r',
      job: { restore_folder: FOLDER },
      snapshot: undefined
    })
    expect(flow.following).toBe(true)
    expect(api.listSnapshots).not.toHaveBeenCalled()
  })

  it('does not mistake a running backup for a restore', async () => {
    api.status.mockResolvedValue(
      status({ running: true, current_job: job({ id: 'job-b', state: 'running' }) })
    )
    await flow.start()
    expect(step()).toBe('pick')
  })

  it('fails the load for a Space not in the caller’s list', async () => {
    api.listSpaces.mockResolvedValue([])
    await flow.start()
    expect(flow.state).toMatchObject({ step: 'load_failed', error: { code: 'not_found' } })
  })

  it('fails the load when the backups cannot be listed', async () => {
    api.listSnapshots.mockRejectedValue(new ApiError('target_unavailable', 'x', 409))
    await flow.start()
    expect(flow.state).toMatchObject({ step: 'load_failed', error: { code: 'target_unavailable' } })
  })
})

describe('RestoreFlow pick and confirm', () => {
  it('selects only a listed backup', async () => {
    await flow.start()
    flow.select('k-unknown')
    expect(flow.state).toMatchObject({ selected: NEWER.id })
    flow.select(OLDER.id)
    expect(flow.state).toMatchObject({ selected: OLDER.id })
  })

  it('reviews the selected backup; nothing is sent', async () => {
    await atConfirm(OLDER.id)
    expect(flow.state).toEqual({
      step: 'confirm',
      snapshot: OLDER,
      starting: false,
      error: undefined
    })
    expect(api.restore).not.toHaveBeenCalled()
  })

  it('goes back to the picker with the same backup selected', async () => {
    await atConfirm(OLDER.id)
    await flow.back()
    expect(flow.state).toMatchObject({ step: 'pick', selected: OLDER.id })
    expect(api.restore).not.toHaveBeenCalled()
  })

  it('sends the snapshot id and follows the run it started', async () => {
    api.restore.mockResolvedValue({ job_id: 'job-r', space_id: SPACE_ID, snapshot_id: NEWER.id })
    api.run.mockResolvedValue(restoreJob())
    await atConfirm()

    await flow.confirm()

    expect(api.restore).toHaveBeenCalledWith(SPACE_ID, NEWER.id)
    expect(api.run).toHaveBeenCalledWith(SPACE_ID, 'job-r')
    expect(flow.state).toMatchObject({
      step: 'running',
      jobId: 'job-r',
      job: { restore_folder: FOLDER },
      snapshot: NEWER,
      refreshFailed: false
    })
  })

  it('shows "starting" while the request is out, and never sends twice', async () => {
    let answer: (v: unknown) => void = () => {}
    api.restore.mockReturnValue(new Promise((resolve) => (answer = resolve)))
    api.run.mockResolvedValue(restoreJob())
    await atConfirm()

    const first = flow.confirm()
    expect(flow.state).toMatchObject({ step: 'confirm', starting: true })
    await flow.confirm()
    await flow.back()
    expect(api.restore).toHaveBeenCalledTimes(1)
    expect(step()).toBe('confirm')

    answer({ job_id: 'job-r' })
    await first
    expect(step()).toBe('running')
  })
})

describe('RestoreFlow start failures', () => {
  it('re-lists with a notice when the backup aged out meanwhile', async () => {
    api.restore.mockRejectedValue(new ApiError('not_found', 'x', 404))
    await atConfirm()
    api.listSnapshots.mockResolvedValue([OLDER])

    await flow.confirm()

    expect(flow.state).toEqual({
      step: 'pick',
      snapshots: [OLDER],
      selected: OLDER.id,
      notice: 'snapshot_gone'
    })
  })

  it('a plain refusal stays on the confirmation with the reason', async () => {
    api.restore.mockRejectedValue(new ApiError('target_unavailable', 'x', 409))
    await atConfirm()
    await flow.confirm()
    expect(flow.state).toMatchObject({
      step: 'confirm',
      starting: false,
      error: { code: 'target_unavailable' }
    })
    expect(api.status).toHaveBeenCalledTimes(1) // start() only
  })

  it.each([
    ['offline', new ApiError('offline', 'x')],
    ['timeout', new ApiError('timeout', 'x')],
    ['a 5xx', new ApiError('internal_error', 'x', 500)],
    ['run_in_progress', new ApiError('run_in_progress', 'x', 409)]
  ])('after %s, follows a restore the status shows running', async (_, error) => {
    api.restore.mockRejectedValue(error)
    api.run.mockResolvedValue(restoreJob())
    await atConfirm()
    api.status.mockResolvedValue(status({ running: true, current_job: restoreJob() }))

    await flow.confirm()

    expect(flow.state).toMatchObject({ step: 'running', jobId: 'job-r', snapshot: undefined })
    expect(api.restore).toHaveBeenCalledTimes(1)
  })

  it.each([
    ['nothing runs', status()],
    ['a backup runs', status({ running: true, current_job: job({ state: 'running' }) })]
  ])('after run_in_progress where %s, stays with the reason', async (_, now) => {
    api.restore.mockRejectedValue(new ApiError('run_in_progress', 'x', 409))
    await atConfirm()
    api.status.mockResolvedValue(now)

    await flow.confirm()

    expect(flow.state).toMatchObject({ step: 'confirm', error: { code: 'run_in_progress' } })
  })

  it('an unanswered POST whose status cannot be read either stays with the reason', async () => {
    api.restore.mockRejectedValue(new ApiError('offline', 'x'))
    await atConfirm()
    api.status.mockRejectedValue(new ApiError('offline', 'x'))

    await flow.confirm()

    expect(flow.state).toMatchObject({
      step: 'confirm',
      starting: false,
      error: { code: 'offline' }
    })
  })
})

describe('RestoreFlow following a run', () => {
  async function running() {
    api.restore.mockResolvedValue({ job_id: 'job-r' })
    api.run.mockResolvedValue(restoreJob())
    await atConfirm()
    await flow.confirm()
  }

  it('ends succeeded with the record, including the folder', async () => {
    await running()
    const done = restoreJob({ state: 'succeeded', file_count: 12, total_bytes: 3400 })
    api.run.mockResolvedValue(done)

    await flow.refresh()

    expect(flow.state).toEqual({ step: 'succeeded', job: done, snapshot: NEWER })
    expect(flow.following).toBe(false)
  })

  it('ends failed with the record, so a partial folder can still be found', async () => {
    await running()
    const failed = restoreJob({ state: 'failed', error: 'the restore run failed' })
    api.run.mockResolvedValue(failed)

    await flow.refresh()

    expect(flow.state).toEqual({ step: 'failed', job: failed, snapshot: NEWER })
  })

  it('a failed read keeps following and says so; the next good read clears it', async () => {
    await running()
    api.run.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await flow.refresh()
    expect(flow.state).toMatchObject({ step: 'running', refreshFailed: true })
    expect(flow.following).toBe(true)

    await flow.refresh()
    expect(flow.state).toMatchObject({ step: 'running', refreshFailed: false })
  })

  it('a run whose record is gone is "lost", not polled forever', async () => {
    await running()
    api.run.mockRejectedValue(new ApiError('not_found', 'x', 404))
    await flow.refresh()
    expect(step()).toBe('lost')
    expect(flow.following).toBe(false)
  })

  it('offers another restore once the run has ended', async () => {
    await running()
    api.run.mockResolvedValue(restoreJob({ state: 'succeeded' }))
    await flow.refresh()

    await flow.pickAnother()

    expect(step()).toBe('pick')
  })

  it('refresh outside a run does nothing', async () => {
    await flow.start()
    await flow.refresh()
    expect(api.run).not.toHaveBeenCalled()
  })
})

describe('RestoreFlow dispose', () => {
  it('ignores answers that arrive after the page is gone', async () => {
    let answer: (v: unknown) => void = () => {}
    api.listSnapshots.mockReturnValue(new Promise((resolve) => (answer = resolve)))
    const started = flow.start()
    await vi.waitFor(() => expect(api.listSnapshots).toHaveBeenCalled())

    flow.dispose()
    answer([NEWER])
    await started

    expect(step()).toBe('closed')
    expect(states.at(-1)).toEqual({ step: 'closed' })
  })
})
