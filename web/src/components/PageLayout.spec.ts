import { describe, expect, it } from 'vitest'
import { h } from 'vue'
import { mountWithHost } from '../test/host'
import PageLayout from './PageLayout.vue'

const crumbs = [{ text: 'Spaces', to: { name: 'backup-vault-overview' } }, { text: 'Home' }]

describe('PageLayout', () => {
  it('shows the trail and repeats its last crumb as the h1', () => {
    const wrapper = mountWithHost(PageLayout, { props: { crumbs } })
    expect(wrapper.find('h1').text()).toBe('Home')
    expect(wrapper.find('[data-testid="breadcrumb"] a').text()).toBe('Spaces')
    expect(wrapper.find('[aria-current="page"]').text()).toBe('Home')
  })

  it('places status, actions and content where they belong', () => {
    const wrapper = mountWithHost(PageLayout, {
      props: { crumbs },
      slots: {
        status: () => h('span', { 'data-testid': 's' }, 'Protected'),
        actions: () => h('button', 'Back up now'),
        default: () => h('p', { 'data-testid': 'c' }, 'content')
      }
    })
    const header = wrapper.find('[data-testid="page-header"]')
    expect(header.find('[data-testid="s"]').exists()).toBe(true)
    expect(header.find('[data-testid="page-actions"] button').text()).toBe('Back up now')
    expect(header.find('[data-testid="c"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="c"]').exists()).toBe(true)
  })

  it('leaves out the actions area when there are none', () => {
    const wrapper = mountWithHost(PageLayout, { props: { crumbs } })
    expect(wrapper.find('[data-testid="page-actions"]').exists()).toBe(false)
  })

  it('caps the content width only when narrow', () => {
    const wide = mountWithHost(PageLayout, { props: { crumbs } })
    const narrow = mountWithHost(PageLayout, { props: { crumbs, narrow: true } })
    expect(wide.find('.ext\\:max-w-2xl').exists()).toBe(false)
    expect(narrow.find('.ext\\:max-w-2xl').exists()).toBe(true)
  })
})
