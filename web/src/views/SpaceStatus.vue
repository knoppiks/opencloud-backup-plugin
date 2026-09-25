<script setup lang="ts">
// The status board for one Space: is it protected, what ran last, what runs
// next, and "Back up now".
//
// Progress is shown as "running since", not as a percentage. Live progress is
// not tracked server-side (Phase 6 amendment; 8d decision 2), so the board
// re-reads status while a run is under way and stops as soon as it is not.
//
// Role gates here decide what is *offered*. The server enforces each of them
// again (pkg/api/access.go); a viewer who forged the request would get a 403.
import { computed, onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError, isApiError, type BackupStatus, type Job, type Space } from '../api'
import { errorAdvice, errorTitle } from '../api/errortext'
import RequestState from '../components/RequestState.vue'
import RetentionEditor from '../components/RetentionEditor.vue'
import RunHistory from '../components/RunHistory.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { usePolling } from '../composables/usePolling'
import { canOperate } from '../status/roles'
import { setupAction, setupActionLabel } from '../status/setupaction'
import { needsAttention, spaceState } from '../status/spacestate'
import { stateAdvice, stateLabel } from '../status/statetext'

/** POLL_INTERVAL_MS is how often a running job is re-read. */
const POLL_INTERVAL_MS = 5000
/** HISTORY_LIMIT bounds the run list; the server reads only what it returns. */
const HISTORY_LIMIT = 10

const props = defineProps<{ spaceId: string }>()

const { $gettext } = useGettext()
const api = useBackupApi()
const format = useFormat()

const loading = ref(true)
const loadError = ref<ApiError | undefined>(undefined)
const space = ref<Space | undefined>(undefined)
const status = ref<BackupStatus | undefined>(undefined)
const runs = ref<Job[]>([])

const starting = ref(false)
const actionError = ref<ApiError | undefined>(undefined)
const refreshFailed = ref(false)

const state = computed(() => (status.value ? spaceState(status.value) : undefined))
const isSetUp = computed(
  () => status.value?.configured === true && status.value?.keys_configured === true
)
const mayOperate = computed(() => (space.value ? canOperate(space.value.role) : false))
const action = computed(() =>
  status.value && space.value ? setupAction(status.value, space.value.role) : undefined
)

const poller = usePolling(refresh, {
  intervalMs: POLL_INTERVAL_MS,
  shouldContinue: () => status.value?.running === true
})

function asApiError(err: unknown): ApiError {
  return isApiError(err) ? err : new ApiError('unknown', $gettext('Something went wrong'))
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = undefined
  try {
    const [spaces, loadedStatus, loadedRuns] = await Promise.all([
      api.listSpaces(),
      api.status(props.spaceId),
      api.listRuns(props.spaceId, HISTORY_LIMIT)
    ])
    const found = spaces.find((s) => s.id === props.spaceId)
    if (!found) {
      // The status call would have been refused for a non-member, so this is
      // a Space that vanished between the two reads. Same answer either way.
      throw new ApiError('not_found', 'space not in the caller’s list')
    }
    space.value = found
    status.value = loadedStatus
    runs.value = loadedRuns
  } catch (err: unknown) {
    loadError.value = asApiError(err)
  } finally {
    loading.value = false
  }
  watchRunning()
}

/**
 * refresh re-reads status while a run is under way. When the run has just
 * ended the history is re-read as well, so the finished run appears in the
 * list without a reload.
 */
async function refresh(): Promise<void> {
  const wasRunning = status.value?.running === true
  try {
    status.value = await api.status(props.spaceId)
    if (wasRunning && !status.value.running) {
      runs.value = await api.listRuns(props.spaceId, HISTORY_LIMIT)
    }
    refreshFailed.value = false
  } catch {
    // Keep showing what we last knew, say that it may be out of date, and
    // keep trying: a blip must not freeze the board on "running" forever.
    refreshFailed.value = true
  }
}

function watchRunning(): void {
  if (status.value?.running) {
    poller.start()
  }
}

async function runNow(): Promise<void> {
  starting.value = true
  actionError.value = undefined
  try {
    await api.runBackup(props.spaceId)
  } catch (err: unknown) {
    const failure = asApiError(err)
    // Someone else (or the schedule) got there first: that is the outcome the
    // person wanted, so it is shown as the running job rather than an error.
    if (failure.code !== 'run_in_progress') {
      actionError.value = failure
      starting.value = false
      return
    }
  }
  await refresh()
  starting.value = false
  // A run that finished between the POST and the status read would leave
  // "running" false; re-read history so it still appears.
  if (!status.value?.running) {
    runs.value = await api.listRuns(props.spaceId, HISTORY_LIMIT).catch(() => runs.value)
  }
  watchRunning()
}

