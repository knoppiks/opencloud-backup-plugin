import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import { fakeApi, SPACE_ID, type FakeApi } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import RetentionEditor from './RetentionEditor.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

let fake: FakeApi
beforeEach(() => {
  fake = fakeApi()
  api.current = fake
})

function editor(editable = true) {
  return mountWithHost(RetentionEditor, {
    props: { spaceId: SPACE_ID, retentionDays: 90, editable }
  })
}

async function openAndType(wrapper: ReturnType<typeof editor>, value: string) {
  await wrapper.find('[data-testid="retention-edit"]').trigger('click')
  await wrapper.find('input').setValue(value)
}

describe('RetentionEditor', () => {
  it('is read-only for someone who may not change it', () => {
    const wrapper = editor(false)
    expect(wrapper.find('[data-testid="retention-days"]').text()).toBe('90 days')
    expect(wrapper.find('[data-testid="retention-edit"]').exists()).toBe(false)
  })

  it('explains the floor beside the field, before anything is saved', async () => {
    const wrapper = editor()
    await wrapper.find('[data-testid="retention-edit"]').trigger('click')
    expect(wrapper.find('.description').text()).toMatch(/^At least 7 days\./)
  })

  // The race PATCH narrows: re-sending target and enabled as read a moment ago
  // would undo a concurrent change to them.
  it('sends only the retention, through PATCH', async () => {
    fake.patchBackupConfig.mockResolvedValue({ retention_days: 30 })
    const wrapper = editor()
    await openAndType(wrapper, '30')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(fake.patchBackupConfig).toHaveBeenCalledWith(SPACE_ID, { retention_days: 30 })
    expect(fake.setBackupConfig).not.toHaveBeenCalled()
    expect(wrapper.emitted('saved')).toEqual([[30]])
    expect(wrapper.find('form').exists()).toBe(false)
  })

  it('refuses a value below the floor without asking the server', async () => {
    const wrapper = editor()
    await openAndType(wrapper, '3')

    expect(wrapper.find('.error').text()).toBe('Backups must be kept for at least 7 days.')
    expect(wrapper.find('[data-testid="retention-save"]').attributes('disabled')).toBeDefined()
    await wrapper.find('form').trigger('submit')
    await flushPromises()
    expect(fake.patchBackupConfig).not.toHaveBeenCalled()
  })

  it('refuses something that is not a whole number of days', async () => {
    const wrapper = editor()
    await openAndType(wrapper, '7.5')
    expect(wrapper.find('.error').text()).toBe('Enter a whole number of days.')
  })

  // The server stays the authority; its refusal is shown, not swallowed.
  it('shows the server refusing the save, and stays open', async () => {
    fake.patchBackupConfig.mockRejectedValue(
      new ApiError('bad_request', 'x', 400, 'retention_days must be at least 7')
    )
    const wrapper = editor()
    await openAndType(wrapper, '30')
    await wrapper.find('form').trigger('submit')
    await flushPromises()

    expect(wrapper.find('[role="alert"]').text()).toContain('retention_days must be at least 7')
    expect(wrapper.find('form').exists()).toBe(true)
    expect(wrapper.emitted('saved')).toBeUndefined()
  })

  it('cancels without saving', async () => {
    const wrapper = editor()
    await openAndType(wrapper, '30')
    await wrapper
      .findAll('button')
      .find((b) => b.text() === 'Cancel')!
      .trigger('click')

    expect(wrapper.find('form').exists()).toBe(false)
    expect(fake.patchBackupConfig).not.toHaveBeenCalled()
  })
})
