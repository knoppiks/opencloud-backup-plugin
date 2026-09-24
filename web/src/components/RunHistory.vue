<script setup lang="ts">
// The Space's recent runs, newest first: what ran, when, how it ended.
//
// Every kind is listed — backups, restores and the nightly clean-up — because
// "what has this service been doing with my Space" is the question the list
// answers. Counts are only shown where they mean something to a person:
// files and size for a backup or restore, snapshots removed for a clean-up.
import { useGettext } from 'vue3-gettext'
import type { Job } from '../api'
import { useFormat } from '../composables/useFormat'

defineProps<{ runs: Job[] }>()

const { $gettext } = useGettext()
const format = useFormat()

function kindLabel(job: Job): string {
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

function stateLabel(job: Job): string {
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

function triggerLabel(job: Job): string {
  switch (job.trigger) {
    case 'manual':
      return $gettext('started by hand')
    case 'schedule':
      return $gettext('scheduled')
    default:
      return ''
  }
}

function details(job: Job): string {
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
</script>

<template>
  <p v-if="runs.length === 0" class="ext:text-role-on-surface-variant">
    {{ $gettext('Nothing has run yet.') }}
  </p>
  <ul v-else class="ext:flex ext:flex-col ext:divide-y ext:divide-role-outline-variant">
    <li
      v-for="run in runs"
      :key="run.id"
      class="ext:py-2"
      :data-kind="run.kind"
      :data-state="run.state"
    >
      <div class="ext:flex ext:flex-wrap ext:justify-between ext:gap-2">
        <span class="ext:font-medium">
          {{ kindLabel(run) }}
          <span v-if="triggerLabel(run)" class="ext:font-normal ext:text-role-on-surface-variant">
            ({{ triggerLabel(run) }})
          </span>
        </span>
        <span :class="run.state === 'failed' ? 'ext:text-role-error' : ''">
          {{ stateLabel(run) }} · {{ format.when(run.created_at) }}
        </span>
      </div>
      <p v-if="details(run)" class="ext:text-sm ext:text-role-on-surface-variant">
        {{ details(run) }}
      </p>
      <p v-if="run.error" class="ext:text-sm ext:text-role-error">{{ run.error }}</p>
    </li>
  </ul>
</template>
