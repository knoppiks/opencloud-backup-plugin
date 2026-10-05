import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../api'
import { mountWithHost } from '../../test/host'
import { modalsDouble, type ModalsDouble } from '../../test/modals'
import { fakeAdminApi, TARGET_ID, type FakeAdminApi } from '../../test/fixtures'
import DeleteTarget from './DeleteTarget.vue'

const host = vi.hoisted(() => ({
  api: undefined as unknown,
  modals: undefined as unknown as ModalsDouble
}))
vi.mock('../../composables/useAdminApi', () => ({ useAdminApi: () => host.api }))
vi.mock('@opencloud-eu/web-pkg', () => ({ useModals: () => host.modals.store }))

let api: FakeAdminApi
beforeEach(() => {
  api = fakeAdminApi()
  host.api = api
  host.modals = modalsDouble()
})

function mountDelete() {
  return mountWithHost(DeleteTarget, { props: { targetId: TARGET_ID, targetName: 'Buddy' } })
}

/** confirmDelete asks, then presses the dialog's confirm button. */
async function confirmDelete() {
  const wrapper = mountDelete()
  await wrapper.find('[data-testid="delete"]').trigger('click')
  await host.modals.last().onConfirm!(undefined)
  await flushPromises()
  return wrapper
}

describe('DeleteTarget', () => {
  it('asks in the host’s dialog and deletes nothing until confirmed', async () => {
    const wrapper = mountDelete()
    expect(host.modals.dispatched).toHaveLength(0)

    await wrapper.find('[data-testid="delete"]').trigger('click')

    expect(host.modals.dispatched).toHaveLength(1)
    const modal = host.modals.last()
    expect(modal.title).toBe('Delete “Buddy”?')
    expect(modal.message).toContain('The backups already stored in the bucket are not deleted.')
    expect(modal.confirmText).toBe('Delete')
    expect(api.deleteTarget).not.toHaveBeenCalled()
  })

  it('deletes once confirmed and says so', async () => {
    api.deleteTarget.mockResolvedValue(undefined)
    const wrapper = await confirmDelete()
    expect(api.deleteTarget).toHaveBeenCalledWith(TARGET_ID)
    expect(wrapper.emitted('deleted')).toHaveLength(1)
  })

  it('never throws into the dialog, so it closes whatever the answer', async () => {
    api.deleteTarget.mockRejectedValue(new ApiError('unavailable', 'x', 503))
    const wrapper = mountDelete()
    await wrapper.find('[data-testid="delete"]').trigger('click')
    await expect(host.modals.last().onConfirm!(undefined)).resolves.toBeUndefined()
  })

  it('shows the count of Spaces in the way, and what to do', async () => {
    api.deleteTarget.mockRejectedValue(
      new ApiError(
        'target_in_use',
        'x',
        409,
        '2 space(s) still back up to this target; point them elsewhere first'
      )
    )
    const wrapper = await confirmDelete()

    const text = wrapper.find('[data-testid="in-use"]').text()
    expect(text).toContain('Spaces still backing up to this destination: 2.')
    expect(text).toContain('switched to another destination first')
    expect(wrapper.emitted('deleted')).toBeUndefined()
  })

  it('still explains a refusal whose message carries no count', async () => {
    api.deleteTarget.mockRejectedValue(new ApiError('target_in_use', 'x', 409))
    const wrapper = await confirmDelete()
    expect(wrapper.find('[data-testid="in-use"]').text()).toContain('Some spaces still back up')
  })

  it('treats a target that is already gone as deleted', async () => {
    api.deleteTarget.mockRejectedValue(new ApiError('not_found', 'x', 404))
    const wrapper = await confirmDelete()
    expect(wrapper.emitted('deleted')).toHaveLength(1)
  })

  it('shows any other failure in admin wording', async () => {
    api.deleteTarget.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    const wrapper = await confirmDelete()
    expect(wrapper.find('[data-testid="action-error"]').text()).toContain(
      'Only administrators can manage backup destinations'
    )
    expect(wrapper.emitted('deleted')).toBeUndefined()
  })

  it('clears an earlier failure when asked again', async () => {
    api.deleteTarget.mockRejectedValue(new ApiError('forbidden', 'x', 403))
    const wrapper = await confirmDelete()
    await wrapper.find('[data-testid="delete"]').trigger('click')
    expect(wrapper.find('[data-testid="action-error"]').exists()).toBe(false)
  })
})
