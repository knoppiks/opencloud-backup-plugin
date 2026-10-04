// The two pieces setup and replacement share: showing a new key and gating it.
import { flushPromises } from '@vue/test-utils'
import { afterEach, describe, expect, it, vi } from 'vitest'
import { generateRecoveryKey } from '../crypto'
import { mountWithHost } from '../test/host'
import RecoveryKeyDisplay from './RecoveryKeyDisplay.vue'
import RecoveryKeyGate from './RecoveryKeyGate.vue'

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('RecoveryKeyDisplay', () => {
  it('shows the whole key as one string, prefix included, and the slot', () => {
    const key = generateRecoveryKey().display
    const wrapper = mountWithHost(RecoveryKeyDisplay, {
      props: { recoveryKey: key },
      slots: { default: '<button data-testid="extra">x</button>' }
    })
    const shown = wrapper.find('[data-testid="recovery-key"]')
    // textContent, not text(): a hand selection gets surrounding whitespace too.
    expect(shown.element.textContent).toBe(key)
    expect(shown.element.tagName).toBe('CODE')
    expect(shown.findAll('*')).toHaveLength(0)
    expect(wrapper.find('[data-testid="extra"]').exists()).toBe(true)
  })

  it('copies exactly the text it shows, and says so', async () => {
    const writeText = vi.fn((_text: string) => Promise.resolve())
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const key = generateRecoveryKey().display
    const wrapper = mountWithHost(RecoveryKeyDisplay, { props: { recoveryKey: key } })
    await wrapper.find('[data-testid="copy-key"]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledTimes(1)
    expect(writeText.mock.calls[0]![0]).toBe(
      wrapper.find('[data-testid="recovery-key"]').element.textContent
    )
    expect(writeText).toHaveBeenCalledWith(key)
    expect(wrapper.find('[data-testid="copy-key"]').text()).toBe('Copied')

    await wrapper.setProps({ recoveryKey: generateRecoveryKey().display })
    expect(wrapper.find('[data-testid="copy-key"]').text()).toBe('Copy')
  })

  it('only writes to the clipboard, never reads it', async () => {
    const readText = vi.fn(() => Promise.resolve(''))
    const read = vi.fn(() => Promise.resolve([]))
    vi.stubGlobal('navigator', {
      clipboard: { writeText: () => Promise.resolve(), readText, read }
    })
    const wrapper = mountWithHost(RecoveryKeyDisplay, {
      props: { recoveryKey: generateRecoveryKey().display }
    })
    await wrapper.find('[data-testid="copy-key"]').trigger('click')
    await flushPromises()
    expect(readText).not.toHaveBeenCalled()
    expect(read).not.toHaveBeenCalled()
  })

  it('does not claim a copy the clipboard refused', async () => {
    vi.stubGlobal('navigator', { clipboard: { writeText: () => Promise.reject(new Error('no')) } })
    const wrapper = mountWithHost(RecoveryKeyDisplay, {
      props: { recoveryKey: generateRecoveryKey().display }
    })
    await wrapper.find('[data-testid="copy-key"]').trigger('click')
    await flushPromises()
    expect(wrapper.find('[data-testid="copy-key"]').text()).toBe('Copy')
  })
})

describe('RecoveryKeyGate', () => {
  const props = { groups: [1, 5], mismatch: false, submitting: false, submitLabel: 'Go' }

  it('asks for the given groups and emits the answers in order', async () => {
    const wrapper = mountWithHost(RecoveryKeyGate, { props })
    expect(wrapper.find('[data-testid="gate-2"]').exists()).toBe(true)
    expect(wrapper.find('[data-testid="gate-6"]').exists()).toBe(true)
    await wrapper.find('input[data-testid="gate-2"]').setValue('aaaaa')
    await wrapper.find('input[data-testid="gate-6"]').setValue('bbbbb')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('submit')).toEqual([[['aaaaa', 'bbbbb']]])
    expect(wrapper.find('[data-testid="gate-submit"]').text()).toBe('Go')
  })

  it('counts groups after the prefix, in the label and in the drawn key', () => {
    const wrapper = mountWithHost(RecoveryKeyGate, { props })
    const label = wrapper.find('input[data-testid="gate-2"]').element.closest('label')
    expect(label?.textContent?.trim()).toBe('Group 2 after ocbk1-')
    expect(wrapper.text()).toContain('Groups are counted after ocbk1-.')
    const hint = wrapper.find('[data-testid="gate-hint-2"]')
    expect(hint.attributes('aria-hidden')).toBe('true')
    expect(hint.text()).toBe('ocbk1-•••••-_____-•••••-•••••-•••••-•••••-••••')
    expect(hint.find('mark').text()).toBe('_____')
    expect(wrapper.find('[data-testid="gate-hint-6"] mark').text()).toBe('_____')
  })

  it('clears the answers when new groups are asked for', async () => {
    const wrapper = mountWithHost(RecoveryKeyGate, { props })
    await wrapper.find('input[data-testid="gate-2"]').setValue('aaaaa')
    await wrapper.setProps({ groups: [1, 3] })
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('submit')).toEqual([[['', '']]])
  })

  it('shows a mismatch and offers the key again', async () => {
    const wrapper = mountWithHost(RecoveryKeyGate, { props: { ...props, mismatch: true } })
    expect(wrapper.find('[data-testid="gate-mismatch"]').exists()).toBe(true)
    await wrapper.find('[data-testid="show-again"]').trigger('click')
    expect(wrapper.emitted('showAgain')).toHaveLength(1)
  })
})
