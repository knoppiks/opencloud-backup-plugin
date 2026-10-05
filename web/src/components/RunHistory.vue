<script setup lang="ts">
// The Space's recent runs, newest first, as a table: what ran and who started
// it, how it ended, when, and what it did (Phase 8g).
//
// Every kind is listed — backups, restores and the nightly clean-up — because
// "what has this service been doing with my Space" is the question the list
// answers. Wording and tags live in status/runtext.ts.
//
// A restore row names the folder it wrote into, linked where the host allows,
// and so does a failed one: it may have left a partial copy behind (8d.3
// decision 5).
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import type { Job } from '../api'
import { useFormat } from '../composables/useFormat'
import { isRestoreFolder } from '../restore/folderlink'
import {
  folderLabel,
  kindLabel,
  runDetails,
  runLook,
  runStateLabel,
  triggerLabel
} from '../status/runtext'
import EmptyState from './EmptyState.vue'
import RestoreFolderLink from './RestoreFolderLink.vue'
import ToneTag from './ToneTag.vue'

defineProps<{ spaceId: string; runs: Job[] }>()

const { $gettext } = useGettext()
const format = useFormat()

const fields = computed(() => [
  { name: 'kind', title: $gettext('Activity'), type: 'slot', width: 'shrink', wrap: 'nowrap' },
  { name: 'state', title: $gettext('Status'), type: 'slot', width: 'shrink' },
  { name: 'when', title: $gettext('When'), type: 'slot', width: 'shrink', wrap: 'nowrap' },
  { name: 'details', title: $gettext('Details'), type: 'slot', width: 'expand' }
])

function showsFolder(run: Job): run is Job & { restore_folder: string } {
  return run.kind === 'restore' && !!run.restore_folder && isRestoreFolder(run.restore_folder)
}
</script>

<template>
  <EmptyState v-if="runs.length === 0" icon="history" :message="$gettext('Nothing has run yet.')" />
  <oc-table v-else :data="runs" :fields="fields" id-key="id" data-testid="runs">
    <template #kind="{ item }">
      <span :data-kind="item.kind" :data-state="item.state" data-testid="run">
        <span class="ext:font-medium">{{ kindLabel(item, $gettext) }}</span>
        <span
          v-if="triggerLabel(item, $gettext)"
          class="ext:block ext:text-sm ext:text-role-on-surface-variant"
        >
          {{ triggerLabel(item, $gettext) }}
        </span>
      </span>
    </template>
    <template #state="{ item }">
      <ToneTag
        :tone="runLook(item).tone"
        :icon="runLook(item).icon"
        :label="runStateLabel(item, $gettext)"
      />
    </template>
    <template #when="{ item }">{{ format.when(item.created_at) }}</template>
    <template #details="{ item }">
      <span v-if="runDetails(item, $gettext, format)" class="ext:block">
        {{ runDetails(item, $gettext, format) }}
      </span>
      <span v-if="item.error" class="ext:block ext:text-sm ext:text-role-error">
        {{ item.error }}
      </span>
      <span v-if="showsFolder(item)" class="ext:block ext:text-sm" data-testid="history-folder">
        {{ folderLabel(item, $gettext) }}
        <RestoreFolderLink :space-id="spaceId" :folder="item.restore_folder" />
      </span>
    </template>
  </oc-table>
</template>
