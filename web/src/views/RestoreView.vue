<script setup lang="ts">
// Restore (Path B): bring a backup back into the Space, as a copy.
//
// All decisions live in restore/flow.ts; this file renders its state, forwards
// clicks and polls while a run is followed. Progress is "running since", not a
// percentage: live progress is not tracked server-side (8d decision 2).
//
// Offered to every member, viewers included (decisions.md #7). A restore never
// overwrites: it writes into a new folder below "Restore/", and the page says
// so before anything is sent.
import { computed, onBeforeUnmount, onMounted, shallowRef } from 'vue'
import { useGettext } from 'vue3-gettext'
import type { Job, Snapshot } from '../api'
import { restoreErrorAdvice, restoreErrorTitle } from '../api/errortext'
import ActionError from '../components/ActionError.vue'
import EmptyState from '../components/EmptyState.vue'
import NoticeBanner from '../components/NoticeBanner.vue'
import PageLayout from '../components/PageLayout.vue'
import { spaceCrumbs } from '../layout/breadcrumbs'
import RequestState from '../components/RequestState.vue'
import RestoreFolderLink from '../components/RestoreFolderLink.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { usePolling } from '../composables/usePolling'
import { isRestoreFolder } from '../restore/folderlink'
import { RestoreFlow, type RestoreState } from '../restore/flow'
import { snapshotRow } from '../restore/snapshotrow'

/** POLL_INTERVAL_MS is how often a followed run is re-read. */
const POLL_INTERVAL_MS = 5000

const props = defineProps<{ spaceId: string }>()

const { $gettext } = useGettext()
const format = useFormat()

const state = shallowRef<RestoreState>({ step: 'loading' })
/**
 * crumbs is the page's breadcrumb trail. It names the Space once flow has
 * read it, which happens with a state change: reading `state` is what makes
 * this follow, since flow.spaceName itself is not reactive.
 */
const crumbs = computed(() => {
  void state.value
  return spaceCrumbs($gettext, props.spaceId, flow.spaceName, { text: $gettext('Restore files') })
})
const flow = new RestoreFlow(props.spaceId, {
  api: useBackupApi(),
  onChange: (next) => {
    state.value = next
    if (flow.following) {
      poller.start()
    }
  }
})

const poller = usePolling(() => flow.refresh(), {
  intervalMs: POLL_INTERVAL_MS,
  shouldContinue: () => flow.following
})

const loadError = computed(() =>
  state.value.step === 'load_failed' ? state.value.error : undefined
)

/** snapshotRows are the picker's rows: date, number of files, size. */
const snapshotRows = computed(() =>
  state.value.step === 'pick' ? state.value.snapshots.map((s) => snapshotRow(s, format)) : []
)

/** snapshotFields are the picker's columns; the first one is the choice. */
const snapshotFields = computed(() => [
  { name: 'choose', title: $gettext('Choose'), type: 'slot', width: 'shrink', headerType: 'slot' },
  { name: 'when', title: $gettext('Date'), width: 'expand' },
  { name: 'files', title: $gettext('Files'), width: 'shrink', alignH: 'right', wrap: 'nowrap' },
  { name: 'size', title: $gettext('Size'), width: 'shrink', alignH: 'right', wrap: 'nowrap' }
])

/** sizeOf is "1,200 files, 3.4 GB" for a backup or a finished run. */
function sizeOf(item: Snapshot | Job): string {
  return $gettext('%{files} files, %{size}', {
    files: format.count(item.file_count ?? 0),
    size: format.bytes(item.total_bytes ?? 0)
  })
}

/** folderOf is a run's restore folder, when it is one worth showing. */
function folderOf(job: Job | undefined): string | undefined {
  const folder = job?.restore_folder
  return folder !== undefined && isRestoreFolder(folder) ? folder : undefined
}

onMounted(() => flow.start())
onBeforeUnmount(() => {
  poller.stop()
  flow.dispose()
})
</script>

