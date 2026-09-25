import { describe, expect, it } from 'vitest'
import { ApiPathError, DEFAULT_API_PATH, resolveApiBase } from './baseurl'

const ORIGIN = 'https://cloud.example.org'

describe('resolveApiBase', () => {
  it('defaults to the path the shipped manifest routes', () => {
    expect(resolveApiBase(undefined, ORIGIN)).toBe(`${ORIGIN}${DEFAULT_API_PATH}`)
  })

  it('keeps the default in step with the service', () => {
    // BACKUPD_BASE_PATH is "/backup" in deploy/deployment-backupd.yaml and the
    // routes beneath it are "/api/v1/...". If one side moves, both must.
    expect(DEFAULT_API_PATH).toBe('/backup/api/v1')
  })

  it.each([
    ['/backup/api/v1', `${ORIGIN}/backup/api/v1`],
    ['/api/v1', `${ORIGIN}/api/v1`],
    ['/backup/api/v1/', `${ORIGIN}/backup/api/v1`],
    ['  /backup/api/v1  ', `${ORIGIN}/backup/api/v1`],
    ['/', ORIGIN]
  ])('resolves %j against the page origin', (path, want) => {
    expect(resolveApiBase(path, ORIGIN)).toBe(want)
  })

  it('does not care which origin the page is on', () => {
    expect(resolveApiBase('/backup/api/v1', 'http://localhost:9200')).toBe(
      'http://localhost:9200/backup/api/v1'
    )
  })

  // The whole reason this module exists. The service ships no CORS and no
  // OPTIONS route on purpose; a config value must not be able to point the
  // client at another origin, because the Data Key crosses this API at setup.
  it.each([
    'https://backup.example.org/api/v1',
    'http://backup.example.org/api/v1',
    '//backup.example.org/api/v1',
    'https://cloud.example.org/api/v1'
  ])('refuses %j because it names an origin', (path) => {
    expect(() => resolveApiBase(path, ORIGIN)).toThrow(ApiPathError)
  })

  it.each([
    ['', 'empty'],
    ['   ', 'empty'],
    ['backup/api/v1', 'must start'],
    ['/backup/api/v1?token=x', 'query or fragment'],
    ['/backup/api/v1#x', 'query or fragment']
  ])('refuses %j', (path, reason) => {
    expect(() => resolveApiBase(path, ORIGIN)).toThrow(new RegExp(reason))
  })

  it('names the offending value so a deployment mistake is fixable', () => {
    expect(() => resolveApiBase('backup', ORIGIN)).toThrow(/"backup"/)
  })
})
