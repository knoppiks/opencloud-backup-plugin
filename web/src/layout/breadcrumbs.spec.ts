import { describe, expect, it } from 'vitest'
import {
  destinationsCrumbs,
  pageTitle,
  recoveryKeyCrumb,
  spaceCrumbs,
  spacesCrumbs
} from './breadcrumbs'

const t = (msgid: string) => msgid
const overview = { name: 'backup-vault-overview' }
const space = { name: 'backup-vault-space', params: { spaceId: 's$1!1' } }

describe('breadcrumbs', () => {
  it('makes the overview the current page or an ancestor', () => {
    expect(spacesCrumbs(t, true)).toEqual([{ text: 'Spaces' }])
    expect(spacesCrumbs(t)).toEqual([{ text: 'Spaces', to: overview }])
  })

  it('ends at the Space when it is the current page', () => {
    expect(spaceCrumbs(t, 's$1!1', 'Family photos')).toEqual([
      { text: 'Spaces', to: overview },
      { text: 'Family photos' }
    ])
  })

  it('links the Space when a page below it is current', () => {
    expect(spaceCrumbs(t, 's$1!1', 'Family photos', { text: 'Restore files' })).toEqual([
      { text: 'Spaces', to: overview },
      { text: 'Family photos', to: space },
      { text: 'Restore files' }
    ])
  })

  it('stands in a neutral word until the Space name has loaded', () => {
    expect(spaceCrumbs(t, 's$1!1', undefined)[1]).toEqual({ text: 'Space' })
    expect(spaceCrumbs(t, 's$1!1', '')[1]).toEqual({ text: 'Space' })
  })

  it('nests the replacement below the Recovery Key page', () => {
    const crumbs = spaceCrumbs(t, 's$1!1', 'Home', recoveryKeyCrumb(t, 's$1!1'), {
      text: 'Replace the Recovery Key'
    })
    expect(crumbs[2]).toEqual({
      text: 'Recovery Key',
      to: { name: 'backup-vault-recovery-key', params: { spaceId: 's$1!1' } }
    })
    expect(recoveryKeyCrumb(t, 's$1!1', true)).toEqual({ text: 'Recovery Key' })
  })

  it('builds the destination trail', () => {
    expect(destinationsCrumbs(t)).toEqual([{ text: 'Backup destinations' }])
    expect(destinationsCrumbs(t, { text: 'Buddy' })).toEqual([
      { text: 'Backup destinations', to: { name: 'backup-vault-admin-targets' } },
      { text: 'Buddy' }
    ])
  })

  it('takes the page title from the last crumb', () => {
    expect(pageTitle(spaceCrumbs(t, 's', 'Home', { text: 'Set up backup' }))).toBe('Set up backup')
    expect(pageTitle([])).toBe('')
  })

  it('never puts anything but the ids into a route', () => {
    const routes = [
      ...spaceCrumbs(t, 's$1!1', 'Home', recoveryKeyCrumb(t, 's$1!1'), { text: 'x' }),
      ...destinationsCrumbs(t, { text: 'y' })
    ]
      .map((c) => c.to)
      .filter(Boolean)
    for (const to of routes) {
      expect(Object.keys(to as object).sort()).toEqual(
        'params' in (to as object) ? ['name', 'params'] : ['name']
      )
    }
  })
})