<template>
  <PageLayout :crumbs="crumbs" narrow>
    <RequestState :loading="state.step === 'loading'" :error="loadError" @retry="flow.start()">
      <!-- Nothing to restore from -->
      <NoticeBanner
        v-if="state.step === 'not_set_up'"
        tone="info"
        :message="
          $gettext('Backups are not set up for this space yet, so there is nothing to restore.')
        "
        data-step="not_set_up"
      />

      <!-- Step 1: which backup -->
      <section
        v-else-if="state.step === 'pick'"
        class="ext:flex ext:flex-col ext:gap-3"
        data-step="pick"
      >
        <NoticeBanner
          v-if="state.notice === 'snapshot_gone'"
          tone="warning"
          role="alert"
          :message="
            $gettext(
              'That backup is no longer available. Older backups are removed as they reach the end of the keep period. Please choose another one.'
            )
          "
          data-testid="snapshot-gone"
        />
        <EmptyState
          v-if="state.snapshots.length === 0"
          icon="history"
          :message="
            $gettext(
              'There are no backups to restore yet. The first one appears after the first backup has run.'
            )
          "
          data-testid="no-snapshots"
        />
        <template v-else>
          <h2 class="ext:text-lg ext:font-semibold">
            {{ $gettext('Which backup do you want back?') }}
          </h2>
          <oc-table
            :data="snapshotRows"
            :fields="snapshotFields"
            :highlighted="state.selected ? [state.selected] : []"
            id-key="id"
            data-testid="snapshot-picker"
          >
            <template #chooseHeader>
              <span class="ext:sr-only">{{ $gettext('Choose') }}</span>
            </template>
            <template #choose="{ item }">
              <oc-radio
                :model-value="state.selected"
                :option="item.id"
                :label="$gettext('Backup from %{when}', { when: item.when })"
                hide-label
                data-testid="snapshot-choice"
                @update:model-value="flow.select(item.id)"
              />
            </template>
          </oc-table>
          <div>
            <oc-button
              appearance="filled"
              :disabled="state.selected === undefined"
              data-testid="review"
              @click="flow.review()"
            >
              {{ $gettext('Continue') }}
            </oc-button>
          </div>
        </template>
      </section>

      <!-- Step 2: what will happen -->
      <section
        v-else-if="state.step === 'confirm'"
        class="ext:flex ext:flex-col ext:gap-3"
        data-step="confirm"
      >
        <h2 class="ext:text-lg ext:font-semibold">
          {{
            $gettext('Restore the backup from %{when}?', {
              when: format.when(state.snapshot.taken_at)
            })
          }}
        </h2>
        <div
          class="ext:flex ext:flex-col ext:gap-2 ext:rounded-xl ext:bg-role-surface-container ext:px-4 ext:py-3"
          data-testid="confirm-summary"
        >
          <p class="ext:font-semibold" data-testid="confirm-size">{{ sizeOf(state.snapshot) }}</p>
          <ul class="ext:list-disc ext:pl-5">
            <li>
              {{
                $gettext(
                  'The files are copied into a new folder inside “Restore” in this space. Nothing in the space is changed or overwritten.'
                )
              }}
            </li>
            <li data-testid="confirm-quota">
              {{
                $gettext('The copy takes up %{size} of this space’s storage.', {
                  size: format.bytes(state.snapshot.total_bytes)
                })
              }}
            </li>
          </ul>
        </div>
        <ActionError
          v-if="state.error"
          :error="state.error"
          :title="restoreErrorTitle"
          :advice="restoreErrorAdvice"
        />
        <div class="ext:flex ext:gap-2">
          <oc-button
            appearance="filled"
            :disabled="state.starting"
            :show-spinner="state.starting"
            data-testid="start-restore"
            @click="flow.confirm()"
          >
            {{ $gettext('Restore') }}
          </oc-button>
          <oc-button
            appearance="outline"
            :disabled="state.starting"
            data-testid="choose-other"
            @click="flow.back()"
          >
            {{ $gettext('Choose another backup') }}
          </oc-button>
        </div>
      </section>

      <!-- Step 3: under way -->
      <section
        v-else-if="state.step === 'running'"
        class="ext:flex ext:flex-col ext:gap-2"
        data-step="running"
        aria-live="polite"
      >
        <p>
          <template v-if="state.snapshot">
            {{
              $gettext('Restoring the backup from %{when}…', {
                when: format.when(state.snapshot.taken_at)
              })
            }}
          </template>
          <template v-else>{{ $gettext('A restore is running for this space.') }}</template>
          <template v-if="state.job">
            {{ $gettext('Started %{when}.', { when: format.when(state.job.created_at) }) }}
          </template>
        </p>
        <oc-progress indeterminate />
        <p v-if="folderOf(state.job)" data-testid="running-folder">
          {{ $gettext('The files are being copied into:') }}
          <RestoreFolderLink :space-id="spaceId" :folder="folderOf(state.job)!" />
        </p>
        <p class="ext:text-sm ext:text-role-on-surface-variant">
          {{
            $gettext(
              'You can leave this page. The restore carries on, and the space’s recent activity shows when it has finished.'
            )
          }}
        </p>
        <p v-if="state.refreshFailed" class="ext:text-sm ext:text-role-on-surface-variant">
          {{ $gettext('Could not refresh the status. Retrying…') }}
        </p>
      </section>

      <!-- Done -->
      <NoticeBanner
        v-else-if="state.step === 'succeeded'"
        tone="success"
        :title="$gettext('Restore finished')"
        :message="sizeOf(state.job)"
        data-step="succeeded"
      >
        <p v-if="folderOf(state.job)" data-testid="done-folder">
          {{ $gettext('Your files are in:') }}
          <RestoreFolderLink :space-id="spaceId" :folder="folderOf(state.job)!" />
        </p>
        <template #actions>
          <oc-button appearance="outline" data-testid="again" @click="flow.pickAnother()">
            {{ $gettext('Restore another backup') }}
          </oc-button>
        </template>
      </NoticeBanner>

      <!-- Failed -->
      <NoticeBanner
        v-else-if="state.step === 'failed'"
        tone="danger"
        :title="$gettext('The restore did not finish')"
        data-step="failed"
      >
        <p v-if="state.job.error" class="ext:text-sm ext:opacity-80">{{ state.job.error }}</p>
        <p>{{ $gettext('Your backups are unaffected.') }}</p>
        <p v-if="folderOf(state.job)" data-testid="partial-folder">
          {{ $gettext('Anything restored before it stopped is in:') }}
          <RestoreFolderLink :space-id="spaceId" :folder="folderOf(state.job)!" />
        </p>
        <template #actions>
          <oc-button appearance="outline" data-testid="again" @click="flow.pickAnother()">
            {{ $gettext('Try again') }}
          </oc-button>
        </template>
      </NoticeBanner>

      <!-- The run's record went away -->
      <NoticeBanner
        v-else-if="state.step === 'lost'"
        tone="neutral"
        :message="
          $gettext(
            'This restore can no longer be followed here. The space’s recent activity shows how it ended.'
          )
        "
        data-step="lost"
      >
        <template #actions>
          <oc-button appearance="outline" data-testid="again" @click="flow.pickAnother()">
            {{ $gettext('Restore another backup') }}
          </oc-button>
        </template>
      </NoticeBanner>
    </RequestState>
  </PageLayout>
</template>
