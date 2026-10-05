import { describe, expect, it, vi } from 'vitest'
import { navExtensionPoint, navExtensionPointId, navExtensions } from './navigation'

const appId = 'backup-vault'
const gettext = (msgid: string) => `[${msgid}]`

function build(admin: () => boolean) {
  const can = vi.fn(() => admin())
  const items = navExtensions(appId, gettext, { can })
  const byId = (suffix: string) => items.find((e) => e.id === `app.${appId}.nav.${suffix}`)!
  return { can, items, spaces: byId('spaces'), destinations: byId('destinations') }
}

function nameOf(item: { navItem: { name: string | (() => string) } }): string {
  const { name } = item.navItem
  return typeof name === 'function' ? name() : name
}

describe('navigation', () => {
  it('registers on the extension point the host reads nav items from', () => {
    expect(navExtensionPointId(appId)).toBe('app.backup-vault.navItems')
    expect(navExtensionPoint(appId)).toEqual({
      id: 'app.backup-vault.navItems',
      extensionType: 'sidebarNav',
      multiple: true
    })
    const { items } = build(() => false)
    for (const item of items) {
      expect(item.type).toBe('sidebarNav')
      expect(item.extensionPointIds).toEqual(['app.backup-vault.navItems'])
    }
  })

  it('has stable ids, independent of the language', () => {
    expect(build(() => false).items.map((e) => e.id)).toEqual([
      'app.backup-vault.nav.spaces',
      'app.backup-vault.nav.destinations'
    ])
  })

  it('translates names when drawn, so a language change is followed', () => {
    const { spaces, destinations } = build(() => false)
    expect(nameOf(spaces)).toBe('[Spaces]')
    expect(nameOf(destinations)).toBe('[Backup destinations]')
  })

  it('keeps "Spaces" active on every page below a Space', () => {
    const { spaces } = build(() => false)
    expect(spaces.navItem.route).toEqual({ name: 'backup-vault-overview' })
    expect(spaces.navItem.activeFor).toEqual([{ path: '/backup-vault/space' }])
    expect(spaces.navItem.isVisible).toBeUndefined()
  })

  it('offers "Backup destinations" to admins only', () => {
    let admin = false
    const { can, destinations } = build(() => admin)
    expect(destinations.navItem.route).toEqual({ name: 'backup-vault-admin-targets' })
    expect(destinations.navItem.isVisible!()).toBe(false)
    expect(can).toHaveBeenCalledWith('read-all', 'Setting')

    // Asked again each time: the rules arrive after sign-in, not at setup.
    admin = true
    expect(destinations.navItem.isVisible!()).toBe(true)
  })

  it('orders "Spaces" before "Backup destinations"', () => {
    const { spaces, destinations } = build(() => true)
    expect(spaces.navItem.priority!).toBeLessThan(destinations.navItem.priority!)
  })
})
