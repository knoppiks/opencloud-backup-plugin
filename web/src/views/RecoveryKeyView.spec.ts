// The Recovery Key page as a member meets it. recoverykey/check.spec.ts covers
// the machine; this checks what is rendered, who is offered what, and that the
// key typed here reaches nothing but the local check.
import { flushPromises, type VueWrapper } from '@vue/test-utils'
import { beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError } from '../api'
import {
  base64Encode,
  generateRecoveryKey,
  MIN_ARGON_PARAMS,
  randomBytes,
  recoveryKeyOpens,
  seal,
  WrapKind
} from '../crypto'
import { translations } from '../l10n/translations'
import { saveBytes } from '../recoverykey/download'
import { fakeApi, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import RecoveryKeyView from './RecoveryKeyView.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

// Argon2id is real in recoverykey/check.spec.ts's parser and in
// crypto/ceremony.spec.ts; here the answer is what the page does with it.
vi.mock('../crypto', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../crypto')>()
  return { ...actual, recoveryKeyOpens: vi.fn() }
})
const opensMock = vi.mocked(recoveryKeyOpens)

vi.mock('../recoverykey/download', () => ({ saveBytes: vi.fn() }))
const saveMock = vi.mocked(saveBytes)

vi.mock('../recoverykey/gate', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../recoverykey/gate')>()
  return { ...actual, nextFrame: () => Promise.resolve() }
})

let fake: FakeApi
let envelope: Uint8Array

beforeAll(async () => {
  envelope = await seal({
    plaintext: randomBytes(32),
    secret: generateRecoveryKey().secret,
    kind: WrapKind.RK,
    params: MIN_ARGON_PARAMS
  })
})

beforeEach(() => {
  fake = fakeApi()
  api.current = fake
  opensMock.mockReset()
  saveMock.mockReset()
})

async function mountPage(role = 'viewer', keys = true, language?: string) {
  fake.listSpaces.mockResolvedValue([space({ role })])
  fake.status.mockResolvedValue(status({ keys_configured: keys }))
  fake.recoveryEnvelope.mockResolvedValue({ envelope: base64Encode(envelope) })
  const wrapper = mountWithHost(RecoveryKeyView, {
    props: { spaceId: SPACE_ID },
    ...(language ? { language, translations } : {})
  })
  await flushPromises()
  return wrapper
}

async function checkKey(wrapper: VueWrapper, key: string) {
  await wrapper.find('[data-testid="check-input"] input').setValue(key)
  await wrapper.find('[data-testid="check-form"]').trigger('submit')
  await flushPromises()
}

const result = (wrapper: VueWrapper) => wrapper.find('[data-result]').attributes('data-result')

describe('RecoveryKeyView for each role', () => {
  it('offers a viewer the check, the key file and the lost-key facts, but no replacement', async () => {
    const wrapper = await mountPage('viewer')
    expect(wrapper.find('h1').text()).toBe('Recovery Key for Family photos')
    expect(wrapper.find('[data-testid="check-form"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="download-envelope"]').exists()).toBe(true)
    expect(wrapper.findAll('[data-testid="lost-key"]')).toHaveLength(1)
    expect(wrapper.find('[data-testid="replace"]').exists()).toBe(false)
  })

  it('links a manager to the replacement', async () => {
    const wrapper = await mountPage('manager')
    const link = wrapper.find('[data-testid="replace"] a')
    expect(JSON.parse(link.attributes('data-to') as string)).toEqual({
      name: 'backup-vault-recovery-key-replace',
      params: { spaceId: SPACE_ID }
    })
  })

  it('says there is nothing yet for a Space without keys', async () => {
    const wrapper = await mountPage('manager', false)
    expect(wrapper.find('[data-step="not_set_up"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="check-form"]').exists()).toBe(false)
  })
})

