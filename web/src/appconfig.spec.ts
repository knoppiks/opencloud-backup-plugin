import { beforeEach, describe, expect, it } from 'vitest'
import { apiBaseUrl, configureApi, resetApiConfigForTests } from './appconfig'
import { ApiPathError, DEFAULT_API_PATH } from './api'

const ORIGIN = 'https://cloud.example.org'

describe('app configuration', () => {
  beforeEach(resetApiConfigForTests)

  it('falls back to the default path when the host passes no config', () => {
    expect(configureApi({}, ORIGIN)).toBe(`${ORIGIN}${DEFAULT_API_PATH}`)
    expect(apiBaseUrl()).toBe(`${ORIGIN}${DEFAULT_API_PATH}`)
  })

  it('honours an apiPath from the manifest', () => {
    expect(configureApi({ apiPath: '/api/v1' }, ORIGIN)).toBe(`${ORIGIN}/api/v1`)
  })

  // The failure has to arrive at app setup, not on the first request: an
  // extension that loads and then fails everything looks like a broken service.
  it('throws on a misconfigured apiPath', () => {
    expect(() => configureApi({ apiPath: 'https://elsewhere.example/api' }, ORIGIN)).toThrow(
      ApiPathError
    )
  })

  it('refuses to hand out a base URL before it is configured', () => {
    expect(() => apiBaseUrl()).toThrow(/not configured/)
  })
})
