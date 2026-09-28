import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { fakeApi, job, snapshot, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import RestoreView from './RestoreView.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

const link = vi.hoisted(() => ({ to: vi.fn() }))
vi.mock('../composables/useRestoreFolderLink', () => ({
  useRestoreFolderLink: () => link.to
}))

const NEWER = snapshot({
  id: 'k-newer',
  taken_at: '2026-09-23T01:30:00Z',
  total_bytes: 3_400_000_000
})
const OLDER = snapshot({ id: 'k-older', taken_at: '2026-09-01T01:30:00Z', file_count: 900 })
const FOLDER = 'Restore/2026-09-25T09-00-00Z'

let fake: FakeApi

function restoreJob(overrides: Parameters<typeof job>[0] = {}) {
  return job({
    id: 'job-r',
    kind: 'restore',
    state: 'running',
    restore_folder: FOLDER,
    ...overrides
  })
}

async function mountPage() {
  const wrapper = mountWithHost(RestoreView, { props: { spaceId: SPACE_ID } })
  await flushPromises()
  return wrapper
}

async function toConfirm() {
  const wrapper = await mountPage()
  await wrapper.find('[data-testid="review"]').trigger('click')
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  fake = fakeApi()
  api.current = fake
  fake.listSpaces.mockResolvedValue([space({ role: 'viewer' })])
  fake.status.mockResolvedValue(status())
  fake.listSnapshots.mockResolvedValue([NEWER, OLDER])
  link.to.mockReset()
  link.to.mockReturnValue({ name: 'files-spaces-generic', params: { driveAliasAndItem: 'x' } })
})
afterEach(() => vi.useRealTimers())

describe('RestoreView picker', () => {
  // A viewer gets the whole flow: restore is a member capability (#7).
  it('lists the backups with human dates and sizes, newest chosen', async () => {
    const wrapper = await mountPage()

    expect(wrapper.find('h1').text()).toBe('Restore files in Family photos')
    const options = wrapper.findAll('[data-testid="snapshot-picker"] label')
    expect(options).toHaveLength(2)
    expect(options[0]!.text()).toContain('1,200 files, 3.4 GB')
    expect(options[0]!.text()).toMatch(/2026|Today|Yesterday/)
    expect(options[1]!.text()).toContain('900 files')
    const radios = wrapper.findAll<HTMLInputElement>('input[type="radio"]')
    expect(radios[0]!.element.checked).toBe(true)
    expect(radios[1]!.element.checked).toBe(false)
  })

  it('says so when there is nothing to restore yet', async () => {
    fake.listSnapshots.mockResolvedValue([])
    const wrapper = await mountPage()
    expect(wrapper.find('[data-testid="no-snapshots"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="review"]').exists()).toBe(false)
  })

  it('says a Space that is not set up has nothing to restore', async () => {
    fake.status.mockResolvedValue(status({ keys_configured: false }))
    const wrapper = await mountPage()
    expect(wrapper.find('[data-step="not_set_up"]').exists()).toBe(true)
    expect(fake.listSnapshots).not.toHaveBeenCalled()
  })

  it('shows a load failure through the shared panel', async () => {
    fake.listSnapshots.mockRejectedValue(new ApiError('target_unavailable', 'x', 409))
    const wrapper = await mountPage()
    expect(wrapper.find('[role="alert"]').text()).toContain(
      'The backup destination cannot be used right now'
    )
  })
})

describe('RestoreView confirmation', () => {
  it('states the size, that nothing is overwritten and the storage it takes', async () => {
    const wrapper = await toConfirm()

    expect(wrapper.find('[data-step="confirm"] h2').text()).toMatch(/^Restore the backup from /)
    expect(wrapper.find('[data-testid="confirm-size"]').text()).toBe('1,200 files, 3.4 GB')
    expect(wrapper.text()).toContain('Nothing in the space is changed or overwritten.')
    expect(wrapper.find('[data-testid="confirm-quota"]').text()).toBe(
      'The copy takes up 3.4 GB of this space’s storage.'
    )
    expect(fake.restore).not.toHaveBeenCalled()
  })

  it('goes back to the picker without sending anything', async () => {
    const wrapper = await toConfirm()
    await wrapper.find('[data-testid="choose-other"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-step="pick"]').exists()).toBe(true)
    expect(fake.restore).not.toHaveBeenCalled()
  })

  it('words a 409 as "another backup or restore", not "a backup"', async () => {
    fake.restore.mockRejectedValue(
      new ApiError('run_in_progress', 'x', 409, 'a run is already in progress')
    )
    const wrapper = await toConfirm()
    await wrapper.find('[data-testid="start-restore"]').trigger('click')
    await flushPromises()

    const error = wrapper.find('[data-testid="action-error"]')
    expect(error.text()).toContain('Another backup or restore is running for this space')
    expect(error.text()).toContain('Try again when it has finished.')
    expect(error.text()).not.toContain('A backup is already running')
  })

  it('re-lists with a notice when the chosen backup has gone', async () => {
    fake.restore.mockRejectedValue(new ApiError('not_found', 'x', 404))
    const wrapper = await toConfirm()
    fake.listSnapshots.mockResolvedValue([OLDER])
    await wrapper.find('[data-testid="start-restore"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="snapshot-gone"]').attributes('role')).toBe('alert')
    expect(wrapper.findAll('[data-testid="snapshot-picker"] label')).toHaveLength(1)
  })
})

