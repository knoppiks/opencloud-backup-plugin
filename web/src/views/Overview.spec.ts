import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { translations } from '../l10n/translations'
import { mountWithHost } from '../test/host'
import { fakeApi, space, status, type FakeApi } from '../test/fixtures'
import Overview from './Overview.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))
const gate = vi.hoisted(() => ({ admin: false }))
vi.mock('../composables/useIsAdmin', () => ({ useIsAdmin: () => gate.admin }))

let fake: FakeApi
beforeEach(() => {
  fake = fakeApi()
  api.current = fake
  gate.admin = false
  fake.listTargets.mockResolvedValue([{ id: 't', name: 'Buddy' }])
})

async function mountOverview(language?: string) {
  const wrapper = mountWithHost(Overview, language ? { language, translations } : {})
  await flushPromises()
  return wrapper
}

const rowsOf = (wrapper: Awaited<ReturnType<typeof mountOverview>>) =>
  wrapper.findAll('[data-testid="spaces"] tbody tr')

describe('Overview', () => {
  it('shows one row per Space, each with its own state', async () => {
    fake.listSpaces.mockResolvedValue([
      space({ id: 'a', name: 'Alice', type: 'personal' }),
      space({ id: 'b', name: 'Bakery' })
    ])
    fake.status.mockImplementation(async (id: string) =>
      id === 'a' ? status({ space_id: 'a' }) : status({ space_id: 'b', stale: true })
    )

    const wrapper = await mountOverview()
    const states = rowsOf(wrapper).map((r) =>
      r.find('[data-testid="state-label"]').attributes('data-state')
    )

    expect(states).toEqual(['active', 'stale'])
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
    expect(wrapper.findAll('[data-testid="space-link"]').map((a) => a.text())).toEqual([
      'Me',
      'Attic',
      'Zoo'
    ])
  })

  // One unreadable Space is not a reason to hide the others.
  it('shows a failed status in its own row only', async () => {
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
    const [fine, broken] = rowsOf(wrapper)

    expect(fine!.find('[data-testid="state-label"]').attributes('data-state')).toBe('active')
    expect(broken!.find('[data-testid="state-label"]').exists()).toBe(false)
    expect(broken!.find('[role="alert"]').text()).toBe(
      'OpenCloud did not answer the backup service'
    )
  })

  it('links each Space to its status board', async () => {
    fake.listSpaces.mockResolvedValue([space({ id: 'x$1!1', name: 'X' })])
    fake.status.mockResolvedValue(status())

    const wrapper = await mountOverview()
    const to = JSON.parse(wrapper.find('[data-testid="space-link"]').attributes('data-to')!)
    expect(to).toEqual({ name: 'backup-vault-space', params: { spaceId: 'x$1!1' } })
  })

  it('offers setup in the row of a Space an editor can set up', async () => {
    fake.listSpaces.mockResolvedValue([space({ id: 's', role: 'editor' })])
    fake.status.mockResolvedValue(
      status({ configured: false, keys_configured: false, enabled: false })
    )

    const link = (await mountOverview()).find('[data-testid="setup-action"]')
    expect(link.text()).toBe('Set up backup')
    expect(JSON.parse(link.attributes('data-to')!)).toEqual({
      name: 'backup-vault-setup',
      params: { spaceId: 's' }
    })
  })

  it('sums up the Spaces in the footer', async () => {
    fake.listSpaces.mockResolvedValue([space({ id: 'a' }), space({ id: 'b' })])
    fake.status.mockImplementation(async (id: string) =>
      id === 'a' ? status() : status({ stale: true })
    )

    const summary = (await mountOverview()).find('[data-testid="summary"]').text()
    expect(summary).toContain('Spaces: 2 · Protected: 1')
    expect(summary).toContain('Need attention: 1')
  })

  it('tells a member without a destination to ask, and offers nothing', async () => {
    fake.listSpaces.mockResolvedValue([space()])
    fake.listTargets.mockResolvedValue([])
    fake.status.mockResolvedValue(status({ configured: false, keys_configured: false }))

    const notice = (await mountOverview()).find('[data-testid="no-targets"]')
    expect(notice.attributes('data-tone')).toBe('warning')
    expect(notice.text()).toContain('Ask your administrator')
    expect(notice.find('[data-testid="no-targets-admin"]').exists()).toBe(false)
  })

  // An admin is the one who can fix it, so "ask your administrator" is wrong.
  it('offers an admin without a destination the way to add or share one', async () => {
    gate.admin = true
    fake.listSpaces.mockResolvedValue([space()])
    fake.listTargets.mockResolvedValue([])
    fake.status.mockResolvedValue(status())

    const notice = (await mountOverview()).find('[data-testid="no-targets"]')
    expect(notice.text()).not.toContain('Ask your administrator')
    const action = notice.find('[data-testid="no-targets-admin"]')
    expect(JSON.parse(action.attributes('data-to')!)).toEqual({
      name: 'backup-vault-admin-targets'
    })
  })

  it('says so when there are no Spaces at all', async () => {
    fake.listSpaces.mockResolvedValue([])
    const wrapper = await mountOverview()
    expect(wrapper.find('[data-testid="empty-state"]').text()).toBe(
      'You do not have any spaces to back up.'
    )
    expect(wrapper.find('[data-testid="spaces"]').exists()).toBe(false)
  })

  it('shows the load failure, with a retry when retrying can help', async () => {
    fake.listSpaces.mockRejectedValueOnce(new ApiError('offline', 'x'))

    const wrapper = await mountOverview()
    expect(wrapper.find('[role="alert"] h2').text()).toBe('The backup service cannot be reached')

    fake.listSpaces.mockResolvedValue([space()])
    fake.status.mockResolvedValue(status())
    await wrapper.find('[role="alert"] button').trigger('click')
    await flushPromises()

    expect(rowsOf(wrapper)).toHaveLength(1)
  })

  it('offers no retry for a failure that cannot change', async () => {
    fake.listSpaces.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    const wrapper = await mountOverview()
    expect(wrapper.find('[role="alert"] button').exists()).toBe(false)
  })

  // Destination management is offered in the left navigation (navigation.ts),
  // where an admin finds it even when the Space list fails to load.
  it('is titled "Spaces", as its navigation item, without a link to itself', async () => {
    fake.listSpaces.mockResolvedValue([])
    const wrapper = await mountOverview()
    expect(wrapper.find('h1').text()).toBe('Spaces')
    expect(wrapper.find('[data-testid="breadcrumb"] [aria-current="page"]').text()).toBe('Spaces')
    expect(wrapper.find('[data-testid="breadcrumb"] a').exists()).toBe(false)
  })

  // The catalogue is wired, not merely present: a German session renders German.
  it('renders in German', async () => {
    fake.listSpaces.mockResolvedValue([space()])
    fake.status.mockResolvedValue(status())
    const wrapper = await mountOverview('de')
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe(translations.de!['Protected'])
  })
})
