import { describe, expect, it } from 'vitest'
import { ApiError, type ApiFailureCode } from '../api'
import { mountWithHost } from '../test/host'
import RequestState from './RequestState.vue'

function state(props: {
  loading?: boolean
  error?: ApiError
  empty?: boolean
  emptyMessage?: string
}) {
  return mountWithHost(RequestState, {
    props: { loading: false, error: undefined, ...props },
    slots: { default: '<p class="content">content</p>' }
  })
}

describe('RequestState', () => {
  it('renders the content when there is nothing to report', () => {
    expect(state({}).find('.content').exists()).toBe(true)
  })

  it('shows loading instead of the content', () => {
    const wrapper = state({ loading: true })
    expect(wrapper.find('.spinner').exists()).toBe(true)
    expect(wrapper.find('.content').exists()).toBe(false)
  })

  it('shows an empty message instead of the content', () => {
    const wrapper = state({ empty: true, emptyMessage: 'Nothing here' })
    expect(wrapper.text()).toBe('Nothing here')
  })

  // A retry button next to a failure that cannot change is a lie.
  it.each<[ApiFailureCode, boolean]>([
    ['offline', true],
    ['timeout', true],
    ['unavailable', true],
    ['upstream_error', true],
    ['forbidden', false],
    ['unauthorized', false],
    ['not_found', false],
    ['internal_error', false]
  ])('%s: retry offered = %s', (code, retry) => {
    const wrapper = state({ error: new ApiError(code, 'x') })
    expect(wrapper.find('[role="alert"]').exists()).toBe(true)
    expect(wrapper.find('[role="alert"] button').exists()).toBe(retry)
  })

  it('emits retry', async () => {
    const wrapper = state({ error: new ApiError('offline', 'x') })
    await wrapper.find('[role="alert"] button').trigger('click')
    expect(wrapper.emitted('retry')).toHaveLength(1)
  })

  // The server's message is a detail, never the headline: it is English and
  // cannot be translated.
  it('shows the server message as a detail under a translated title', () => {
    const wrapper = state({
      error: new ApiError('bad_request', 'x', 400, 'retention_days must be at least 7')
    })
    expect(wrapper.find('h2').text()).toBe('Something went wrong')
    expect(wrapper.text()).toContain('retention_days must be at least 7')
  })
})
