// The retention floor, as the UI states it.
//
// The server enforces the floor (decisions.md #22) and its 400 stays the
// authority. The UI checks the same number first so the rule can be explained
// next to the field, before the person presses save, and not only in an error.

/** MIN_RETENTION_DAYS mirrors spacecfg.MinRetentionWindow. */
export const MIN_RETENTION_DAYS = 7

/** RetentionProblem is why an entered value cannot be saved. */
export type RetentionProblem = 'not_a_number' | 'below_floor'

/**
 * parseRetentionDays reads what was typed into the field. Whole days only:
 * "7.5" is refused rather than rounded, because a rounding the person did not
 * see is a setting they did not choose.
 */
export function parseRetentionDays(
  input: string
): { days: number } | { problem: RetentionProblem } {
  const trimmed = input.trim()
  if (!/^\d+$/.test(trimmed)) {
    return { problem: 'not_a_number' }
  }
  const days = Number(trimmed)
  if (days < MIN_RETENTION_DAYS) {
    return { problem: 'below_floor' }
  }
  return { days }
}
