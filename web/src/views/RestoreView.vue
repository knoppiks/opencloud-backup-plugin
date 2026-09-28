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
import RequestState from '../components/RequestState.vue'
import RestoreFolderLink from '../components/RestoreFolderLink.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { usePolling } from '../composables/usePolling'
import { isRestoreFolder } from '../restore/folderlink'
import { RestoreFlow, type RestoreState } from '../restore/flow'

/** POLL_INTERVAL_MS is how often a followed run is re-read. */
const POLL_INTERVAL_MS = 5000

const props = defineProps<{ spaceId: string }>()

const { $gettext } = useGettext()
const format = useFormat()

const state = shallowRef<RestoreState>({ step: 'loading' })
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
  <main class="ext:p-4 ext:flex ext:flex-col ext:gap-4 ext:max-w-2xl">
    <router-link
      :to="{ name: 'backup-vault-space', params: { spaceId } }"
      class="ext:text-sm"
      data-testid="back"
    >
      {{ $gettext('Back to the space') }}
    </router-link>

    <h1 class="ext:text-xl ext:font-semibold">
      <template v-if="flow.spaceName">
        {{ $gettext('Restore files in %{space}', { space: flow.spaceName }) }}
      </template>
      <template v-else>{{ $gettext('Restore files') }}</template>
    </h1>

    <RequestState :loading="state.step === 'loading'" :error="loadError" @retry="flow.start()">
      <!-- Nothing to restore from -->
      <section v-if="state.step === 'not_set_up'" data-step="not_set_up">
        <p>
          {{
            $gettext('Backups are not set up for this space yet, so there is nothing to restore.')
          }}
        </p>
      </section>

      <!-- Step 1: which backup -->
      <section v-else-if="state.step === 'pick'" data-step="pick">
        <p v-if="state.notice === 'snapshot_gone'" role="alert" data-testid="snapshot-gone">
          {{
            $gettext(
              'That backup is no longer available. Older backups are removed as they reach the end of the keep period. Please choose another one.'
            )
          }}
        </p>
        <p v-if="state.snapshots.length === 0" data-testid="no-snapshots">
          {{
            $gettext(
              'There are no backups to restore yet. The first one appears after the first backup has run.'
            )
          }}
        </p>
        <template v-else>
          <h2 class="ext:text-lg ext:font-semibold">
            {{ $gettext('Which backup do you want back?') }}
          </h2>
          <fieldset class="ext:mt-2 ext:flex ext:flex-col ext:gap-1" data-testid="snapshot-picker">
            <legend class="ext:sr-only">{{ $gettext('Backup') }}</legend>
            <label v-for="snap in state.snapshots" :key="snap.id" class="ext:flex ext:gap-2">
              <input
                type="radio"
                name="snapshot"
                :value="snap.id"
                :checked="state.selected === snap.id"
                @change="flow.select(snap.id)"
              />
              <span>
                <span class="ext:font-medium">{{ format.when(snap.taken_at) }}</span>
                <span class="ext:text-role-on-surface-variant"> · {{ sizeOf(snap) }}</span>
              </span>
            </label>
          </fieldset>
          <oc-button
            class="ext:mt-3"
            appearance="filled"
            :disabled="state.selected === undefined"
            data-testid="review"
            @click="flow.review()"
          >
            {{ $gettext('Continue') }}
          </oc-button>
        </template>
      </section>

      <!-- Step 2: what will happen -->
      <section v-else-if="state.step === 'confirm'" data-step="confirm">
        <h2 class="ext:text-lg ext:font-semibold">
          {{
            $gettext('Restore the backup from %{when}?', {
              when: format.when(state.snapshot.taken_at)
            })
          }}
        </h2>
        <p class="ext:mt-2" data-testid="confirm-size">{{ sizeOf(state.snapshot) }}</p>
        <ul class="ext:mt-2 ext:list-disc ext:pl-5">
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
        <ActionError
          v-if="state.error"
          class="ext:mt-2"
          :error="state.error"
          :title="restoreErrorTitle"
          :advice="restoreErrorAdvice"
        />
        <div class="ext:mt-3 ext:flex ext:gap-2">
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
      <section v-else-if="state.step === 'running'" data-step="running" aria-live="polite">
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
        <oc-progress indeterminate class="ext:mt-2" />
        <p v-if="folderOf(state.job)" class="ext:mt-2" data-testid="running-folder">
          {{ $gettext('The files are being copied into:') }}
          <RestoreFolderLink :space-id="spaceId" :folder="folderOf(state.job)!" />
        </p>
        <p class="ext:mt-2 ext:text-sm ext:text-role-on-surface-variant">
          {{
            $gettext(
              'You can leave this page. The restore carries on, and the space’s recent activity shows when it has finished.'
            )
          }}
        </p>
        <p v-if="state.refreshFailed" class="ext:mt-1 ext:text-sm ext:text-role-on-surface-variant">
          {{ $gettext('Could not refresh the status. Retrying…') }}
        </p>
      </section>

      <!-- Done -->
      <section v-else-if="state.step === 'succeeded'" data-step="succeeded">
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Restore finished') }}</h2>
        <p class="ext:mt-2">{{ sizeOf(state.job) }}</p>
        <p v-if="folderOf(state.job)" class="ext:mt-2" data-testid="done-folder">
          {{ $gettext('Your files are in:') }}
          <RestoreFolderLink :space-id="spaceId" :folder="folderOf(state.job)!" />
        </p>
        <oc-button
          class="ext:mt-3"
          appearance="outline"
          data-testid="again"
          @click="flow.pickAnother()"
        >
          {{ $gettext('Restore another backup') }}
        </oc-button>
      </section>

      <!-- Failed -->
      <section v-else-if="state.step === 'failed'" data-step="failed">
        <h2 class="ext:text-lg ext:font-semibold ext:text-role-error" role="alert">
          {{ $gettext('The restore did not finish') }}
        </h2>
        <p v-if="state.job.error" class="ext:mt-1 ext:text-sm ext:text-role-on-surface-variant">
          {{ state.job.error }}
        </p>
        <p class="ext:mt-2">{{ $gettext('Your backups are unaffected.') }}</p>
        <p v-if="folderOf(state.job)" class="ext:mt-2" data-testid="partial-folder">
          {{ $gettext('Anything restored before it stopped is in:') }}
          <RestoreFolderLink :space-id="spaceId" :folder="folderOf(state.job)!" />
        </p>
        <oc-button
          class="ext:mt-3"
          appearance="outline"
          data-testid="again"
          @click="flow.pickAnother()"
        >
          {{ $gettext('Try again') }}
        </oc-button>
      </section>

      <!-- The run's record went away -->
      <section v-else-if="state.step === 'lost'" data-step="lost">
        <p>
          {{
            $gettext(
              'This restore can no longer be followed here. The space’s recent activity shows how it ended.'
            )
          }}
        </p>
        <oc-button
          class="ext:mt-3"
          appearance="outline"
          data-testid="again"
          @click="flow.pickAnother()"
        >
          {{ $gettext('Restore another backup') }}
        </oc-button>
      </section>
    </RequestState>
  </main>
</template>
