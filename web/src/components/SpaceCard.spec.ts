import { describe, expect, it } from 'vitest'
import { ApiError } from '../api'
import { translations } from '../l10n/translations'
import { job, space, status } from '../test/fixtures'
import { mountWithHost } from '../test/host'
import SpaceCard from './SpaceCard.vue'

function card(props: Partial<InstanceType<typeof SpaceCard>['$props']>, language?: string) {
  return mountWithHost(SpaceCard, {
    props: { space: space(), status: undefined, error: undefined, ...props },
    ...(language ? { language, translations } : {})
  })
}

describe('SpaceCard', () => {
  it('says a healthy Space is protected, and since when', () => {
    const wrapper = card({ status: status() })
    expect(wrapper.attributes('data-state')).toBe('active')
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Protected')
    expect(wrapper.find('[data-testid="state-summary"]').text()).toMatch(/^Last backup /)
  })

  it('names the date backups stopped for a stale Space', () => {
    const wrapper = card({ status: status({ stale: true, stale_since: '2026-09-01T01:30:00Z' }) })
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Backups have stopped')
    expect(wrapper.find('[data-testid="state-summary"]').text()).toMatch(
      /^No successful backup since .*2026/
    )
  })

  it('reassures that earlier backups survive a failure', () => {
    const wrapper = card({
      status: status({ last_run: job({ state: 'failed' }), last_error: 'target unavailable' })
    })
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Last backup failed')
    expect(wrapper.find('[data-testid="state-summary"]').text()).toMatch(/^Last successful backup/)
  })

  it('shows a running job with its start', () => {
    const wrapper = card({
      status: status({ running: true, current_job: job({ state: 'running' }) })
    })
    expect(wrapper.find('[data-testid="state-summary"]').text()).toMatch(/^Running since /)
  })

  it('explains a Space nobody set up', () => {
    const wrapper = card({ status: status({ configured: false, keys_configured: false }) })
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe('Not backed up')
    expect(wrapper.find('[data-testid="state-summary"]').text()).toBe(
      'Nothing in this space is being backed up.'
    )
  })

  it('shows its own error instead of a state', () => {
    const wrapper = card({ error: new ApiError('forbidden', 'x', 403) })
    expect(wrapper.attributes('data-state')).toBe('error')
    expect(wrapper.find('[role="alert"]').text()).toBe('You do not have access to this')
  })

  it('distinguishes personal and shared Spaces', () => {
    expect(card({ space: space({ type: 'personal' }) }).text()).toContain('Personal space')
    expect(card({ space: space({ type: 'project' }) }).text()).toContain('Shared space')
  })

  // The catalogue is wired, not merely present: a German session renders German.
  it('renders in German', () => {
    const wrapper = card({ status: status() }, 'de')
    expect(wrapper.find('[data-testid="state-label"]').text()).toBe(translations.de!['Protected'])
  })
})

describe('SpaceCard setup link', () => {
  const unset = { configured: false, keys_configured: false, enabled: false }

  it('links an editor to the wizard for a Space nobody has set up', () => {
    const wrapper = card({ status: status(unset), space: space({ role: 'editor' }) })
    const link = wrapper.find('[data-testid="setup-action"]')
    expect(link.text()).toBe('Set up backup')
    expect(JSON.parse(link.attributes('data-to') as string)).toMatchObject({
      name: 'backup-vault-setup'
    })
  })

  it('shows a viewer no link', () => {
    const wrapper = card({ status: status(unset), space: space({ role: 'viewer' }) })
    expect(wrapper.find('[data-testid="setup-action"]').exists()).toBe(false)
  })

  it('shows an editor no dead-end link when only a manager can continue', () => {
    const wrapper = card({
      status: status({ keys_configured: false, enabled: false }),
      space: space({ role: 'editor' })
    })
    expect(wrapper.find('[data-testid="setup-action"]').exists()).toBe(false)
  })

  it('shows no link on a protected Space', () => {
    const wrapper = card({ status: status(), space: space({ role: 'owner' }) })
    expect(wrapper.find('[data-testid="setup-action"]').exists()).toBe(false)
  })
})
