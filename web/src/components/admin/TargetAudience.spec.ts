import { flushPromises, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type Grant } from '../../api'
import { mountWithHost } from '../../test/host'
import {
  fakeAdminApi,
  fakeDirectory,
  TARGET_ID,
  type FakeAdminApi,
  type FakeDirectory
} from '../../test/fixtures'
import TargetAudience from './TargetAudience.vue'

const host = vi.hoisted(() => ({ api: undefined as unknown, directory: undefined as unknown }))
vi.mock('../../composables/useAdminApi', () => ({ useAdminApi: () => host.api }))
vi.mock('../../composables/useUserDirectory', () => ({
  useUserDirectory: () => host.directory,
  useUserSearchMinLength: () => 1
}))

const SPACE_GRANT: Grant = { scope: 'space', space_id: 'storage$space!opaque' }

let api: FakeAdminApi
let directory: FakeDirectory
beforeEach(() => {
  api = fakeAdminApi()
  directory = fakeDirectory()
  host.api = api
  host.directory = directory
  // replaceGrants answers with what it was sent, as the server does.
  api.replaceGrants.mockImplementation(async (_id: string, grants: Grant[]) => grants)
})

async function mountAudience(grants: Grant[]) {
  api.grants.mockResolvedValue(grants)
  const wrapper = mountWithHost(TargetAudience, { props: { targetId: TARGET_ID } })
  await flushPromises()
  return wrapper
}

function saveButton(wrapper: VueWrapper) {
  return wrapper.find('[data-testid="save-audience"]')
}

async function save(wrapper: VueWrapper) {
  await saveButton(wrapper).trigger('click')
  await flushPromises()
}

function sentGrants(): Grant[] {
  return api.replaceGrants.mock.calls[0]![1] as Grant[]
}

describe('TargetAudience', () => {
  it('names each granted person by asking the directory', async () => {
    directory.lookup.mockImplementation(async (id: string) => ({
      id,
      displayName: id === 'u-1' ? 'Alice' : 'Bob'
    }))
    const wrapper = await mountAudience([
      { scope: 'user', user_id: 'u-1' },
      { scope: 'user', user_id: 'u-2' }
    ])

    expect(api.grants).toHaveBeenCalledWith(TARGET_ID)
    expect(wrapper.findAll('[data-testid="people"] li').map((li) => li.text())).toEqual([
      expect.stringContaining('Alice'),
      expect.stringContaining('Bob')
    ])
    expect(
      (wrapper.find('[data-testid="mode-people"] input').element as HTMLInputElement).checked
    ).toBe(true)
    // Shown with their avatar, as the host's share dialogs show people.
    expect(wrapper.find('[data-user-id="u-1"] [data-avatar]').attributes('data-avatar')).toContain(
      'Alice'
    )
  })

  // A deleted account must not leave a grant nobody can take away.
  it('shows a person it cannot name by id, and lets them be removed', async () => {
    const wrapper = await mountAudience([{ scope: 'user', user_id: 'gone-user' }])
    const row = wrapper.find('[data-user-id="gone-user"]')

    expect(row.text()).toContain('Unknown user')
    expect(row.find('[data-testid="unknown-id"]').text()).toContain('gone-user')

    await row.find('[data-testid="remove-user"]').trigger('click')
    await save(wrapper)
    expect(sentGrants()).toEqual([])
  })

  it('offers no save until something changed', async () => {
    const wrapper = await mountAudience([{ scope: 'all_users' }])
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()

    await wrapper.find('[data-testid="mode-people"] input').trigger('change')
    expect(saveButton(wrapper).attributes('disabled')).toBeUndefined()
  })

  it('adds a picked person and saves them by graph id', async () => {
    directory.search.mockResolvedValue([{ id: 'u-9', displayName: 'Carol' }])
    const wrapper = await mountAudience([])
    expect(wrapper.find('[data-testid="nobody"]').exists()).toBe(true)

    // Timers only: a faked Date makes Vue drop the click that follows as
    // older than the listener it would reach.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    try {
      await wrapper.find('input[data-testid="user-search"]').setValue('car')
      await vi.advanceTimersByTimeAsync(500)
    } finally {
      vi.useRealTimers()
    }
    await flushPromises()
    await wrapper.find('[data-testid="add-user"]').trigger('click')

    expect(wrapper.find('[data-testid="people"]').text()).toContain('Carol')
    expect(wrapper.find('[data-testid="nobody"]').exists()).toBe(false)

    await save(wrapper)
    expect(sentGrants()).toEqual([{ scope: 'user', user_id: 'u-9' }])
    expect(wrapper.find('[data-testid="audience-saved"]').exists()).toBe(true)
    expect(saveButton(wrapper).attributes('disabled')).toBeDefined()
  })

  it('saves everyone as one all_users grant', async () => {
    const wrapper = await mountAudience([])
    await wrapper.find('[data-testid="mode-everyone"] input').trigger('change')
    await save(wrapper)
    expect(sentGrants()).toEqual([{ scope: 'all_users' }])
  })

  it('says that choosing everyone drops the listed people, then drops them', async () => {
    const wrapper = await mountAudience([{ scope: 'user', user_id: 'u-1' }])
    expect(wrapper.find('[data-testid="redundant-people"]').exists()).toBe(false)

    await wrapper.find('[data-testid="mode-everyone"] input').trigger('change')
    expect(wrapper.find('[data-testid="redundant-people"]').exists()).toBe(true)

    await save(wrapper)
    expect(sentGrants()).toEqual([{ scope: 'all_users' }])
  })

  // PUT replaces the whole list: a Space grant left out would be revoked.
  it('shows Space grants read-only and sends them back on every save', async () => {
    const wrapper = await mountAudience([SPACE_GRANT, { scope: 'user', user_id: 'u-1' }])
    expect(wrapper.find('[data-testid="kept-grants"]').text()).toContain('storage$space!opaque')
    expect(wrapper.find('[data-testid="nobody"]').exists()).toBe(false)

    await wrapper.find('[data-user-id="u-1"] [data-testid="remove-user"]').trigger('click')
    await save(wrapper)

    expect(sentGrants()).toEqual([SPACE_GRANT])
  })

  it('shows a failed save in admin wording and keeps the edit', async () => {
    api.replaceGrants.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    const wrapper = await mountAudience([])
    await wrapper.find('[data-testid="mode-everyone"] input').trigger('change')

    await save(wrapper)

    expect(wrapper.find('[data-testid="action-error"]').text()).toContain(
      'Only administrators can manage backup destinations'
    )
    expect(
      (wrapper.find('[data-testid="mode-everyone"] input').element as HTMLInputElement).checked
    ).toBe(true)
  })

  it('shows a failed load with a retry when retrying can help', async () => {
    api.grants.mockRejectedValueOnce(new ApiError('offline', 'x'))
    const wrapper = mountWithHost(TargetAudience, { props: { targetId: TARGET_ID } })
    await flushPromises()
    expect(wrapper.find('[role="alert"] h2').text()).toBe('The backup service cannot be reached')

    api.grants.mockResolvedValue([{ scope: 'all_users' }])
    await wrapper.find('[role="alert"] button').trigger('click')
    await flushPromises()
    expect(
      (wrapper.find('[data-testid="mode-everyone"] input').element as HTMLInputElement).checked
    ).toBe(true)
  })
})