function onRetentionSaved(days: number): void {
  if (status.value) {
    status.value = { ...status.value, retention_days: days }
  }
}

/** runningText names what is running; a restore is not "a backup". */
const runningText = computed(() => {
  switch (status.value?.current_job?.kind) {
    case 'restore':
      return $gettext('A restore is running.')
    case 'prune':
      return $gettext('Old backups are being cleaned up.')
    default:
      return $gettext('A backup is running.')
  }
})

onMounted(load)
</script>

<template>
  <main class="ext:p-4 ext:flex ext:flex-col ext:gap-4">
    <router-link :to="{ name: 'backup-vault-overview' }" class="ext:text-sm">
      {{ $gettext('All spaces') }}
    </router-link>

    <RequestState :loading="loading" :error="loadError" @retry="load">
      <template v-if="space && status && state">
        <header>
          <h1 class="ext:text-xl ext:font-semibold">{{ space.name }}</h1>
          <p
            class="ext:mt-1 ext:font-medium"
            :class="needsAttention(state) ? 'ext:text-role-error' : ''"
            data-testid="state-label"
          >
            {{ stateLabel(state, $gettext) }}
          </p>
          <p v-if="stateAdvice(state, $gettext)" data-testid="state-advice">
            {{ stateAdvice(state, $gettext) }}
          </p>
        </header>

        <div v-if="action" data-testid="setup-action">
          <p v-if="action === 'needs_manager'">{{ setupActionLabel(action, $gettext) }}</p>
          <router-link
            v-else
            :to="{ name: 'backup-vault-setup', params: { spaceId } }"
            class="ext:font-medium"
          >
            {{ setupActionLabel(action, $gettext) }}
          </router-link>
        </div>

        <section
          v-if="status.running && status.current_job"
          data-testid="running"
          aria-live="polite"
        >
          <p>
            {{ runningText }}
            {{ $gettext('Started %{when}.', { when: format.when(status.current_job.created_at) }) }}
          </p>
          <oc-progress indeterminate class="ext:mt-2" />
          <p v-if="refreshFailed" class="ext:mt-1 ext:text-sm ext:text-role-on-surface-variant">
            {{ $gettext('Could not refresh the status. Retrying…') }}
          </p>
        </section>

        <section v-if="status.stale && status.stale_since" role="alert" data-testid="stale">
          {{
            $gettext('No successful backup since %{when}.', {
              when: format.when(status.stale_since)
            })
          }}
        </section>

        <section
          v-if="state === 'failed' && status.last_error"
          role="alert"
          data-testid="last-error"
        >
          <p>{{ $gettext('The last backup failed:') }}</p>
          <p class="ext:text-sm ext:text-role-on-surface-variant">{{ status.last_error }}</p>
        </section>

        <dl v-if="isSetUp" class="ext:grid ext:grid-cols-[max-content_1fr] ext:gap-x-4 ext:gap-y-2">
          <dt>{{ $gettext('Last successful backup') }}</dt>
          <dd data-testid="last-success">
            <template v-if="status.last_successful_run">
              {{ format.when(status.last_successful_run.created_at) }}
            </template>
            <template v-else>{{ $gettext('None yet') }}</template>
          </dd>

          <dt>{{ $gettext('Next backup') }}</dt>
          <dd data-testid="next-run">
            <template v-if="!status.enabled">{{ $gettext('Scheduled backups are off') }}</template>
            <template v-else-if="status.next_run">{{ format.when(status.next_run) }}</template>
            <template v-else>{{ $gettext('Not scheduled') }}</template>
          </dd>

          <dt>{{ $gettext('Keeps backups for') }}</dt>
          <dd>
            <RetentionEditor
              v-if="status.retention_days !== undefined"
              :space-id="spaceId"
              :retention-days="status.retention_days"
              :editable="mayOperate"
              @saved="onRetentionSaved"
            />
          </dd>
        </dl>

        <div v-if="isSetUp && mayOperate">
          <oc-button
            appearance="filled"
            :disabled="starting || status.running"
            :show-spinner="starting"
            data-testid="run-now"
            @click="runNow"
          >
            {{ $gettext('Back up now') }}
          </oc-button>
          <div v-if="actionError" role="alert" class="ext:mt-2" data-testid="action-error">
            <p>{{ errorTitle(actionError.code, $gettext) }}</p>
            <p v-if="errorAdvice(actionError.code, $gettext)" class="ext:text-sm">
              {{ errorAdvice(actionError.code, $gettext) }}
            </p>
          </div>
        </div>

        <section>
          <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Recent activity') }}</h2>
          <RunHistory :runs="runs" />
        </section>
      </template>
    </RequestState>
  </main>
</template>
