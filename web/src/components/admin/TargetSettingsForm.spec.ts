import { flushPromises, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type AdminTarget } from '../../api'
import { mountWithHost } from '../../test/host'
import { adminTarget, fakeAdminApi, TARGET_ID, type FakeAdminApi } from '../../test/fixtures'
import TargetSettingsForm from './TargetSettingsForm.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../../composables/useAdminApi', () => ({ useAdminApi: () => api.current }))

// Markers, not realistic keys: a secret scanner cannot tell a test value from
// a real one.
const BACKUP_SECRET = 'backup-secret-marker'
const MAINTENANCE_SECRET = 'maintenance-secret-marker'

let fake: FakeAdminApi
beforeEach(() => {
  fake = fakeAdminApi()
  api.current = fake
})

function mountForm(target?: AdminTarget) {
  return mountWithHost(TargetSettingsForm, { props: target ? { target } : {} })
}

async function type(wrapper: VueWrapper, testid: string, value: string) {
  await wrapper.find(`input[data-testid="${testid}"]`).setValue(value)
}

async function typePair(wrapper: VueWrapper, pair: string, id: string, secret: string) {
  const fields = wrapper.find(`[data-testid="${pair}"]`)
  await fields.find('input[data-testid="access-key-id"]').setValue(id)
  await fields.find('input[data-testid="secret-access-key"]').setValue(secret)
}

async function fillSettings(wrapper: VueWrapper) {
  await type(wrapper, 'name', 'Buddy')
  await type(wrapper, 'endpoint', 'buddy:3900')
  await type(wrapper, 'bucket', 'backups')
}

async function submit(wrapper: VueWrapper) {
  await wrapper.find('form').trigger('submit')
  await flushPromises()
}

function passwordValues(wrapper: VueWrapper): string[] {
  return wrapper
    .findAll('input[type="password"]')
    .map((input) => (input.element as HTMLInputElement).value)
}

describe('TargetSettingsForm: creating', () => {
  it('asks for the keys from the start and has no way to skip them', () => {
    const wrapper = mountForm()
    expect(wrapper.find('[data-testid="backup-keys"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="replace-keys"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="keep-keys"]').exists()).toBe(false)
  })

  it('names what is missing and sends nothing', async () => {
    const wrapper = mountForm()
    expect(wrapper.find('.error').exists()).toBe(false)

    await submit(wrapper)

    expect(fake.createTarget).not.toHaveBeenCalled()
    // name, endpoint, bucket, backup id, backup secret.
    expect(wrapper.findAll('.error')).toHaveLength(5)
  })

  it('creates with the backup pair only, and forgets the keys afterwards', async () => {
    const created = adminTarget()
    fake.createTarget.mockResolvedValue(created)
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await wrapper.find('[data-testid="path-style"] input').setValue(true)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)

    await submit(wrapper)

    expect(fake.createTarget).toHaveBeenCalledWith({
      name: 'Buddy',
      endpoint: 'buddy:3900',
      bucket: 'backups',
      region: '',
      prefix: '',
      use_path_style: true,
      disable_tls: false,
      credentials: { access_key_id: 'backup-id', secret_access_key: BACKUP_SECRET }
    })
    expect(wrapper.emitted('saved')).toEqual([[created]])
    expect(passwordValues(wrapper)).toEqual(['', ''])
  })

  it('sends a maintenance pair when one is typed', async () => {
    fake.createTarget.mockResolvedValue(adminTarget())
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)
    await typePair(wrapper, 'maintenance-keys', 'maint-id', MAINTENANCE_SECRET)

    await submit(wrapper)

    expect(fake.createTarget.mock.calls[0]![0].maintenance_credentials).toEqual({
      access_key_id: 'maint-id',
      secret_access_key: MAINTENANCE_SECRET
    })
  })

  it('refuses half a maintenance pair', async () => {
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)
    await typePair(wrapper, 'maintenance-keys', 'maint-id', '')

    await submit(wrapper)

    expect(fake.createTarget).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="maintenance-keys"] .error').exists()).toBe(true)
  })

  it('shows a refusal in admin wording, with the server detail, and keeps what was typed', async () => {
    fake.createTarget.mockRejectedValue(
      new ApiError(
        'unavailable',
        'x',
        503,
        'target credentials cannot be stored: no target-wrap key is configured'
      )
    )
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)

    await submit(wrapper)

    const alert = wrapper.find('[data-testid="action-error"]')
    expect(alert.text()).toContain('no target-wrap key is configured')
    expect(alert.text()).toContain('Ask whoever runs the backup service.')
    expect(wrapper.emitted('saved')).toBeUndefined()
    expect(passwordValues(wrapper)[0]).toBe(BACKUP_SECRET)
  })
})