describe('RestoreView following the run', () => {
  async function started() {
    vi.useFakeTimers()
    fake.restore.mockResolvedValue({ job_id: 'job-r', space_id: SPACE_ID, snapshot_id: NEWER.id })
    fake.run.mockResolvedValue(restoreJob())
    const wrapper = await toConfirm()
    await wrapper.find('[data-testid="start-restore"]').trigger('click')
    await flushPromises()
    return wrapper
  }

  it('sends the snapshot id and shows indeterminate progress with the folder', async () => {
    const wrapper = await started()

    expect(fake.restore).toHaveBeenCalledWith(SPACE_ID, NEWER.id)
    const running = wrapper.find('[data-step="running"]')
    expect(running.text()).toMatch(/^Restoring the backup from .*Started /)
    // A percentage would be a claim the server cannot back (8d decision 2).
    expect(running.find('progress').attributes('data-indeterminate')).toBe('true')
    expect(wrapper.find('[data-testid="running-folder"]').text()).toContain(FOLDER)
    wrapper.unmount()
  })

  it('polls the run it started and links the folder when it is done', async () => {
    const wrapper = await started()
    fake.run.mockResolvedValue(
      restoreJob({ state: 'succeeded', file_count: 1200, total_bytes: 3_400_000_000 })
    )

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    expect(fake.run).toHaveBeenLastCalledWith(SPACE_ID, 'job-r')
    const done = wrapper.find('[data-step="succeeded"]')
    expect(done.text()).toContain('Restore finished')
    expect(done.text()).toContain('1,200 files, 3.4 GB')
    const folder = wrapper.find('[data-testid="done-folder"]')
    expect(folder.find('[data-testid="folder-link"]').text()).toBe(FOLDER)
    expect(link.to).toHaveBeenCalledWith(SPACE_ID, FOLDER)

    // Nothing left to follow: no further reads.
    const reads = fake.run.mock.calls.length
    await vi.advanceTimersByTimeAsync(20_000)
    expect(fake.run.mock.calls.length).toBe(reads)
  })

  it('shows a failure with the partial folder and that backups are safe', async () => {
    const wrapper = await started()
    fake.run.mockResolvedValue(restoreJob({ state: 'failed', error: 'the restore run failed' }))

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    const failed = wrapper.find('[data-step="failed"]')
    expect(failed.text()).toContain('The restore did not finish')
    expect(failed.text()).toContain('the restore run failed')
    expect(failed.text()).toContain('Your backups are unaffected.')
    expect(wrapper.find('[data-testid="partial-folder"]').text()).toContain(FOLDER)
  })

  it('names the folder as text when the host cannot link it', async () => {
    link.to.mockReturnValue(undefined)
    const wrapper = await started()
    const folder = wrapper.find('[data-testid="running-folder"]')
    expect(folder.find('[data-testid="folder-link"]').exists()).toBe(false)
    expect(folder.find('[data-testid="folder-path"]').text()).toBe(FOLDER)
    wrapper.unmount()
  })

  it('keeps following through a failed read and says so', async () => {
    const wrapper = await started()
    fake.run.mockRejectedValueOnce(new ApiError('offline', 'x'))

    await vi.advanceTimersByTimeAsync(5000)
    await flushPromises()

    expect(wrapper.find('[data-step="running"]').text()).toContain(
      'Could not refresh the status. Retrying…'
    )
    wrapper.unmount()
  })

  it('stops polling when the page is left', async () => {
    const wrapper = await started()
    const reads = fake.run.mock.calls.length
    wrapper.unmount()
    await vi.advanceTimersByTimeAsync(20_000)
    expect(fake.run.mock.calls.length).toBe(reads)
  })

  // A reload mid-restore must not offer a second full copy.
  it('follows a restore already running when the page opens', async () => {
    vi.useFakeTimers()
    fake.status.mockResolvedValue(status({ running: true, current_job: restoreJob() }))
    fake.run.mockResolvedValue(restoreJob())
    const wrapper = await mountPage()

    expect(wrapper.find('[data-step="running"]').text()).toMatch(
      /^A restore is running for this space\./
    )
    expect(fake.listSnapshots).not.toHaveBeenCalled()
    await vi.advanceTimersByTimeAsync(5000)
    expect(fake.run).toHaveBeenCalledWith(SPACE_ID, 'job-r')
    wrapper.unmount()
  })
})
