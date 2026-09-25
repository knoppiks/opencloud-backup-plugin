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
  it('shows the seven groups, numbered, and the slot', () => {
    const key = generateRecoveryKey().display
    const wrapper = mountWithHost(RecoveryKeyDisplay, {
      props: { recoveryKey: key },
      slots: { default: '<button data-testid="extra">x</button>' }
    })
    const items = wrapper.findAll('[data-testid="recovery-key"] li')
    expect(items).toHaveLength(7)
    expect(items.map((li) => li.find('span:last-child').text())).toEqual(key.split('-').slice(1))
    expect(items[0]!.find('span').text()).toBe('1')
    expect(wrapper.find('[data-testid="extra"]').exists()).toBe(true)
  })

  it('copies the whole key to the clipboard, and says so', async () => {
    const writeText = vi.fn(() => Promise.resolve())
    vi.stubGlobal('navigator', { clipboard: { writeText } })
    const key = generateRecoveryKey().display
    const wrapper = mountWithHost(RecoveryKeyDisplay, { props: { recoveryKey: key } })
    await wrapper.find('[data-testid="copy-key"]').trigger('click')
    await flushPromises()
    expect(writeText).toHaveBeenCalledWith(key)
    expect(wrapper.find('[data-testid="copy-key"]').text()).toBe('Copied')

    await wrapper.setProps({ recoveryKey: generateRecoveryKey().display })
    expect(wrapper.find('[data-testid="copy-key"]').text()).toBe('Copy')
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
    await wrapper.find('[data-testid="gate-2"] input').setValue('aaaaa')
    await wrapper.find('[data-testid="gate-6"] input').setValue('bbbbb')
    await wrapper.find('form').trigger('submit')
    expect(wrapper.emitted('submit')).toEqual([[['aaaaa', 'bbbbb']]])
    expect(wrapper.find('[data-testid="gate-submit"]').text()).toBe('Go')
  })

  it('clears the answers when new groups are asked for', async () => {
    const wrapper = mountWithHost(RecoveryKeyGate, { props })
    await wrapper.find('[data-testid="gate-2"] input').setValue('aaaaa')
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
