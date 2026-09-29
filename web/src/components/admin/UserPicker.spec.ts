import { flushPromises } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { mountWithHost } from '../../test/host'
import { fakeDirectory, type FakeDirectory } from '../../test/fixtures'
import UserPicker from './UserPicker.vue'

const host = vi.hoisted(() => ({ directory: undefined as unknown, minLength: 3 }))
vi.mock('../../composables/useUserDirectory', () => ({
  useUserDirectory: () => host.directory,
  useUserSearchMinLength: () => host.minLength
}))

let directory: FakeDirectory
beforeEach(() => {
  // Timers only: a faked Date makes Vue drop clicks as older than their listener.
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
  directory = fakeDirectory()
  host.directory = directory
  host.minLength = 3
})
afterEach(() => {
  vi.useRealTimers()
})

function mountPicker(excludeIds: string[] = []) {
  return mountWithHost(UserPicker, { props: { excludeIds } })
}

async function typeAndWait(wrapper: ReturnType<typeof mountPicker>, value: string) {
  await wrapper.find('input[data-testid="user-search"]').setValue(value)
  await vi.advanceTimersByTimeAsync(500)
  await flushPromises()
}

describe('UserPicker', () => {
  it('waits for the pause after typing, then searches once', async () => {
    directory.search.mockResolvedValue([{ id: 'u-1', displayName: 'Alice' }])
    const wrapper = mountPicker()
    const input = wrapper.find('input[data-testid="user-search"]')

    await input.setValue('ali')
    await vi.advanceTimersByTimeAsync(300)
    await input.setValue('alic')
    await vi.advanceTimersByTimeAsync(300)
    expect(directory.search).not.toHaveBeenCalled()

    await vi.advanceTimersByTimeAsync(200)
    await flushPromises()

    expect(directory.search).toHaveBeenCalledTimes(1)
    expect(directory.search).toHaveBeenCalledWith('alic')
    expect(wrapper.find('[data-testid="matches"]').text()).toContain('Alice')
  })

  it('does not search below the minimum length the server asks for', async () => {
    const wrapper = mountPicker()
    await typeAndWait(wrapper, 'al')
    expect(directory.search).not.toHaveBeenCalled()
    expect(wrapper.text()).toContain('Type at least 3 characters')
  })

  it('emits the picked user', async () => {
    const alice = { id: 'u-1', displayName: 'Alice', mail: 'alice@example.org' }
    directory.search.mockResolvedValue([alice])
    const wrapper = mountPicker()
    await typeAndWait(wrapper, 'alice')

    await wrapper.find('[data-testid="add-user"]').trigger('click')

    expect(wrapper.emitted('pick')).toEqual([[alice]])
  })

  it('marks people already listed instead of offering them again', async () => {
    directory.search.mockResolvedValue([{ id: 'u-1', displayName: 'Alice' }])
    const wrapper = mountPicker(['u-1'])
    await typeAndWait(wrapper, 'alice')

    expect(wrapper.find('[data-testid="add-user"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="already"]').exists()).toBe(true)
  })

  it('says when nobody matches', async () => {
    const wrapper = mountPicker()
    await typeAndWait(wrapper, 'zzz')
    expect(wrapper.find('[data-testid="no-matches"]').exists()).toBe(true)
  })

  it('says when the search failed', async () => {
    directory.search.mockRejectedValue(new Error('down'))
    const wrapper = mountPicker()
    await typeAndWait(wrapper, 'alice')
    expect(wrapper.find('[data-testid="search-failed"]').exists()).toBe(true)
  })

  // The list belongs to what is in the box, not to whichever answer was slowest.
  it('drops an answer that arrives after the term changed', async () => {
    let answerFirst: (users: unknown[]) => void = () => {}
    directory.search
      .mockImplementationOnce(() => new Promise((resolve) => (answerFirst = resolve)))
      .mockResolvedValueOnce([{ id: 'u-2', displayName: 'Bob' }])
    const wrapper = mountPicker()

    await typeAndWait(wrapper, 'ali')
    await typeAndWait(wrapper, 'bob')
    answerFirst([{ id: 'u-1', displayName: 'Alice' }])
    await flushPromises()

    const text = wrapper.find('[data-testid="matches"]').text()
    expect(text).toContain('Bob')
    expect(text).not.toContain('Alice')
  })
})