describe('TargetSettingsForm: editing', () => {
  it('prefills the settings and never the keys', () => {
    const wrapper = mountForm(adminTarget({ prefix: 'family/' }))
    const value = (id: string) =>
      (wrapper.find(`input[data-testid="${id}"]`).element as HTMLInputElement).value

    expect(value('name')).toBe('Buddy')
    expect(value('endpoint')).toBe('buddy.example.org:3900')
    expect(value('prefix')).toBe('family/')
    expect(
      (wrapper.find('[data-testid="path-style"] input').element as HTMLInputElement).checked
    ).toBe(true)
    // Closed: there are no key fields to fill at all.
    expect(wrapper.find('[data-testid="keys-stored"]').exists()).toBe(true)
    expect(wrapper.findAll('input[type="password"]')).toHaveLength(0)
  })

  it('opens empty key fields on "Replace keys"', async () => {
    const wrapper = mountForm(adminTarget())
    await wrapper.find('[data-testid="replace-keys"]').trigger('click')
    expect(passwordValues(wrapper)).toEqual(['', ''])
  })

  it('saves without keys while the section is closed', async () => {
    fake.updateTarget.mockResolvedValue(adminTarget({ name: 'Renamed' }))
    const wrapper = mountForm(adminTarget())
    await type(wrapper, 'name', 'Renamed')

    await submit(wrapper)

    const [id, body] = fake.updateTarget.mock.calls[0]!
    expect(id).toBe(TARGET_ID)
    expect(body.name).toBe('Renamed')
    expect(body).not.toHaveProperty('credentials')
    expect(body).not.toHaveProperty('maintenance_credentials')
    expect(wrapper.find('[data-testid="saved"]').exists()).toBe(true)
  })

  it('requires the backup pair once the keys are being replaced', async () => {
    const wrapper = mountForm(adminTarget())
    await wrapper.find('[data-testid="replace-keys"]').trigger('click')

    await submit(wrapper)

    expect(fake.updateTarget).not.toHaveBeenCalled()
    expect(wrapper.findAll('[data-testid="backup-keys"] .error')).toHaveLength(2)
  })

  it('replaces the keys, then closes the section and forgets them', async () => {
    fake.updateTarget.mockResolvedValue(adminTarget())
    const wrapper = mountForm(adminTarget())
    await wrapper.find('[data-testid="replace-keys"]').trigger('click')
    await typePair(wrapper, 'backup-keys', 'new-id', BACKUP_SECRET)

    await submit(wrapper)

    expect(fake.updateTarget.mock.calls[0]![1].credentials).toEqual({
      access_key_id: 'new-id',
      secret_access_key: BACKUP_SECRET
    })
    expect(wrapper.findAll('input[type="password"]')).toHaveLength(0)
    expect(wrapper.find('[data-testid="keys-stored"]').exists()).toBe(true)
  })

  it('warns that both pairs are replaced when a maintenance pair is stored', async () => {
    const plain = mountForm(adminTarget())
    await plain.find('[data-testid="replace-keys"]').trigger('click')
    expect(plain.find('[data-testid="replace-both-warning"]').exists()).toBe(false)

    const separated = mountForm(adminTarget({ maintenance_configured: true }))
    await separated.find('[data-testid="replace-keys"]').trigger('click')
    expect(separated.find('[data-testid="replace-both-warning"]').exists()).toBe(true)
  })

  it('"Keep the stored keys" closes the section and drops what was typed', async () => {
    fake.updateTarget.mockResolvedValue(adminTarget())
    const wrapper = mountForm(adminTarget())
    await wrapper.find('[data-testid="replace-keys"]').trigger('click')
    await typePair(wrapper, 'backup-keys', 'typed-id', BACKUP_SECRET)
    await wrapper.find('[data-testid="keep-keys"]').trigger('click')

    await wrapper.find('[data-testid="replace-keys"]').trigger('click')
    expect(passwordValues(wrapper)).toEqual(['', ''])

    await wrapper.find('[data-testid="keep-keys"]').trigger('click')
    await submit(wrapper)
    expect(fake.updateTarget.mock.calls[0]![1]).not.toHaveProperty('credentials')
  })
})

