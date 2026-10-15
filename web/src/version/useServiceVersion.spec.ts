import { flushPromises } from '@vue/test-utils'
import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { ServiceVersion } from '../api'
import { APP_VERSION } from './appversion'
import { forgetServiceVersion, loadServiceVersion, useServiceVersion } from './useServiceVersion'

const answer: ServiceVersion = {
  version: 'v1.2.3',
  api_level: 1,
  opencloud: { known: true, in_window: true, supported: 'x' }
}

beforeEach(() => forgetServiceVersion())

describe('useServiceVersion', () => {
  it('asks once per page load, however many pages ask', async () => {
    const source = { serviceVersion: vi.fn(async () => answer) }
    const first = useServiceVersion(source)
    const second = useServiceVersion(source)
    expect(first.value).toBeUndefined()
    await flushPromises()

    expect(first.value).toEqual(answer)
    expect(second.value).toEqual(answer)
    expect(await loadServiceVersion(source)).toEqual(answer)
    expect(source.serviceVersion).toHaveBeenCalledTimes(1)
  })

  it('answers undefined when the service does not, and does not ask again', async () => {
    const source = { serviceVersion: vi.fn(async () => Promise.reject(new Error('503'))) }
    const version = useServiceVersion(source)
    await flushPromises()

    expect(version.value).toBeUndefined()
    await loadServiceVersion(source)
    expect(source.serviceVersion).toHaveBeenCalledTimes(1)
  })
})

describe('APP_VERSION', () => {
  it('is "dev" in an unstamped build', () => {
    expect(APP_VERSION).toBe('dev')
  })
})
