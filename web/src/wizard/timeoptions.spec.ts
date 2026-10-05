import { describe, expect, it } from 'vitest'
import { selectedTime, timeLabel, timeOptions } from './timeoptions'

describe('timeLabel', () => {
  it('writes a time as HH:MM with leading zeros', () => {
    expect(timeLabel(2, 5)).toBe('02:05')
    expect(timeLabel(23, 30)).toBe('23:30')
  })
})

describe('timeOptions', () => {
  it('offers every half hour of the day, in order', () => {
    const options = timeOptions({ hour: 2, minute: 30 })
    expect(options).toHaveLength(48)
    expect(options[0]).toEqual({ hour: 0, minute: 0, label: '00:00' })
    expect(options[5]).toEqual({ hour: 2, minute: 30, label: '02:30' })
    expect(options.at(-1)).toEqual({ hour: 23, minute: 30, label: '23:30' })
  })

  it('keeps a stored time that is off the grid instead of rounding it', () => {
    const options = timeOptions({ hour: 2, minute: 15 })
    expect(options).toHaveLength(49)
    expect(options.map((o) => o.label).slice(4, 7)).toEqual(['02:00', '02:15', '02:30'])
  })
})

describe('selectedTime', () => {
  it('finds the entry for the current time, on or off the grid', () => {
    for (const current of [
      { hour: 2, minute: 30 },
      { hour: 7, minute: 45 }
    ]) {
      expect(selectedTime(timeOptions(current), current)).toMatchObject(current)
    }
  })

  it('finds nothing for a time that is not offered', () => {
    expect(selectedTime(timeOptions({ hour: 1, minute: 0 }), { hour: 1, minute: 10 })).toBe(
      undefined
    )
  })
})
