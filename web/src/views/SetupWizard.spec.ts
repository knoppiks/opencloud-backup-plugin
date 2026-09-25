// The setup wizard as a person meets it. The machine's own spec covers the
// state logic exhaustively; this one checks what is rendered for each state and
// that the page never hands the Recovery Key to anything but the screen.
import { flushPromises, type VueWrapper } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { ApiError, type BackupStatus, type Target } from '../api'
import {
  base64Encode,
  CeremonyError,
  generateRecoveryKey,
  performSetupCeremony,
  type SetupCeremony
} from '../crypto'
import { translations } from '../l10n/translations'
import { fakeApi, space, SPACE_ID, status, type FakeApi } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import SetupWizard from './SetupWizard.vue'

const api = vi.hoisted(() => ({ current: undefined as unknown }))
vi.mock('../composables/useBackupApi', () => ({ useBackupApi: () => api.current }))

// Real Argon2id at the default parameters takes seconds; crypto/ceremony.spec.ts
// tests the real ceremony, including its self-verification.
vi.mock('../crypto', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../crypto')>()
  return { ...actual, performSetupCeremony: vi.fn() }
})
const ceremonyMock = vi.mocked(performSetupCeremony)

// jsdom never paints, so a frame callback would never come.
vi.mock('../wizard/machine', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../wizard/machine')>()
  return { ...actual, nextFrame: () => Promise.resolve() }
})

const ONE: Target = { id: 't-1', name: 'Buddy' }
const TWO: Target = { id: 't-2', name: 'Offsite' }

let fake: FakeApi
let ceremony: SetupCeremony | undefined

function unset(overrides: Partial<BackupStatus> = {}): BackupStatus {
  const s = status({ configured: false, keys_configured: false, enabled: false, ...overrides })
  delete s.last_run
  delete s.last_successful_run
  delete s.next_run
  return s
}

function given(board: BackupStatus, role = 'manager', targets: Target[] = [ONE]) {
  fake.listSpaces.mockResolvedValue([space({ role })])
  fake.status.mockResolvedValueOnce(board)
  fake.listTargets.mockResolvedValue(targets)
}

async function mountWizard(language?: string) {
  const wrapper = mountWithHost(SetupWizard, {
    props: { spaceId: SPACE_ID },
    ...(language ? { language, translations } : {})
  })
  await flushPromises()
  return wrapper
}

const step = (wrapper: VueWrapper) => wrapper.find('[data-step]').attributes('data-step')

/** passGate answers whichever two groups the gate asked for. */
async function passGate(wrapper: VueWrapper, key: string) {
  const groups = key.split('-').slice(1)
  for (const field of wrapper.findAll('[data-testid^="gate-"] input')) {
    const label = field.element.closest('[data-testid]')?.getAttribute('data-testid') ?? ''
    const number = Number(label.replace('gate-', ''))
    await field.setValue(groups[number - 1] as string)
  }
  await wrapper.find('form[data-step="confirm"]').trigger('submit')
  await flushPromises()
}

beforeEach(() => {
  fake = fakeApi()
  api.current = fake
  fake.setBackupConfig.mockResolvedValue({})
  fake.setupKeys.mockResolvedValue({ configured: true })
  fake.setSchedule.mockResolvedValue({})
  ceremony = undefined
  ceremonyMock.mockReset()
  ceremonyMock.mockImplementation(() => {
    const dataKey = Uint8Array.from({ length: 32 }, (_, i) => i + 7)
    const envelope = new Uint8Array([4, 5, 6])
    ceremony = {
      recoveryKey: generateRecoveryKey().display,
      dataKey,
      envelope,
      request: { wrapped_dk_rk: base64Encode(envelope), data_key: base64Encode(dataKey) }
    }
    return Promise.resolve(ceremony)
  })
})
afterEach(() => vi.unstubAllGlobals())

describe('SetupWizard roles', () => {
  it('offers a viewer no wizard and writes nothing', async () => {
    given(unset(), 'viewer')
    const wrapper = await mountWizard()
    expect(step(wrapper)).toBe('not_allowed')
    expect(fake.setBackupConfig).not.toHaveBeenCalled()
  })

  it('lets an editor bind the target, then says a manager has to finish', async () => {
    given(unset(), 'editor')
    const wrapper = await mountWizard()
    expect(fake.setBackupConfig).toHaveBeenCalledWith(SPACE_ID, {
      target_id: 't-1',
      retention_days: 0,
      enabled: false
    })
    expect(step(wrapper)).toBe('needs_manager')
    expect(wrapper.text()).toContain('A manager of this space has to finish setup.')
    expect(wrapper.find('[data-testid="create-key"]').exists()).toBe(false)
  })

  it('brings a manager to the Recovery Key', async () => {
    given(unset(), 'manager')
    const wrapper = await mountWizard()
    expect(step(wrapper)).toBe('key_intro')
    expect(wrapper.find('[data-testid="create-key"]').exists()).toBe(true)
  })
})

