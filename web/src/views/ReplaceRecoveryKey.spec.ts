// Replacing the Recovery Key as a manager meets it. recoverykey/rotation.spec.ts
// covers the machine exhaustively; this checks the rendering of each step and
// that neither key reaches anything but the screen and the local ceremony.
import { flushPromises, type VueWrapper } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import {
  base64Encode,
  generateRecoveryKey,
  performRecoveryKeyRotation,
  randomBytes,
  recoveryKeyOpens,
  UnwrapError,
  type RotationCeremony,
  type RotationOptions
} from '../crypto'
import { translations } from '../l10n/translations'
import { fakeApi, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import ReplaceRecoveryKey from './ReplaceRecoveryKey.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

// Real Argon2id runs in crypto/ceremony.spec.ts.
vi.mock('../crypto', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../crypto')>()
  return { ...actual, performRecoveryKeyRotation: vi.fn(), recoveryKeyOpens: vi.fn() }
})
const rotateMock = vi.mocked(performRecoveryKeyRotation)
const opensMock = vi.mocked(recoveryKeyOpens)

vi.mock('../recoverykey/gate', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../recoverykey/gate')>()
  return { ...actual, nextFrame: () => Promise.resolve() }
})

let fake: FakeApi
let stored: Uint8Array
let ceremonies: RotationCeremony[]
let oldKey: string

beforeEach(() => {
  fake = fakeApi()
  api.current = fake
  stored = randomBytes(90)
  ceremonies = []
  oldKey = generateRecoveryKey().display
  rotateMock.mockReset()
  rotateMock.mockImplementation(async (options: RotationOptions) => {
    const envelope = randomBytes(90)
    const ceremony: RotationCeremony = {
      recoveryKey: generateRecoveryKey().display,
      envelope,
      request: {
        wrapped_dk_rk: base64Encode(envelope),
        replaces_sha256: `digest-of-${options.currentEnvelope.length}`
      }
    }
    ceremonies.push(ceremony)
    return ceremony
  })
  opensMock.mockReset()
})

async function mountPage(role = 'manager', language?: string) {
  fake.listSpaces.mockResolvedValue([space({ role })])
  fake.status.mockResolvedValue(status())
  fake.recoveryEnvelope.mockResolvedValue({ envelope: base64Encode(stored) })
  const wrapper = mountWithHost(ReplaceRecoveryKey, {
    props: { spaceId: SPACE_ID },
    ...(language ? { language, translations } : {})
  })
  await flushPromises()
  return wrapper
}

const step = (wrapper: VueWrapper) => wrapper.find('[data-step]').attributes('data-step')

async function enterCurrent(wrapper: VueWrapper, key: string) {
  await wrapper.find('[data-testid="current-key"] input').setValue(key)
  await wrapper.find('form[data-step="enter_current"]').trigger('submit')
  await flushPromises()
}

async function passGate(wrapper: VueWrapper, key: string) {
  const groups = key.split('-').slice(1)
  for (const field of wrapper.findAll('[data-testid^="gate-"] input')) {
    const label = field.element.closest('[data-testid]')?.getAttribute('data-testid') ?? ''
    await field.setValue(groups[Number(label.replace('gate-', '')) - 1] as string)
  }
  await wrapper.find('form[data-step="confirm"]').trigger('submit')
  await flushPromises()
}

async function toGate(wrapper: VueWrapper) {
  await enterCurrent(wrapper, oldKey)
  await wrapper.find('[data-testid="key-saved"]').trigger('click')
}

describe('ReplaceRecoveryKey roles', () => {
  it.each(['viewer', 'editor'])('tells a %s that a manager has to do it', async (role) => {
    const wrapper = await mountPage(role)
    expect(step(wrapper)).toBe('needs_manager')
    expect(wrapper.find('[data-testid="current-key"]').exists()).toBe(false)
  })

  it('asks a manager for the current key, with the lost-key facts', async () => {
    const wrapper = await mountPage()
    expect(step(wrapper)).toBe('enter_current')
    expect(wrapper.find('[data-testid="lost-key"]').exists()).toBe(true)
  })
})

