// Human-readable dates and sizes.
//
// The phase plan asks for "human dates: Yesterday 03:00". Timestamps on the
// wire are UTC instants, and they are shown in the browser's own timezone:
// "when did my backup run" is a question about the reader's clock. (Schedule
// *times* are a different matter — they are in the service's zone — and are
// not formatted here.)
//
// Pure functions with the clock, the locale and the translator passed in, so
// they are testable without a DOM and without Vue.

import type { Gettext } from '../api/errortext'

/** FormatContext is what formatting depends on besides the value. */
export interface FormatContext {
  now: Date
  /** locale is a BCP 47 tag, e.g. "de" or "en-GB". */
  locale: string
  $gettext: Gettext
}

/**
 * formatWhen renders an instant relative to today where that reads better:
 * "Today, 03:00", "Yesterday, 03:00", "Tomorrow, 02:30", and a plain date and
 * time otherwise. An unparseable value is returned as given rather than as
 * "Invalid Date".
 */
export function formatWhen(iso: string, ctx: FormatContext): string {
  const at = new Date(iso)
  if (Number.isNaN(at.getTime())) {
    return iso
  }
  const time = new Intl.DateTimeFormat(ctx.locale, { timeStyle: 'short' }).format(at)
  switch (calendarDaysBetween(ctx.now, at)) {
    case 0:
      return ctx.$gettext('Today, %{time}', { time })
    case -1:
      return ctx.$gettext('Yesterday, %{time}', { time })
    case 1:
      return ctx.$gettext('Tomorrow, %{time}', { time })
    default:
      return new Intl.DateTimeFormat(ctx.locale, {
        dateStyle: 'medium',
        timeStyle: 'short'
      }).format(at)
  }
}

/**
 * calendarDaysBetween counts local calendar days from `from` to `to`: 0 for
 * the same day, -1 for the day before. Midnight-to-midnight, not 24-hour
 * blocks, so 23:59 yesterday is "yesterday" even one minute ago.
 */
function calendarDaysBetween(from: Date, to: Date): number {
  const start = new Date(from.getFullYear(), from.getMonth(), from.getDate())
  const end = new Date(to.getFullYear(), to.getMonth(), to.getDate())
  // Rounded, because a day that contains a DST switch is 23 or 25 hours long.
  return Math.round((end.getTime() - start.getTime()) / 86_400_000)
}

const byteUnits = ['byte', 'kilobyte', 'megabyte', 'gigabyte', 'terabyte'] as const

/** formatBytes renders a byte count with a decimal unit ("1.5 GB"). */
export function formatBytes(bytes: number, locale: string): string {
  let value = Math.max(0, bytes)
  let unit = 0
  while (value >= 1000 && unit < byteUnits.length - 1) {
    value /= 1000
    unit++
  }
  return new Intl.NumberFormat(locale, {
    style: 'unit',
    unit: byteUnits[unit],
    unitDisplay: 'short',
    maximumFractionDigits: unit === 0 ? 0 : 1
  }).format(value)
}

/** formatCount renders a whole number with the locale's grouping. */
export function formatCount(count: number, locale: string): string {
  return new Intl.NumberFormat(locale).format(count)
}