describe('SetupWizard targets', () => {
  it('hides the picker with one granted target', async () => {
    given(unset())
    const wrapper = await mountWizard()
    expect(wrapper.find('[data-testid="target-picker"]').exists()).toBe(false)
    expect(fake.setBackupConfig).toHaveBeenCalledTimes(1)
  })

  it('shows the picker with several, and continues only after a choice', async () => {
    given(unset(), 'manager', [ONE, TWO])
    const wrapper = await mountWizard()
    const picker = wrapper.find('[data-testid="target-picker"]')
    expect(picker.findAll('label').map((l) => l.text())).toEqual(['Buddy', 'Offsite'])
    expect(wrapper.find('[data-testid="target-continue"]').attributes('disabled')).toBeDefined()

    await picker.findAll('input')[1]!.trigger('change')
    await wrapper.find('[data-testid="target-continue"]').trigger('click')
    await flushPromises()
    expect(fake.setBackupConfig).toHaveBeenCalledWith(SPACE_ID, {
      target_id: 't-2',
      retention_days: 0,
      enabled: false
    })
    expect(step(wrapper)).toBe('key_intro')
  })

  it('says to ask the administrator when there is no target', async () => {
    given(unset(), 'manager', [])
    const wrapper = await mountWizard()
    expect(step(wrapper)).toBe('no_targets')
    expect(wrapper.text()).toContain('Ask your administrator')
  })
})

describe('SetupWizard ceremony', () => {
  it('walks a manager from nothing to done', async () => {
    given(unset({ timezone: 'Europe/Berlin' }))
    const wrapper = await mountWizard()

    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()
    expect(step(wrapper)).toBe('show_key')
    const key = ceremony!.recoveryKey
    const shown = wrapper
      .findAll('[data-testid="recovery-key"] li')
      .map((li) => li.findAll('span').map((span) => span.text()))
    expect(shown).toEqual(
      key
        .split('-')
        .slice(1)
        .map((group, i) => [String(i + 1), group])
    )

    await wrapper.find('[data-testid="key-saved"]').trigger('click')
    expect(step(wrapper)).toBe('confirm')
    expect(wrapper.findAll('[data-testid^="gate-"] input')).toHaveLength(2)
    // The key is not on screen while the gate asks for it.
    expect(wrapper.text()).not.toContain(key)
    expect(wrapper.find('[data-testid="recovery-key"]').exists()).toBe(false)

    await passGate(wrapper, key)
    expect(fake.setupKeys).toHaveBeenCalledWith(SPACE_ID, ceremony!.request)
    expect(ceremony!.dataKey.every((b) => b === 0)).toBe(true)
    expect(step(wrapper)).toBe('schedule')
    expect(wrapper.find('[data-testid="timezone"]').text()).toBe(
      'Times are in the server’s time zone (Europe/Berlin).'
    )
    expect((wrapper.find('[data-testid="time"]').element as HTMLInputElement).value).toBe('02:30')

    const scheduled = status({ next_run: '2026-09-25T00:30:00Z' })
    delete scheduled.last_successful_run
    fake.status.mockResolvedValueOnce(scheduled)
    await wrapper.find('form[data-step="schedule"]').trigger('submit')
    await flushPromises()
    expect(fake.setSchedule).toHaveBeenCalledWith(SPACE_ID, {
      enabled: true,
      preset: { kind: 'daily', hour: 2, minute: 30 }
    })
    expect(step(wrapper)).toBe('done')
  })

  it('blocks setup on a wrong group', async () => {
    given(unset())
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="key-saved"]').trigger('click')

    for (const field of wrapper.findAll('[data-testid^="gate-"] input')) {
      await field.setValue('WRONG')
    }
    await wrapper.find('form[data-step="confirm"]').trigger('submit')
    await flushPromises()

    expect(fake.setupKeys).not.toHaveBeenCalled()
    expect(wrapper.find('[data-testid="gate-mismatch"]').exists()).toBe(true)
  })

  it('shows a failed self-verification as a failed ceremony, never the key', async () => {
    ceremonyMock.mockRejectedValueOnce(new CeremonyError('self_verify_failed', 'x'))
    given(unset())
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()

    expect(step(wrapper)).toBe('key_intro')
    expect(wrapper.find('[data-testid="ceremony-failed"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="recovery-key"]').exists()).toBe(false)
    expect(fake.setupKeys).not.toHaveBeenCalled()
  })

  it('treats 409 as already protected and offers no way to redo it', async () => {
    fake.setupKeys.mockRejectedValueOnce(new ApiError('conflict', 'x', 409))
    given(unset())
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="key-saved"]').trigger('click')
    await passGate(wrapper, ceremony!.recoveryKey)

    expect(step(wrapper)).toBe('already_protected')
    expect(wrapper.find('[data-testid="create-key"]').exists()).toBe(false)
    expect(wrapper.findAll('button')).toHaveLength(0)
  })

  it('never offers the ceremony for a Space that has keys', async () => {
    given(status({ enabled: false }))
    const wrapper = await mountWizard()
    expect(step(wrapper)).toBe('schedule')
    expect(ceremonyMock).not.toHaveBeenCalled()
  })

  it('copies the key to the clipboard, which is local', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    given(unset())
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="copy-key"]').trigger('click')
    await flushPromises()

    expect(writeText).toHaveBeenCalledWith(ceremony!.recoveryKey)
    expect(wrapper.find('[data-testid="copy-key"]').text()).toBe('Copied')
  })

  it('zeroizes an unsent Data Key when the page is left', async () => {
    given(unset())
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()
    wrapper.unmount()
    expect(ceremony!.dataKey.every((b) => b === 0)).toBe(true)
  })
})