describe('RecoveryKeyView check', () => {
  it('says so when the key opens the backups', async () => {
    const wrapper = await mountPage()
    const key = generateRecoveryKey().display
    opensMock.mockResolvedValueOnce(true)
    await checkKey(wrapper, key)
    expect(opensMock).toHaveBeenCalledWith(envelope, key)
    expect(result(wrapper)).toBe('opens')
  })

  it('calls a wrong key wrong, with the lost-key facts inside the answer only', async () => {
    const wrapper = await mountPage()
    opensMock.mockResolvedValueOnce(false)
    await checkKey(wrapper, generateRecoveryKey().display)
    expect(result(wrapper)).toBe('wrong')
    expect(wrapper.findAll('[data-testid="lost-key"]')).toHaveLength(1)
    expect(wrapper.find('[data-result="wrong"] [data-testid="lost-key"]').exists()).toBe(true)
  })

  it('calls a typo not a Recovery Key, without fetching anything', async () => {
    const wrapper = await mountPage()
    await checkKey(wrapper, 'definitely not a key')
    expect(result(wrapper)).toBe('malformed')
    expect(fake.recoveryEnvelope).not.toHaveBeenCalled()
    expect(opensMock).not.toHaveBeenCalled()
  })

  it('says it could not check, not that the key is wrong, when the envelope fails', async () => {
    const wrapper = await mountPage()
    fake.recoveryEnvelope.mockRejectedValueOnce(new ApiError('offline', 'x'))
    await checkKey(wrapper, generateRecoveryKey().display)
    expect(result(wrapper)).toBe('cannot_tell')
    expect(wrapper.find('[data-testid="action-error"]').text()).toContain(
      'The backup service cannot be reached'
    )
  })

  it('forgets the answer when the key is edited', async () => {
    const wrapper = await mountPage()
    await checkKey(wrapper, 'nope')
    expect(result(wrapper)).toBe('malformed')
    await wrapper.find('[data-testid="check-input"] input').setValue('nope2')
    expect(wrapper.find('[data-result]').exists()).toBe(false)
  })

  it('sends the typed key to no API call', async () => {
    const wrapper = await mountPage('manager')
    const key = generateRecoveryKey().display
    opensMock.mockResolvedValue(false)
    await checkKey(wrapper, key)
    await checkKey(wrapper, key.toLowerCase())
    await wrapper.find('[data-testid="download-envelope"]').trigger('click')
    await flushPromises()

    const groups = key.split('-').slice(1)
    for (const [name, method] of Object.entries(fake)) {
      for (const args of method.mock.calls) {
        const sent = JSON.stringify(args).toUpperCase()
        expect(sent, name).not.toContain(key.toUpperCase())
        expect(sent, name).not.toContain(groups.join(''))
        for (const group of groups) {
          expect(sent, name).not.toContain(group)
        }
      }
    }
  })
})

describe('RecoveryKeyView key file', () => {
  it('downloads the stored envelope as recovery.ocbke', async () => {
    const wrapper = await mountPage()
    await wrapper.find('[data-testid="download-envelope"]').trigger('click')
    await flushPromises()
    expect(saveMock).toHaveBeenCalledWith(envelope, 'recovery.ocbke')
    expect(wrapper.find('[data-testid="downloaded"]').exists()).toBe(true)
  })

  it('reports a failed download', async () => {
    const wrapper = await mountPage()
    fake.recoveryEnvelope.mockRejectedValueOnce(new ApiError('forbidden', 'x', 403))
    await wrapper.find('[data-testid="download-envelope"]').trigger('click')
    await flushPromises()
    expect(saveMock).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="download"] [data-testid="action-error"]').exists()).toBe(
      true
    )
  })
})

describe('RecoveryKeyView in German', () => {
  it('renders the page in German', async () => {
    const wrapper = await mountPage('manager', true, 'de')
    const text = wrapper.text()
    expect(text).toContain(translations.de!['Check my Recovery Key'])
    expect(text).toContain(translations.de!['If the Recovery Key is lost'])
    expect(text).toContain(translations.de!['Download recovery.ocbke'])
  })
})
