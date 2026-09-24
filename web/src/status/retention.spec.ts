import { describe, expect, it } from 'vitest'
import { MIN_RETENTION_DAYS, parseRetentionDays } from './retention'

describe('parseRetentionDays', () => {
  it('accepts whole days at or above the floor', () => {
    expect(parseRetentionDays(String(MIN_RETENTION_DAYS))).toEqual({ days: 7 })
    expect(parseRetentionDays(' 90 ')).toEqual({ days: 90 })
    expect(parseRetentionDays('3650')).toEqual({ days: 3650 })
  })

  // The floor is the ransomware defence (decisions.md #22); the UI explains it
  // before the server has to refuse it.
  it('refuses anything below the floor', () => {
    expect(parseRetentionDays('6')).toEqual({ problem: 'below_floor' })
    expect(parseRetentionDays('0')).toEqual({ problem: 'below_floor' })
  })

  it('refuses what is not a whole number of days', () => {
    for (const input of ['', 'abc', '7.5', '-10', '1e3', '٧']) {
      expect(parseRetentionDays(input)).toEqual({ problem: 'not_a_number' })
    }
  })
})
