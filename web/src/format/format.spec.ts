import { describe, expect, it } from 'vitest'
import { formatBytes, formatCount, formatWhen, type FormatContext } from './format'

/** echo is a translator that shows which msgid was chosen and with what. */
const echo = (msgid: string, params?: Record<string, string>) =>
  params ? `${msgid}|${JSON.stringify(params)}` : msgid

// A fixed local noon, so "today" and "yesterday" never straddle midnight
// whatever the timezone the test runs in.
const now = new Date(2026, 8, 24, 12, 0, 0)
const ctx: FormatContext = { now, locale: 'en-GB', $gettext: echo }

function localIso(day: number, hour: number, minute = 0): string {
  return new Date(2026, 8, day, hour, minute).toISOString()
}

describe('formatWhen', () => {
  it('says today, yesterday and tomorrow with the local time', () => {
    expect(formatWhen(localIso(24, 3), ctx)).toBe('Today, %{time}|{"time":"03:00"}')
    expect(formatWhen(localIso(23, 3), ctx)).toBe('Yesterday, %{time}|{"time":"03:00"}')
    expect(formatWhen(localIso(25, 2, 30), ctx)).toBe('Tomorrow, %{time}|{"time":"02:30"}')
  })

  // Calendar days, not 24-hour blocks: a minute before midnight yesterday was
  // yesterday, even when it was only twelve hours ago.
  it('counts calendar days, not elapsed hours', () => {
    expect(formatWhen(localIso(23, 23, 59), ctx)).toContain('Yesterday')
    expect(formatWhen(localIso(24, 0, 1), ctx)).toContain('Today')
  })

  it('falls back to a full date further away', () => {
    const out = formatWhen(localIso(20, 3), ctx)
    expect(out).not.toContain('%{time}')
    expect(out).toContain('2026')
  })

  it('returns an unparseable value unchanged rather than "Invalid Date"', () => {
    expect(formatWhen('not a date', ctx)).toBe('not a date')
  })
})

describe('formatBytes', () => {
  it('scales to a decimal unit', () => {
    expect(formatBytes(0, 'en')).toBe('0 byte')
    expect(formatBytes(999, 'en')).toBe('999 byte')
    expect(formatBytes(1500, 'en')).toBe('1.5 kB')
    expect(formatBytes(2_300_000_000, 'en')).toBe('2.3 GB')
  })

  it('follows the locale', () => {
    expect(formatBytes(1500, 'de')).toBe('1,5 kB')
  })

  it('never goes negative', () => {
    expect(formatBytes(-5, 'en')).toBe('0 byte')
  })
})

describe('formatCount', () => {
  it('groups digits per locale', () => {
    expect(formatCount(12345, 'en')).toBe('12,345')
    expect(formatCount(12345, 'de')).toBe('12.345')
  })
})
