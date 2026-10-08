import { describe, expect, it } from 'vitest'
import type { OpenCloudVersion } from '../api'
import { openCloudNotice } from './compat'

const $gettext = (msgid: string, params: Record<string, string> = {}) =>
  msgid.replace(/%\{(\w+)\}/g, (_, k: string) => params[k] ?? '')

const supported = '7.3.0 to 8.1.0'

function version(overrides: Partial<OpenCloudVersion>): OpenCloudVersion {
  return {
    known: true,
    version: '8.1.0',
    edition: 'rolling',
    in_window: true,
    supported,
    ...overrides
  }
}

describe('openCloudNotice', () => {
  it.each([
    ['no answer', undefined],
    ['not known yet', { known: false, in_window: false, supported }],
    ['inside the window', version({})]
  ])('says nothing: %s', (_name, v) => {
    expect(openCloudNotice(v, $gettext)).toBeUndefined()
  })

  it('names the running version and the tested range outside the window', () => {
    const notice = openCloudNotice(version({ version: '9.0.0', in_window: false }), $gettext)
    expect(notice?.title).toContain('not been tested')
    expect(notice?.message).toContain('OpenCloud 9.0.0 is running')
    expect(notice?.message).toContain(supported)
  })

  // The policy is "warn and run": the notice must not suggest backups stopped.
  it('says backups continue', () => {
    const notice = openCloudNotice(version({ version: '9.0.0', in_window: false }), $gettext)
    expect(notice?.message).toContain('Backups continue')
  })
})
