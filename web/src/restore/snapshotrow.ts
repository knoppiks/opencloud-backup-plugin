// One row of the restore picker's table (Phase 8g): a backup as date, number
// of files and size, worked out without rendering anything.

import type { Snapshot } from '../api'
import type { Format } from '../composables/useFormat'

/** SnapshotRow is what the picker shows for one backup. */
export interface SnapshotRow {
  id: string
  when: string
  files: string
  size: string
}

/** snapshotRow builds a row, formatted for the reader's language. */
export function snapshotRow(snapshot: Snapshot, format: Format): SnapshotRow {
  return {
    id: snapshot.id,
    when: format.when(snapshot.taken_at),
    files: format.count(snapshot.file_count ?? 0),
    size: format.bytes(snapshot.total_bytes ?? 0)
  }
}
