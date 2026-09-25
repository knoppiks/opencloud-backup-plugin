// The format helpers, bound to the current language and clock.

import { useGettext } from 'vue3-gettext'
import { formatBytes, formatCount, formatWhen } from '../format/format'

/** Format is what a template needs to render dates and sizes. */
export interface Format {
  when: (iso: string) => string
  bytes: (count: number) => string
  count: (count: number) => string
}

/**
 * useFormat binds the pure format helpers to the user's language. "Now" is read
 * at each call rather than captured, so a board left open past midnight says
 * "Yesterday" for last night's run on its next render.
 */
export function useFormat(): Format {
  const gettext = useGettext()
  const locale = () => gettext.current || 'en'
  return {
    when: (iso) =>
      formatWhen(iso, { now: new Date(), locale: locale(), $gettext: gettext.$gettext }),
    bytes: (count) => formatBytes(count, locale()),
    count: (count) => formatCount(count, locale())
  }
}