describe('SetupWizard key hygiene', () => {
  it('never passes the Recovery Key to the API', async () => {
    given(unset())
    fake.setupKeys.mockRejectedValueOnce(new ApiError('timeout', 'x'))
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="create-key"]').trigger('click')
    await flushPromises()
    await wrapper.find('[data-testid="key-saved"]').trigger('click')
    await passGate(wrapper, ceremony!.recoveryKey)
    expect(step(wrapper)).toBe('setup_uncertain')

    fake.status.mockResolvedValueOnce(status({ enabled: false }))
    fake.recoveryEnvelope.mockResolvedValueOnce({ envelope: base64Encode(ceremony!.envelope) })
    await wrapper.find('[data-testid="check-again"]').trigger('click')
    await flushPromises()
    // The fake stored envelope is not an envelope at all. That is no answer
    // about the key, so the wizard stays uncertain rather than guessing.
    expect(fake.recoveryEnvelope).toHaveBeenCalledWith(SPACE_ID)
    expect(step(wrapper)).toBe('setup_uncertain')

    const key = ceremony!.recoveryKey
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
  })
})

describe('SetupWizard done and wording', () => {
  it('says when the first run is', async () => {
    const done = status({ next_run: '2026-09-25T00:30:00Z' })
    delete done.last_successful_run
    given(done)
    const wrapper = await mountWizard()
    expect(step(wrapper)).toBe('done')
    expect(wrapper.find('[data-testid="first-run"]').text()).toMatch(/^First backup: .+/)
  })

  it('says "server time" without a name when the service has none', async () => {
    given(status({ enabled: false }))
    const wrapper = await mountWizard()
    expect(wrapper.find('[data-testid="timezone"]').text()).toBe(
      'Times are in the server’s time zone.'
    )
  })

  it('speaks German', async () => {
    given(unset())
    const wrapper = await mountWizard('de')
    expect(wrapper.find('[data-testid="create-key"]').text()).toBe(
      'Meinen Wiederherstellungsschlüssel erstellen'
    )
  })

  it('offers weekdays when weekly is chosen, Sunday first', async () => {
    given(status({ enabled: false }))
    const wrapper = await mountWizard()
    await wrapper.find('[data-testid="kind-weekly"]').trigger('change')
    const days = wrapper.findAll('[data-testid="weekday"] option').map((o) => o.text())
    expect(days[0]).toBe('Sunday')
    expect(days).toHaveLength(7)

    await wrapper.find('[data-testid="weekday"]').setValue('3')
    fake.status.mockResolvedValueOnce(status())
    await wrapper.find('form[data-step="schedule"]').trigger('submit')
    await flushPromises()
    expect(fake.setSchedule).toHaveBeenCalledWith(SPACE_ID, {
      enabled: true,
      preset: { kind: 'weekly', hour: 2, minute: 30, weekday: 3 }
    })
  })
})
