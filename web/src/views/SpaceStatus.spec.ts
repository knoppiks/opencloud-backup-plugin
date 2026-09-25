import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type BackupStatus } from '../api'
import { fakeApi, job, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import SpaceStatus from './SpaceStatus.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

let fake: FakeApi

/** given wires the three reads the board makes on load. */
function given(board: BackupStatus, role = 'editor') {
  fake.listSpaces.mockResolvedValue([space({ role })])
  fake.status.mockResolvedValue(board)
  fake.listRuns.mockResolvedValue([job()])
}

async function mountBoard() {
  const wrapper = mountWithHost(SpaceStatus, { props: { spaceId: SPACE_ID } })
  await flushPromises()
  return wrapper
}

beforeEach(() => {
  fake = fakeApi()
  api.current = fake
})
afterEach(() => vi.useRealTimers())

describe('SpaceStatus board states', () => {
  it('fresh: protected, last and next run, no warnings', async () => {
    given(status())
    const wrapper = await mountBoard()

    expect(wrapper.find('h1').text()).toBe('Family photos')
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Protected')
    expect(wrapper.find('[data-testid="last-success"]').text()).not.toBe('None yet')
    expect(wrapper.find('[data-testid="next-run"]').text()).not.toBe('')
    expect(wrapper.find('[data-testid="stale"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="last-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="running"]').exists()).toBe(false)
  })

  it('stale: says since when, as an alert', async () => {
    given(status({ stale: true, stale_since: '2026-09-01T01:30:00Z' }))
    const wrapper = await mountBoard()

    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Backups have stopped')
    const stale = wrapper.find('[data-testid="stale"]')
    expect(stale.attributes('role')).toBe('alert')
    expect(stale.text()).toMatch(/^No successful backup since .*2026/)
  })

  it('failed: shows the recorded reason and that older backups are safe', async () => {
    given(
      status({
        last_run: job({ state: 'failed', error: 'the backup target is unavailable' }),
        last_error: 'the backup target is unavailable'
      })
    )
    const wrapper = await mountBoard()

    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Last backup failed')
    expect(wrapper.find('[data-testid="state-advice"]').text()).toMatch(/still safe/)
    expect(wrapper.find('[data-testid="last-error"]').text()).toContain(
      'the backup target is unavailable'
    )
  })

  it('running: indeterminate progress, "Back up now" disabled', async () => {
    vi.useFakeTimers()
    given(status({ running: true, current_job: job({ state: 'running' }) }))
    const wrapper = await mountBoard()

    const running = wrapper.find('[data-testid="running"]')
    expect(running.text()).toMatch(/^A backup is running\. Started /)
    // A percentage would be a claim the server cannot back (8d decision 2).
    expect(running.find('progress').attributes('data-indeterminate')).toBe('true')
    expect(wrapper.find('[data-testid="run-now"]').attributes('disabled')).toBeDefined()
    wrapper.unmount()
  })

  it('names a running restore as a restore', async () => {
    vi.useFakeTimers()
    given(status({ running: true, current_job: job({ kind: 'restore', state: 'running' }) }))
    const wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="running"]').text()).toMatch(/^A restore is running\./)
    wrapper.unmount()
  })

  it('paused: says scheduled backups are off instead of a next run', async () => {
    given(status({ enabled: false }))
    const wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="next-run"]').text()).toBe('Scheduled backups are off')
  })

  it('not set up: no details and no run button', async () => {
    given(status({ configured: false, keys_configured: false }))
    const wrapper = await mountBoard()

    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Not backed up')
    expect(wrapper.find('dl').exists()).toBe(false)
    expect(wrapper.find('[data-testid="run-now"]').exists()).toBe(false)
  })
})

describe('SpaceStatus roles', () => {
  // The server refuses a viewer's run; the board does not offer one.
  it('offers a viewer neither "Back up now" nor a retention edit', async () => {
    given(status(), 'viewer')
    const wrapper = await mountBoard()

    expect(wrapper.find('[data-testid="run-now"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="retention-edit"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="retention-days"]').text()).toBe('90 days')
  })

  it('offers an editor both', async () => {
    given(status(), 'editor')
    const wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="run-now"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="retention-edit"]').exists()).toBe(true)
  })
})

describe('SpaceStatus setup entry', () => {
  const link = (wrapper: Awaited<ReturnType<typeof mountBoard>>) =>
    wrapper.find('[data-testid="setup-action"] a')

  it('offers an editor "Set up backup" for a Space nobody has set up', async () => {
    given(status({ configured: false, keys_configured: false, enabled: false }), 'editor')
    const wrapper = await mountBoard()
    expect(link(wrapper).text()).toBe('Set up backup')
    expect(JSON.parse(link(wrapper).attributes('data-to') as string)).toEqual({
      name: 'backup-vault-setup',
      params: { spaceId: SPACE_ID }
    })
  })

  it('offers a manager "Finish setup" when only the keys are missing', async () => {
    given(status({ keys_configured: false, enabled: false }), 'manager')
    const wrapper = await mountBoard()
    expect(link(wrapper).text()).toBe('Finish setup')
  })

  it('tells an editor a manager has to finish, without a link', async () => {
    given(status({ keys_configured: false, enabled: false }), 'editor')
    const wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="setup-action"]').text()).toBe(
      'A manager of this space has to finish setup.'
    )
    expect(link(wrapper).exists()).toBe(false)
  })

  it('offers a viewer nothing', async () => {
    given(status({ configured: false, keys_configured: false, enabled: false }), 'viewer')
    const wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="setup-action"]').exists()).toBe(false)
  })

  it('offers nothing for a Space that is set up and on', async () => {
    given(status(), 'manager')
    const wrapper = await mountBoard()
    expect(wrapper.find('[data-testid="setup-action"]').exists()).toBe(false)
  })
})

