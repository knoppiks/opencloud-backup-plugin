// The times a backup can be scheduled at, as dropdown options (Phase 8g).
//
// The design system has no time field, so the time is picked from a list:
// every half hour of the day. A time the server already has that is not on
// that grid (set by an earlier version, or through the API) is added to the
// list rather than rounded, so opening the wizard never changes it silently.

/** TIME_STEP_MINUTES is the spacing of the offered times. */
export const TIME_STEP_MINUTES = 30

/** TimeOption is one entry of the time dropdown. */
export interface TimeOption {
  hour: number
  minute: number
  /** label is "HH:MM", 24-hour, as the server's schedule is written. */
  label: string
}

/** timeLabel writes a time as "HH:MM". */
export function timeLabel(hour: number, minute: number): string {
  return `${String(hour).padStart(2, '0')}:${String(minute).padStart(2, '0')}`
}

function option(hour: number, minute: number): TimeOption {
  return { hour, minute, label: timeLabel(hour, minute) }
}

/**
 * timeOptions is the grid of the day, plus `current` where it is off the
 * grid, in order. The entry for `current` is always in the list.
 */
export function timeOptions(current: { hour: number; minute: number }): TimeOption[] {
  const grid = Array.from({ length: (24 * 60) / TIME_STEP_MINUTES }, (_, i) =>
    option(Math.floor((i * TIME_STEP_MINUTES) / 60), (i * TIME_STEP_MINUTES) % 60)
  )
  if (current.minute % TIME_STEP_MINUTES === 0) {
    return grid
  }
  const extra = option(current.hour, current.minute)
  const minutesOf = (t: TimeOption) => t.hour * 60 + t.minute
  return [...grid, extra].sort((a, b) => minutesOf(a) - minutesOf(b))
}

/** selectedTime is the entry of `options` for `current`. */
export function selectedTime(
  options: TimeOption[],
  current: { hour: number; minute: number }
): TimeOption | undefined {
  return options.find((t) => t.hour === current.hour && t.minute === current.minute)
}
