import { describe, expect, it } from 'vitest'
import { ApiError, asApiError, mayHaveLanded } from './errors'

describe('mayHaveLanded', () => {
  it('is true when nothing answered or the server failed', () => {
    expect(mayHaveLanded(new ApiError('offline', 'x'))).toBe(true)
    expect(mayHaveLanded(new ApiError('timeout', 'x'))).toBe(true)
    expect(mayHaveLanded(new ApiError('internal_error', 'x', 500))).toBe(true)
    expect(mayHaveLanded(new ApiError('unavailable', 'x', 503))).toBe(true)
  })

  it('is false for a refusal, which means nothing was done', () => {
    expect(mayHaveLanded(new ApiError('bad_request', 'x', 400))).toBe(false)
    expect(mayHaveLanded(new ApiError('forbidden', 'x', 403))).toBe(false)
    expect(mayHaveLanded(new ApiError('conflict', 'x', 409))).toBe(false)
  })
})

describe('asApiError', () => {
  it('keeps an ApiError as it is', () => {
    const err = new ApiError('forbidden', 'x', 403)
    expect(asApiError(err)).toBe(err)
  })

  it('turns anything else into a generic error that repeats none of it', () => {
    const err = asApiError(new Error('secret detail'))
    expect(err.code).toBe('unknown')
    expect(JSON.stringify(err) + err.message).not.toContain('secret detail')
  })
})