describe('SpaceStatus "Back up now"', () => {
  it('starts a run and follows it until it finishes', async () => {
    vi.useFakeTimers()
    given(status())
    const wrapper = await mountBoard()

    const runningStatus = status({
      running: true,
      current_job: job({ id: 'job-2', state: 'running' })
    })
    const doneStatus = status({ last_run: job({ id: 'job-2' }) })
    fake.runBackup.mockResolvedValue({ job_id: 'job-2', space_id: SPACE_ID, state: 'running' })
    fake.status
      .mockResolvedValueOnce(runningStatus)
      .mockResolvedValueOnce(runningStatus)
      .mockResolvedValue(doneStatus)
    fake.listRuns.mockResolvedValue([job({ id: 'job-2' }), job()])

    await wrapper.find('[data-testid="run-now"]').trigger('click')
    await flushPromises()
    expect(fake.runBackup).toHaveBeenCalledWith(SPACE_ID)
    expect(wrapper.find('[data-testid="running"]').exists()).toBe(true)

    await vi.advanceTimersByTimeAsync(5000) // still running
    expect(wrapper.find('[data-testid="running"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(5000) // finished
    expect(wrapper.find('[data-testid="running"]').exists()).toBe(false)
    expect(wrapper.findAll('li[data-kind]')).toHaveLength(2)

    // Nothing is running any more, so nothing is polled any more.
    const calls = fake.status.mock.calls.length
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fake.status.mock.calls.length).toBe(calls)
  })

  it('stops polling when the board is left', async () => {
    vi.useFakeTimers()
    given(status({ running: true, current_job: job({ state: 'running' }) }))
    const wrapper = await mountBoard()
    wrapper.unmount()

    const calls = fake.status.mock.calls.length
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fake.status.mock.calls.length).toBe(calls)
  })

  // Someone else, or the schedule, got there first: that is what the person
  // wanted, so it is shown as the running job, not as an error.
  it('treats "already running" as the run it asked for', async () => {
    vi.useFakeTimers()
    given(status())
    const wrapper = await mountBoard()

    fake.runBackup.mockRejectedValue(new ApiError('run_in_progress', 'x', 409))
    fake.status.mockResolvedValue(status({ running: true, current_job: job({ state: 'running' }) }))
    await wrapper.find('[data-testid="run-now"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="action-error"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="running"]').exists()).toBe(true)
    wrapper.unmount()
  })

  it('shows why a run could not start', async () => {
    given(status())
    const wrapper = await mountBoard()

    fake.runBackup.mockRejectedValue(new ApiError('target_unavailable', 'x', 409))
    await wrapper.find('[data-testid="run-now"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="action-error"]').text()).toContain(
      'The backup destination cannot be used right now'
    )
    expect(wrapper.find('[data-testid="run-now"]').attributes('disabled')).toBeUndefined()
  })

  it('keeps the last known state and says so when a refresh fails', async () => {
    vi.useFakeTimers()
    given(status({ running: true, current_job: job({ state: 'running' }) }))
    const wrapper = await mountBoard()

    fake.status.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await vi.advanceTimersByTimeAsync(5000)

    expect(wrapper.find('[data-testid="running"]').text()).toContain('Could not refresh')
    // And it keeps trying: a blip must not freeze the board on "running".
    fake.status.mockResolvedValue(status())
    fake.listRuns.mockResolvedValue([job()])
    await vi.advanceTimersByTimeAsync(5000)
    expect(wrapper.find('[data-testid="running"]').exists()).toBe(false)
  })
})

describe('SpaceStatus load failures', () => {
  it('shows a Space missing from the caller’s list as not available', async () => {
    fake.listSpaces.mockResolvedValue([space({ id: 'someone-else' })])
    fake.status.mockResolvedValue(status())
    fake.listRuns.mockResolvedValue([])
    const wrapper = await mountBoard()
    expect(wrapper.find('[role="alert"] h2').text()).toBe('This is not available')
  })

  it('shows a refused status read', async () => {
    fake.listSpaces.mockResolvedValue([space()])
    fake.status.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    fake.listRuns.mockResolvedValue([])
    const wrapper = await mountBoard()
    expect(wrapper.find('[role="alert"] h2').text()).toBe('You do not have access to this')
  })
})
