// How one run in a Space's recent activity is worded and tagged.
//
// Counts are only shown where they mean something to a person: files and size
// for a backup or restore, snapshots removed for a clean-up, and nothing for a
// run that did not succeed (its counts would describe a partial run).

import type { Job } from '../api'
import type { Gettext } from '../api/errortext'
import type { StateLook } from '../layout/tone'

/** kindLabel names what ran. */
export function kindLabel(job: Job, $gettext: Gettext): string {
  switch (job.kind) {
    case 'backup':
      return $gettext('Backup')
    case 'restore':
      return $gettext('Restore')
    case 'prune':
      return $gettext('Clean-up of old backups')
    default:
      return job.kind
  }
}

/** runStateLabel names how a run got on. */
export function runStateLabel(job: Job, $gettext: Gettext): string {
  switch (job.state) {
    case 'succeeded':
      return $gettext('Succeeded')
    case 'failed':
      return $gettext('Failed')
    case 'running':
      return $gettext('Running')
    case 'pending':
      return $gettext('Waiting')
    default:
      return job.state
  }
}

/** runLook tags a run's outcome; only a failure is serious. */
export function runLook(job: Job): StateLook {
  switch (job.state) {
    case 'succeeded':
      return { tone: 'success', icon: 'checkbox-circle' }
    case 'failed':
      return { tone: 'danger', icon: 'close-circle' }
    case 'running':
      return { tone: 'info', icon: 'loader-4' }
    default:
      return { tone: 'neutral', icon: 'time' }
  }
}

/** triggerLabel says who started a run, or nothing for an unknown trigger. */
export function triggerLabel(job: Job, $gettext: Gettext): string {
  switch (job.trigger) {
    case 'manual':
      return $gettext('started by hand')
    case 'schedule':
      return $gettext('scheduled')
    default:
      return ''
  }
}

/** folderLabel introduces a restore's folder by how the run got on. */
export function folderLabel(job: Job, $gettext: Gettext): string {
  switch (job.state) {
    case 'succeeded':
      return $gettext('Restored into:')
    case 'failed':
      return $gettext('Anything restored before it stopped is in:')
    default:
      return $gettext('Restoring into:')
  }
}

/** RunFormat is the part of the format helpers the details need. */
export interface RunFormat {
  count: (n: number) => string
  bytes: (n: number) => string
}

/** runDetails is the counts of a succeeded run, or empty. */
export function runDetails(job: Job, $gettext: Gettext, format: RunFormat): string {
  if (job.state !== 'succeeded') {
    return ''
  }
  if (job.kind === 'prune') {
    return $gettext('%{deleted} removed, %{kept} kept', {
      deleted: format.count(job.snapshots_deleted ?? 0),
      kept: format.count(job.snapshots_kept ?? 0)
    })
  }
  if (job.file_count === undefined && job.total_bytes === undefined) {
    return ''
  }
  return $gettext('%{files} files, %{size}', {
    files: format.count(job.file_count ?? 0),
    size: format.bytes(job.total_bytes ?? 0)
  })
}