describe('ReplaceRecoveryKey happy path', () => {
  it('shows the new key once, gates it, sends only the ceremony’s request, then says what changed', async () => {
    const wrapper = await mountPage()
    await enterCurrent(wrapper, oldKey)
    expect(rotateMock).toHaveBeenCalledWith({ currentEnvelope: stored, currentRecoveryKey: oldKey })
    expect(step(wrapper)).toBe('show_key')
    const newKey = ceremonies[0]!.recoveryKey
    const shown = wrapper.findAll('[data-testid="recovery-key"] li span:last-child')
    expect(shown.map((g) => g.text())).toEqual(newKey.split('-').slice(1))
    expect(fake.rotateRecoveryKey).not.toHaveBeenCalled()

    await wrapper.find('[data-testid="key-saved"]').trigger('click')
    expect(step(wrapper)).toBe('confirm')
    expect(wrapper.find('[data-testid="gate-submit"]').text()).toBe('Replace the Recovery Key')

    fake.rotateRecoveryKey.mockResolvedValueOnce({})
    await passGate(wrapper, newKey)
    expect(fake.rotateRecoveryKey).toHaveBeenCalledWith(SPACE_ID, ceremonies[0]!.request)
    expect(step(wrapper)).toBe('done')
    expect(wrapper.find('[data-testid="old-key"]').text()).toContain(
      'The old Recovery Key stops working once the next backup has run'
    )

    fake.runBackup.mockResolvedValueOnce({})
    await wrapper.find('[data-testid="back-up-now"]').trigger('click')
    await flushPromises()
    expect(fake.runBackup).toHaveBeenCalledWith(SPACE_ID)
    expect(wrapper.find('[data-testid="backup-started"]').exists()).toBe(true)
  })

  it('blocks a gate that does not match', async () => {
    const wrapper = await mountPage()
    await toGate(wrapper)
    for (const field of wrapper.findAll('[data-testid^="gate-"] input')) {
      await field.setValue('00000')
    }
    await wrapper.find('form[data-step="confirm"]').trigger('submit')
    await flushPromises()
    expect(wrapper.find('[data-testid="gate-mismatch"]').exists()).toBe(true)
    expect(fake.rotateRecoveryKey).not.toHaveBeenCalled()
  })
})

describe('ReplaceRecoveryKey failures', () => {
  it('says a wrong current key does not open the backups, and keeps it for correction', async () => {
    const wrapper = await mountPage()
    rotateMock.mockRejectedValueOnce(new UnwrapError())
    await enterCurrent(wrapper, oldKey)
    expect(wrapper.find('[data-failure="wrong"]').exists()).toBe(true)
    const input = wrapper.find('[data-testid="current-key"] input').element as HTMLInputElement
    expect(input.value).toBe(oldKey)
  })

  it('clears the current key once it has been used', async () => {
    const wrapper = await mountPage()
    fake.rotateRecoveryKey.mockRejectedValueOnce(new ApiError('bad_request', 'x', 400))
    await toGate(wrapper)
    await passGate(wrapper, ceremonies[0]!.recoveryKey)
    expect(step(wrapper)).toBe('enter_current')
    expect(wrapper.find('[data-testid="discard-new"]').exists()).toBe(true)
    const input = wrapper.find('[data-testid="current-key"] input').element as HTMLInputElement
    expect(input.value).toBe('')
  })

  it('says someone else replaced it on 409', async () => {
    const wrapper = await mountPage()
    fake.rotateRecoveryKey.mockRejectedValueOnce(new ApiError('conflict', 'x', 409))
    await toGate(wrapper)
    await passGate(wrapper, ceremonies[0]!.recoveryKey)
    expect(step(wrapper)).toBe('replaced_elsewhere')
  })

  it('settles an uncertain POST by checking again', async () => {
    const wrapper = await mountPage()
    fake.rotateRecoveryKey.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await toGate(wrapper)
    await passGate(wrapper, ceremonies[0]!.recoveryKey)
    expect(step(wrapper)).toBe('uncertain')

    opensMock.mockResolvedValueOnce(true)
    await wrapper.find('[data-testid="check-again"]').trigger('click')
    await flushPromises()
    expect(opensMock).toHaveBeenCalledWith(stored, ceremonies[0]!.recoveryKey)
    expect(step(wrapper)).toBe('done')
  })
})

describe('ReplaceRecoveryKey: neither key reaches the API', () => {
  it('holds through a refusal, an uncertain POST, its resolution and a backup', async () => {
    const wrapper = await mountPage()
    fake.rotateRecoveryKey
      .mockRejectedValueOnce(new ApiError('bad_request', 'x', 400))
      .mockRejectedValueOnce(new ApiError('timeout', 'x'))
    await toGate(wrapper)
    await passGate(wrapper, ceremonies[0]!.recoveryKey)
    await toGate(wrapper)
    await passGate(wrapper, ceremonies[1]!.recoveryKey)
    expect(step(wrapper)).toBe('uncertain')
    opensMock.mockResolvedValueOnce(false).mockResolvedValueOnce(true)
    await wrapper.find('[data-testid="check-again"]').trigger('click')
    await flushPromises()
    expect(step(wrapper)).toBe('enter_current')

    const keys = [oldKey, ...ceremonies.map((c) => c.recoveryKey)]
    for (const key of keys) {
      const groups = key.split('-').slice(1)
      for (const [name, method] of Object.entries(fake)) {
        for (const args of method.mock.calls) {
          const sent = JSON.stringify(args)
          expect(sent, name).not.toContain(key)
          expect(sent, name).not.toContain(groups.join(''))
          for (const group of groups) {
            expect(sent, name).not.toContain(group)
          }
        }
      }
    }
  })
})

describe('ReplaceRecoveryKey in German', () => {
  it('renders the first step in German', async () => {
    const wrapper = await mountPage('manager', 'de')
    expect(wrapper.text()).toContain(
      translations.de!['To make the new key, enter the current Recovery Key.']
    )
  })
})
