import { describe, expect, it } from 'vitest'
import type { AdminTarget } from '../api'
import {
  buildTargetRequest,
  emptyCredentials,
  emptySettings,
  hasProblems,
  pairState,
  settingsFromTarget,
  validateTarget,
  type CredentialsInput,
  type TargetSettings
} from './targetform'

function settings(overrides: Partial<TargetSettings> = {}): TargetSettings {
  return { ...emptySettings(), name: 'Buddy', endpoint: 'buddy:3900', bucket: 'b', ...overrides }
}

function credentials(
  backup: [string, string] = ['backup-id', 'backup-secret'],
  maintenance: [string, string] = ['', '']
): CredentialsInput {
  return {
    backup: { accessKeyId: backup[0], secretAccessKey: backup[1] },
    maintenance: { accessKeyId: maintenance[0], secretAccessKey: maintenance[1] }
  }
}

describe('settingsFromTarget', () => {
  it('prefills every setting and defaults the optional ones to empty', () => {
    const target: AdminTarget = {
      id: 't',
      name: 'Buddy',
      endpoint: 'buddy:3900',
      bucket: 'b',
      use_path_style: true,
      disable_tls: true,
      maintenance_configured: true
    }
    expect(settingsFromTarget(target)).toEqual({
      name: 'Buddy',
      endpoint: 'buddy:3900',
      bucket: 'b',
      region: '',
      prefix: '',
      usePathStyle: true,
      disableTls: true
    })
  })
})

describe('pairState', () => {
  it.each([
    ['', '', 'empty'],
    ['  ', '', 'empty'],
    ['id', '', 'partial'],
    ['', 'secret', 'partial'],
    ['id', 'secret', 'complete'],
    // The secret is not trimmed, by the server or here: a space is a character.
    ['id', ' ', 'complete']
  ])('%j / %j is %s', (accessKeyId, secretAccessKey, state) => {
    expect(pairState({ accessKeyId, secretAccessKey })).toBe(state)
  })
})

describe('validateTarget', () => {
  it('requires name, endpoint and bucket, ignoring surrounding space', () => {
    expect(validateTarget(settings({ name: ' ', endpoint: '', bucket: '' }), undefined)).toEqual({
      name: 'required',
      endpoint: 'required',
      bucket: 'required'
    })
  })

  it('checks no keys when they are not being replaced', () => {
    expect(hasProblems(validateTarget(settings(), undefined))).toBe(false)
  })

  it('requires the whole backup pair when keys are being entered', () => {
    expect(validateTarget(settings(), emptyCredentials())).toEqual({
      backupAccessKeyId: 'required',
      backupSecretAccessKey: 'required'
    })
    expect(validateTarget(settings(), credentials(['id', '']))).toEqual({
      backupSecretAccessKey: 'required'
    })
  })

  it('accepts an empty maintenance pair: both roles then use the backup pair', () => {
    expect(hasProblems(validateTarget(settings(), credentials()))).toBe(false)
  })

  it('refuses half a maintenance pair rather than dropping it', () => {
    expect(validateTarget(settings(), credentials(undefined, ['m-id', '']))).toEqual({
      maintenanceSecretAccessKey: 'required'
    })
    expect(validateTarget(settings(), credentials(undefined, ['', 'm-secret']))).toEqual({
      maintenanceAccessKeyId: 'required'
    })
  })
})

describe('buildTargetRequest', () => {
  it('sends no key pair at all when keys are not being replaced', () => {
    const body = buildTargetRequest(settings({ usePathStyle: true }), undefined)
    expect(body).toEqual({
      name: 'Buddy',
      endpoint: 'buddy:3900',
      bucket: 'b',
      region: '',
      prefix: '',
      use_path_style: true,
      disable_tls: false
    })
    expect(body).not.toHaveProperty('credentials')
    expect(body).not.toHaveProperty('maintenance_credentials')
  })

  it('sends the backup pair, trimming the id and never the secret', () => {
    const body = buildTargetRequest(settings(), credentials([' id ', ' secret ']))
    expect(body.credentials).toEqual({ access_key_id: 'id', secret_access_key: ' secret ' })
    expect(body).not.toHaveProperty('maintenance_credentials')
  })

  it('sends a complete maintenance pair', () => {
    const body = buildTargetRequest(settings(), credentials(undefined, ['m-id', 'm-secret']))
    expect(body.maintenance_credentials).toEqual({
      access_key_id: 'm-id',
      secret_access_key: 'm-secret'
    })
  })
})
