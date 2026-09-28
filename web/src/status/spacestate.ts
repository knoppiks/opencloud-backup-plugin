// What state a Space's backup is in, decided once.
//
// The overview card and the status board both colour themselves from this, so
// "is this Space OK" has one answer on every screen. It is a pure function of
// the server's status document and nothing else. In particular it does not
// compute staleness: the server does that with the same rule it sends the
// backup_stale notification on (8d decision 1), and a second copy here is
// exactly what that decision exists to prevent.

import type { BackupStatus } from '../api'

/**
 * SpaceState is the one word a card shows.
 *
 * The order below is the order of precedence: a Space that is running *and*
 * stale shows as running, because that is the thing happening right now and
 * the one that may resolve the other.
 */
export type SpaceState =
  /** No target and no keys: nobody has started setup. */
  | 'not_set_up'
  /** One of the two halves exists: setup was started and not finished. */
  | 'setup_incomplete'
  /** A backup, restore or prune is under way. */
  | 'running'
  /** The most recent backup failed. */
  | 'failed'
  /** No successful backup for longer than the schedule allows. */
  | 'stale'
  /** Set up, and scheduled backups are switched off. */
  | 'paused'
  /** Set up and on, and no backup has run yet. */
  | 'waiting'
  /** Set up, on, and the most recent backup succeeded. */
  | 'active'

/** spaceState classifies a status document. */
export function spaceState(status: BackupStatus): SpaceState {
  if (!status.configured && !status.keys_configured) {
    return 'not_set_up'
  }
  if (!status.configured || !status.keys_configured) {
    return 'setup_incomplete'
  }
  if (status.running) {
    return 'running'
  }
  if (status.last_run?.state === 'failed') {
    return 'failed'
  }
  if (status.stale) {
    return 'stale'
  }
  if (!status.enabled) {
    return 'paused'
  }
  if (!status.last_successful_run) {
    return 'waiting'
  }
  return 'active'
}

/**
 * needsAttention reports whether a state is one a member should act on. Cards
 * in these states are visually marked; the others are informational.
 */
export function needsAttention(state: SpaceState): boolean {
  return state === 'failed' || state === 'stale' || state === 'setup_incomplete'
}
