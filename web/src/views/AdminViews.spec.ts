// The three admin pages: the client gate, the server's 403, and the moves
// between them. What each section does is covered by its own component spec.

import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { mountWithHost } from '../test/host'
import {
  adminTarget,
  fakeAdminApi,
  fakeDirectory,
  TARGET_ID,
  type FakeAdminApi
} from '../test/fixtures'
import AdminTargetEdit from './AdminTargetEdit.vue'
import AdminTargetNew from './AdminTargetNew.vue'
import AdminTargets from './AdminTargets.vue'

const host = vi.hoisted(() => ({
  api: undefined as unknown,
  admin: true,
  router: { replace: undefined as unknown as ReturnType<typeof vi.fn> }
}))
vi.mock('../composables/useAdminApi', () => ({ useAdminApi: () => host.api }))
vi.mock('../composables/useIsAdmin', () => ({ useIsAdmin: () => host.admin }))
vi.mock('../composables/useUserDirectory', () => ({
  useUserDirectory: () => fakeDirectory(),
  useUserSearchMinLength: () => 1
}))
vi.mock('@opencloud-eu/web-pkg', () => ({ useRouter: () => host.router }))

let api: FakeAdminApi
beforeEach(() => {
  api = fakeAdminApi()
  host.api = api
  host.admin = true
  host.router.replace = vi.fn(async () => undefined)
})

async function mounted<T>(component: T, props: Record<string, unknown> = {}) {
  const wrapper = mountWithHost(component as never, { props } as never)
  await flushPromises()
  return wrapper
}

function apiCalls(): number {
  return Object.values(api).reduce((sum, fn) => sum + fn.mock.calls.length, 0)
}

describe('admin gate', () => {
  it.each([
    ['list', AdminTargets, {}],
    ['new', AdminTargetNew, {}],
    ['edit', AdminTargetEdit, { targetId: TARGET_ID }]
  ])('%s: shows a non-admin a notice and calls nothing', async (_name, view, props) => {
    host.admin = false
    const wrapper = await mounted(view, props)

    expect(wrapper.find('[data-testid="not-admin"]').exists()).toBe(true)
    expect(wrapper.find('form').exists()).toBe(false)
    expect(apiCalls()).toBe(0)
  })

  // The client gate only decides what is offered. The server decides.
  it('reads a 403 as "not an administrator", with no retry', async () => {
    api.listTargets.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    const wrapper = await mounted(AdminTargets)

    expect(wrapper.find('[role="alert"] h2').text()).toBe(
      'Only administrators can manage backup destinations'
    )
    expect(wrapper.find('[role="alert"] button').exists()).toBe(false)
  })
})

describe('AdminTargets', () => {
  it('lists each destination with where it points and links to it', async () => {
    api.listTargets.mockResolvedValue([
      adminTarget({ prefix: 'family/' }),
      adminTarget({ id: 't-2', name: 'Offsite', maintenance_configured: true })
    ])
    const wrapper = await mounted(AdminTargets)
    const items = wrapper.findAll('[data-testid="destinations"] li')

    expect(items).toHaveLength(2)
    expect(items[0]!.find('[data-testid="location"]').text()).toBe(
      'buddy.example.org:3900 / backups / family/'
    )
    expect(items[1]!.find('[data-testid="key-pairs"]').text()).toBe('Separate maintenance keys')
    expect(JSON.parse(items[0]!.find('a').attributes('data-to')!)).toEqual({
      name: 'backup-vault-admin-target',
      params: { targetId: TARGET_ID }
    })
  })

  it('says what an empty list means, and offers to add one', async () => {
    api.listTargets.mockResolvedValue([])
    const wrapper = await mounted(AdminTargets)
    expect(wrapper.find('[data-testid="no-destinations"]').exists()).toBe(true)
    expect(JSON.parse(wrapper.find('[data-testid="add"]').attributes('data-to')!)).toEqual({
      name: 'backup-vault-admin-target-new'
    })
  })
})

describe('AdminTargetNew', () => {
  it('moves to the new destination’s page once it is created', async () => {
    api.createTarget.mockResolvedValue(adminTarget({ id: 'created-id' }))
    const wrapper = await mounted(AdminTargetNew)
    await wrapper.find('input[data-testid="name"]').setValue('Buddy')
    await wrapper.find('input[data-testid="endpoint"]').setValue('buddy:3900')
    await wrapper.find('input[data-testid="bucket"]').setValue('backups')
    await wrapper.find('input[data-testid="access-key-id"]').setValue('backup-id')
    await wrapper.find('input[data-testid="secret-access-key"]').setValue('secret-marker')

    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(host.router.replace).toHaveBeenCalledWith({
      name: 'backup-vault-admin-target',
      params: { targetId: 'created-id' }
    })
  })
})

describe('AdminTargetEdit', () => {
  beforeEach(() => {
    api.grants.mockResolvedValue([])
  })

  it('shows settings, audience and delete for the destination', async () => {
    api.target.mockResolvedValue(adminTarget())
    const wrapper = await mounted(AdminTargetEdit, { targetId: TARGET_ID })

    expect(api.target).toHaveBeenCalledWith(TARGET_ID)
    expect(wrapper.find('h1').text()).toBe('Buddy')
    expect(wrapper.find('[data-testid="settings"] form').exists()).toBe(true)
    expect(wrapper.find('[data-testid="audience"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="delete-target"]').exists()).toBe(true)
    // Write-only: an edit page has no key field until the admin asks for one.
    expect(wrapper.findAll('input[type="password"]')).toHaveLength(0)
  })

  it('says a destination that does not exist does not exist', async () => {
    api.target.mockRejectedValue(new ApiError('not_found', 'x', 404, 'no such target'))
    const wrapper = await mounted(AdminTargetEdit, { targetId: 'nope' })
    expect(wrapper.find('[role="alert"] h2').text()).toBe('This backup destination does not exist')
  })

  it('renames the heading once a new name is saved', async () => {
    api.target.mockResolvedValue(adminTarget())
    api.updateTarget.mockResolvedValue(adminTarget({ name: 'Renamed' }))
    const wrapper = await mounted(AdminTargetEdit, { targetId: TARGET_ID })

    await wrapper.find('input[data-testid="name"]').setValue('Renamed')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('h1').text()).toBe('Renamed')
  })

  it('goes back to the list once deleted', async () => {
    api.target.mockResolvedValue(adminTarget())
    api.deleteTarget.mockResolvedValue(undefined)
    const wrapper = await mounted(AdminTargetEdit, { targetId: TARGET_ID })

    await wrapper.find('[data-testid="delete"]').trigger('click')
    await wrapper.find('[data-testid="confirm"]').trigger('click')
    await flushPromises()

    expect(host.router.replace).toHaveBeenCalledWith({ name: 'backup-vault-admin-targets' })
  })
})