describe('TargetSettingsForm: connection check', () => {
  it('is offered only once a backup pair is typed, and never runs by itself', async () => {
    const wrapper = mountForm()
    const button = () => wrapper.find('[data-testid="check"]')
    expect(button().attributes('disabled')).toBeDefined()

    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)

    expect(button().attributes('disabled')).toBeUndefined()
    expect(fake.checkTarget).not.toHaveBeenCalled()
  })

  it('checks what is typed and shows one outcome per key pair', async () => {
    fake.checkTarget.mockResolvedValue([
      { role: 'backup', outcome: 'ok' },
      { role: 'maintenance', outcome: 'denied' }
    ])
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)
    await typePair(wrapper, 'maintenance-keys', 'maint-id', MAINTENANCE_SECRET)

    await wrapper.find('[data-testid="check"]').trigger('click')
    await flushPromises()

    expect(fake.checkTarget.mock.calls[0]![0]).toMatchObject({
      bucket: 'backups',
      credentials: { access_key_id: 'backup-id' },
      maintenance_credentials: { access_key_id: 'maint-id' }
    })
    const items = wrapper.findAll('[data-testid="check-results"] li')
    expect(items.map((li) => li.text())).toEqual([
      'Backup keys: Works',
      'Maintenance keys: Access denied'
    ])
    // Checking is not saving.
    expect(fake.createTarget).not.toHaveBeenCalled()
  })

  it('clears a result as soon as anything it was about changes', async () => {
    fake.checkTarget.mockResolvedValue([{ role: 'backup', outcome: 'ok' }])
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)
    await wrapper.find('[data-testid="check"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="check-results"]').exists()).toBe(true)

    await type(wrapper, 'bucket', 'other')

    expect(wrapper.find('[data-testid="check-results"]').exists()).toBe(false)
  })

  it('names missing settings instead of sending a check the server would refuse', async () => {
    const wrapper = mountForm()
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)

    await wrapper.find('[data-testid="check"]').trigger('click')
    await flushPromises()

    expect(fake.checkTarget).not.toHaveBeenCalled()
    // The error sits beside the input, inside its label.
    expect(
      wrapper.find('input[data-testid="name"]').element.closest('label')!.querySelector('.error')
    ).not.toBeNull()
  })

  it('shows a failed check as a failure, not as an outcome', async () => {
    fake.checkTarget.mockRejectedValue(
      new ApiError('unavailable', 'x', 503, 'target checks are not available')
    )
    const wrapper = mountForm()
    await fillSettings(wrapper)
    await typePair(wrapper, 'backup-keys', 'backup-id', BACKUP_SECRET)

    await wrapper.find('[data-testid="check"]').trigger('click')
    await flushPromises()

    expect(wrapper.find('[data-testid="check-results"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="connection-check"] [role="alert"]').text()).toContain(
      'target checks are not available'
    )
  })
})
