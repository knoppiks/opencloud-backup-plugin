import { describe, expect, it } from 'vitest'
import { h } from 'vue'
import { ApiError } from '../api'
import { stateLook, toneClasses, toneIcon, type Tone } from '../layout/tone'
import type { SpaceState } from '../status/spacestate'
import { needsAttention } from '../status/spacestate'
import { mountWithHost } from '../test/host'
import ActionError from './ActionError.vue'
import EmptyState from './EmptyState.vue'
import NoticeBanner from './NoticeBanner.vue'
import StatusTag from './StatusTag.vue'

const STATES: SpaceState[] = [
  'not_set_up',
  'setup_incomplete',
  'running',
  'failed',
  'stale',
  'paused',
  'waiting',
  'active'
]
const TONES: Tone[] = ['success', 'info', 'neutral', 'warning', 'danger']

describe('tones', () => {
  it('draws every tone from the theme roles, never a fixed colour', () => {
    for (const tone of TONES) {
      expect(toneClasses(tone)).toMatch(/^ext:bg-role-\S+ ext:text-role-on-\S+$/)
      expect(toneIcon(tone)).toBeTruthy()
    }
  })

  it('gives every state an icon, so colour is never the only cue', () => {
    for (const state of STATES) {
      expect(stateLook(state).icon).toBeTruthy()
    }
  })

  it('marks exactly the states that need attention as warning or danger', () => {
    for (const state of STATES) {
      const serious = ['warning', 'danger'].includes(stateLook(state).tone)
      expect(serious, state).toBe(needsAttention(state))
    }
  })

  it('calls only a Space whose last backup succeeded a success', () => {
    expect(STATES.filter((s) => stateLook(s).tone === 'success')).toEqual(['active'])
  })
})

describe('StatusTag', () => {
  it('shows icon and words for the state', () => {
    const wrapper = mountWithHost(StatusTag, { props: { state: 'failed' } })
    expect(wrapper.text()).toBe('Last backup failed')
    expect(wrapper.attributes('data-tone')).toBe('danger')
    expect(wrapper.find('[data-icon="error-warning"]').exists()).toBe(true)
  })
})

describe('NoticeBanner', () => {
  it('interrupts for danger and waits politely otherwise', () => {
    const danger = mountWithHost(NoticeBanner, { props: { tone: 'danger', title: 'x' } })
    const warning = mountWithHost(NoticeBanner, { props: { tone: 'warning', title: 'x' } })
    expect(danger.attributes('role')).toBe('alert')
    expect(warning.attributes('role')).toBe('status')
  })

  it('shows title, message, content and actions', () => {
    const wrapper = mountWithHost(NoticeBanner, {
      props: { title: 'Title', message: 'Message' },
      slots: { default: () => h('em', 'more'), actions: () => h('button', 'Act') }
    })
    expect(wrapper.find('[data-testid="notice-title"]').text()).toBe('Title')
    expect(wrapper.find('[data-testid="notice-message"]').text()).toBe('Message')
    expect(wrapper.find('em').text()).toBe('more')
    expect(wrapper.find('button').text()).toBe('Act')
  })

  it('leaves out an empty message', () => {
    const wrapper = mountWithHost(NoticeBanner, { props: { title: 'Title' } })
    expect(wrapper.find('[data-testid="notice-message"]').exists()).toBe(false)
  })

  it('says a one-sentence notice as its message, without a title', () => {
    const wrapper = mountWithHost(NoticeBanner, { props: { message: 'Only this.' } })
    expect(wrapper.find('[data-testid="notice-title"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="notice-message"]').text()).toBe('Only this.')
  })
})

describe('ActionError', () => {
  it('is a danger notice with the shared wording and the service’s words', () => {
    const error = new ApiError('offline', 'x', undefined, 'dial tcp: refused')
    const wrapper = mountWithHost(ActionError, { props: { error } })
    expect(wrapper.attributes('role')).toBe('alert')
    expect(wrapper.attributes('data-testid')).toBe('action-error')
    expect(wrapper.find('[data-testid="notice-title"]').text()).toBe(
      'The backup service cannot be reached'
    )
    expect(wrapper.text()).toContain('dial tcp: refused')
  })
})

describe('EmptyState', () => {
  it('shows the message and what to do next', () => {
    const wrapper = mountWithHost(EmptyState, {
      props: { message: 'Nothing here', icon: 'server' },
      slots: { default: () => h('button', 'Add') }
    })
    expect(wrapper.find('p').text()).toBe('Nothing here')
    expect(wrapper.find('[data-icon="server"]').exists()).toBe(true)
    expect(wrapper.find('button').text()).toBe('Add')
  })
})
