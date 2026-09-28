// Where a Space's setup has got to, read from the server's status document.
//
// Kept apart from the wizard's machine on purpose: the overview and the board
// need this rule too, and importing it from the machine would pull the whole
// key ceremony (Argon2id, XChaCha20) into pages that never run one.

import type { BackupStatus } from '../api'

/** Step is where a status document says setup has got to. */
export type Step = 'target' | 'keys' | 'schedule' | 'done'

/**
 * nextStep is the resume rule: the first write the server has not seen.
 * Target binding, then keys, then the schedule that switches runs on
 * (8d.2 decision 1).
 */
export function nextStep(status: BackupStatus): Step {
  if (!status.configured) {
    return 'target'
  }
  if (!status.keys_configured) {
    return 'keys'
  }
  if (!status.enabled) {
    return 'schedule'
  }
  return 'done'
}
