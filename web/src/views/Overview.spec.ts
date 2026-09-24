import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { mountWithHost } from '../test/host'
import { fakeApi, space, status, type FakeApi } from '../test/fixtures'
import Overview from './Overview.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

let fake: FakeApi
beforeEach(() => {
  fake = fakeApi()
  api.current = fake
  fake.listTargets.mockResolvedValue([{ id: 't', name: 'Buddy' }])
})

async function mountOverview() {
  const wrapper = mountWithHost(Overview)
  await flushPromises()
  return wrapper
}

describe('Overview', () => {
  it('shows one card per Space, each with its own state', async () => {
    fake.listSpaces.mockResolvedValue([
      space({ id: 'a', name: 'Alice', type: 'personal' }),
      space({ id: 'b', name: 'Bakery' })
    ])
    fake.status.mockImplementation(async (id: string) =>
      id === 'a' ? status({ space_id: 'a' }) : status({ space_id: 'b', stale: true })
    )

    const wrapper = await mountOverview()
    const cards = wrapper.findAll('article')

    expect(cards.map((c) => c.attributes('data-state'))).toEqual(['active', 'stale'])
    expect(fake.status).toHaveBeenCalledTimes(2)
  })

  it('lists the personal Space first, then shared ones by name', async () => {
    fake.listSpaces.mockResolvedValue([
      space({ id: 'z', name: 'Zoo' }),
      space({ id: 'a', name: 'Attic' }),
      space({ id: 'me', name: 'Me', type: 'personal' })
    ])
    fake.status.mockResolvedValue(status())

    const wrapper = await mountOverview()
    expect(wrapper.findAll('article header a').map((a) => a.text())).toEqual(['Me', 'Attic', 'Zoo'])
  })

  // One unreadable Space is not a reason to hide the others.
  it('shows a failed status on its own card only', async () => {
    fake.listSpaces.mockResolvedValue([
      space({ id: 'ok', name: 'Attic' }),
      space({ id: 'bad', name: 'Broken' })
    ])
    fake.status.mockImplementation(async (id: string) => {
      if (id === 'bad') {
        throw new ApiError('upstream_error', 'x', 502)
      }
      return status({ space_id: id })
    })

    const wrapper = await mountOverview()
    const [fine, broken] = wrapper.findAll('article')

    expect(fine!.attributes('data-state')).toBe('active')
    expect(broken!.attributes('data-state')).toBe('error')
    expect(broken!.find('[role="alert"]').text()).toBe(
      'OpenCloud did not answer the backup service'
    )
  })

  it('links each card to its status board', async () => {
    fake.listSpaces.mockResolvedValue([space({ id: 'x$1!1', name: 'X' })])
    fake.status.mockResolvedValue(status())

    const wrapper = await mountOverview()
    const to = JSON.parse(wrapper.find('article header a').attributes('data-to')!)
    expect(to).toEqual({ name: 'backup-vault-space', params: { spaceId: 'x$1!1' } })
  })

  it('says so when no destination has been granted', async () => {
    fake.listSpaces.mockResolvedValue([space()])
    fake.listTargets.mockResolvedValue([])
    fake.status.mockResolvedValue(status({ configured: false, keys_configured: false }))

    const wrapper = await mountOverview()
    expect(wrapper.find('[data-testid="no-targets"]').exists()).toBe(true)
  })

  it('shows the load failure, with a retry when retrying can help', async () => {
    fake.listSpaces.mockRejectedValueOnce(new ApiError('offline', 'x'))

    const wrapper = await mountOverview()
    expect(wrapper.find('[role="alert"] h2').text()).toBe('The backup service cannot be reached')

    fake.listSpaces.mockResolvedValue([space()])
    fake.status.mockResolvedValue(status())
    await wrapper.find('[role="alert"] button').trigger('click')
    await flushPromises()

    expect(wrapper.findAll('article')).toHaveLength(1)
  })

  it('offers no retry for a failure that cannot change', async () => {
    fake.listSpaces.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    const wrapper = await mountOverview()
    expect(wrapper.find('[role="alert"] button').exists()).toBe(false)
  })
})
