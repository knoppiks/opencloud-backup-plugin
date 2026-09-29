import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../../api'
import { mountWithHost } from '../../test/host'
import { fakeAdminApi, TARGET_ID, type FakeAdminApi } from '../../test/fixtures'
import DeleteTarget from './DeleteTarget.vue'

const host = vi.hoisted(() => ({ api: undefined as unknown }))
vi.mock('../../composables/useAdminApi', () => ({ useAdminApi: () => host.api }))

let api: FakeAdminApi
beforeEach(() => {
  api = fakeAdminApi()
  host.api = api
})

async function confirmDelete() {
  const wrapper = mountWithHost(DeleteTarget, {
    props: { targetId: TARGET_ID, targetName: 'Buddy' }
  })
  await wrapper.find('[data-testid="delete"]').trigger('click')
  await wrapper.find('[data-testid="confirm"]').trigger('click')
  await flushPromises()
  return wrapper
}

describe('DeleteTarget', () => {
  it('asks first and deletes nothing until confirmed', async () => {
    const wrapper = mountWithHost(DeleteTarget, {
      props: { targetId: TARGET_ID, targetName: 'Buddy' }
    })
    await wrapper.find('[data-testid="delete"]').trigger('click')

    expect(wrapper.find('[data-testid="confirm-delete"]').text()).toContain('Delete “Buddy”?')
    expect(api.deleteTarget).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="cancel-delete"]').trigger('click')
    expect(wrapper.find('[data-testid="confirm-delete"]').exists()).toBe(false)
    expect(api.deleteTarget).not.toHaveBeenCalled()
  })

  it('deletes once confirmed and says so', async () => {
    api.deleteTarget.mockResolvedValue(undefined)
    const wrapper = await confirmDelete()
    expect(api.deleteTarget).toHaveBeenCalledWith(TARGET_ID)
    expect(wrapper.emitted('deleted')).toHaveLength(1)
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
})
