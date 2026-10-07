// The three admin pages: the client gate, the server's 403, and the moves
// between them. What each section does is covered by its own component spec.

import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { mountWithHost } from '../test/host'
import { modalsDouble, type ModalsDouble } from '../test/modals'
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
  router: { replace: undefined as unknown as ReturnType<typeof vi.fn> },
  modals: undefined as unknown as ModalsDouble
}))
vi.mock('../composables/useAdminApi', () => ({ useAdminApi: () => host.api }))
vi.mock('../composables/useIsAdmin', () => ({ useIsAdmin: () => host.admin }))
vi.mock('../composables/useUserDirectory', () => ({
  useUserDirectory: () => fakeDirectory(),
  useUserSearchMinLength: () => 1
}))
vi.mock('@opencloud-eu/web-pkg', () => ({
  useRouter: () => host.router,
  useModals: () => host.modals.store
}))

let api: FakeAdminApi
beforeEach(() => {
  api = fakeAdminApi()
  host.api = api
  host.admin = true
  host.router.replace = vi.fn(async () => undefined)
  host.modals = modalsDouble()
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
    const items = wrapper.findAll('[data-testid="destinations"] tbody tr')

    expect(items).toHaveLength(2)
    expect(items[0]!.find('.oc-table-data-cell-location').text()).toBe(
      'buddy.example.org:3900 / backups / family/'
    )
    expect(items[1]!.find('.oc-table-data-cell-keyPairs').text()).toBe('Separate maintenance keys')
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

  // compatibility-policy.md §3: warn, never block. The list stays usable.
  it('warns above the list when OpenCloud runs an untested version', async () => {
    api.listTargets.mockResolvedValue([adminTarget()])
    api.openCloudVersion.mockResolvedValue({
      known: true,
      version: '9.0.0',
      edition: 'rolling',
      in_window: false,
      supported: 'Rolling 7.3.0 to 8.1.0'
    })
    const wrapper = await mounted(AdminTargets)

    const notice = wrapper.find('[data-testid="opencloud-untested"]')
    expect(notice.exists()).toBe(true)
    expect(notice.text()).toContain('OpenCloud 9.0.0')
    expect(wrapper.findAll('[data-testid="destinations"] tbody tr')).toHaveLength(1)
  })

  it.each([
    [
      'inside the window',
      () => Promise.resolve({ known: true, version: '8.1.0', in_window: true, supported: 'x' })
    ],
    ['the version route failing', () => Promise.reject(new ApiError('unavailable', 'x', 503))]
  ])('says nothing about OpenCloud with %s', async (_name, answer) => {
    api.listTargets.mockResolvedValue([adminTarget()])
    api.openCloudVersion.mockImplementation(answer)
    const wrapper = await mounted(AdminTargets)

    expect(wrapper.find('[data-testid="opencloud-untested"]').exists()).toBe(false)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
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
    // One section each: connection, keys, who may use it, deletion.
    expect(wrapper.findAll('h2').map((h) => h.text())).toEqual([
      'Connection',
      'Access keys',
      'Who can back up here',
      'Danger zone'
    ])
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
    await host.modals.last().onConfirm!(undefined)
    await flushPromises()

    expect(host.router.replace).toHaveBeenCalledWith({ name: 'backup-vault-admin-targets' })
  })
})
