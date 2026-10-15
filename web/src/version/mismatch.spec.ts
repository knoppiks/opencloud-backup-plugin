import { describe, expect, it } from 'vitest'
import type { ServiceVersion } from '../api'
import { compareWithService, versionNotice, type BundleVersion } from './mismatch'

const $gettext = (msgid: string, params: Record<string, string> = {}) =>
  msgid.replace(/%\{(\w+)\}/g, (_, k: string) => params[k] ?? '')

function service(version: string, apiLevel = 1): ServiceVersion {
  return {
    version,
    api_level: apiLevel,
    opencloud: { known: true, in_window: true, supported: 'x' }
  }
}

const bundle = (version: string, apiLevel = 1): BundleVersion => ({ version, apiLevel })

describe('compareWithService', () => {
  it.each([
    ['same version, same level', 'v1.2.3', 1, 'v1.2.3', 1, 'none'],
    ['different version, same level', 'v1.2.4', 1, 'v1.2.3', 1, 'version'],
    ['different level', 'v1.2.3', 2, 'v1.2.3', 1, 'api_level'],
    ['service dev, same level', 'dev', 1, 'v1.2.3', 1, 'none'],
    ['bundle dev, same level', 'v1.2.3', 1, 'dev', 1, 'none'],
    ['both dev, different level', 'dev', 2, 'dev', 1, 'api_level']
  ])('%s', (_name, sv, sl, bv, bl, want) => {
    expect(compareWithService(service(sv, sl), bundle(bv, bl))).toBe(want)
  })
})

describe('versionNotice', () => {
  it('says nothing without an answer, or when they match', () => {
    expect(versionNotice(undefined, bundle('v1'), true, $gettext)).toBeUndefined()
    expect(versionNotice(service('v1'), bundle('v1'), true, $gettext)).toBeUndefined()
  })

  it('warns an admin about another API level, naming both versions', () => {
    const notice = versionNotice(service('v2.0.0', 2), bundle('v1.0.0'), true, $gettext)
    expect(notice?.tone).toBe('warning')
    expect(notice?.message).toContain('Backup Vault v1.0.0')
    expect(notice?.message).toContain('the backup service is v2.0.0')
  })

  it('tells a member calmly to reload, without versions', () => {
    const notice = versionNotice(service('v2.0.0', 2), bundle('v1.0.0'), false, $gettext)
    expect(notice?.tone).toBe('info')
    expect(notice?.title).toBe('')
    expect(notice?.message).toContain('reload the page')
    expect(notice?.message).not.toContain('v2.0.0')
  })

  it('informs an admin, and not a member, about a version difference alone', () => {
    const admin = versionNotice(service('v1.0.1'), bundle('v1.0.0'), true, $gettext)
    expect(admin?.tone).toBe('info')
    expect(admin?.message).toContain('run them at the same version')
    expect(versionNotice(service('v1.0.1'), bundle('v1.0.0'), false, $gettext)).toBeUndefined()
  })
})
