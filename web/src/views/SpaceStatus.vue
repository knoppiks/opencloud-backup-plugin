<script setup lang="ts">
// The status board for one Space: is it protected, what ran last, what runs
// next, and "Back up now". Laid out like an OpenCloud page (Phase 8g): the
// state and the actions in the header, notices below it, then the summary and
// the recent activity.
//
// Progress is shown as "running since", not as a percentage. Live progress is
// not tracked server-side (Phase 6 amendment; 8d decision 2), so the board
// re-reads status while a run is under way and stops as soon as it is not.
//
// Role gates here decide what is *offered*. The server enforces each of them
// again (internal/api/access.go); a viewer who forged the request would get a 403.
import { computed, onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError, isApiError, type BackupStatus, type Job, type Space } from '../api'
import ActionError from '../components/ActionError.vue'
import NoticeBanner from '../components/NoticeBanner.vue'
import PageLayout from '../components/PageLayout.vue'
import RequestState from '../components/RequestState.vue'
import { spaceCrumbs } from '../layout/breadcrumbs'
import { stateLook } from '../layout/tone'
import RetentionEditor from '../components/RetentionEditor.vue'
import RunHistory from '../components/RunHistory.vue'
import StatusTag from '../components/StatusTag.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { usePolling } from '../composables/usePolling'
import { canOperate } from '../status/roles'
import { setupAction, setupActionLabel } from '../status/setupaction'
import { spaceState } from '../status/spacestate'
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

/**
 * showsAdvice says whether the state gets its advice as a notice. The failed
 * and stale states have notices of their own that already carry it.
 */
const showsAdvice = computed(
  () =>
    state.value !== undefined &&
    state.value !== 'failed' &&
    state.value !== 'stale' &&
    stateAdvice(state.value, $gettext) !== ''
)

onMounted(load)
</script>

<template>
  <PageLayout :crumbs="spaceCrumbs($gettext, spaceId, space?.name)">
    <template v-if="state" #status>
      <StatusTag :state="state" />
    </template>

    <template v-if="isSetUp" #actions>
      <oc-button
        v-if="mayOperate"
        appearance="filled"
        :disabled="starting || status?.running"
        :show-spinner="starting"
        data-testid="run-now"
        @click="runNow"
      >
        <oc-icon name="play-circle" fill-type="line" />
        {{ $gettext('Back up now') }}
      </oc-button>
      <!-- Every member, viewers included: restore is a member capability
           (decisions.md #7), and it never overwrites anything. -->
      <oc-button
        type="router-link"
        :to="{ name: 'backup-vault-restore', params: { spaceId } }"
        appearance="outline"
        data-testid="restore-link"
      >
        <oc-icon name="arrow-go-back" fill-type="line" />
        {{ $gettext('Restore files') }}
      </oc-button>
      <!-- Every member: the recovery envelope is theirs to check and keep
           (decisions.md #7). Replacement is linked from there, for managers. -->
      <oc-button
        type="router-link"
        :to="{ name: 'backup-vault-recovery-key', params: { spaceId } }"
        appearance="outline"
        data-testid="recovery-key-link"
      >
        <oc-icon name="key-2" fill-type="line" />
        {{ $gettext('Recovery Key') }}
      </oc-button>
    </template>

    <RequestState :loading="loading" :error="loadError" @retry="load">
      <template v-if="space && status && state">
        <ActionError v-if="actionError" :error="actionError" />

        <NoticeBanner
          v-if="status.running && status.current_job"
          tone="info"
          :title="runningText"
          :message="
            $gettext('Started %{when}.', { when: format.when(status.current_job.created_at) })
          "
          data-testid="running"
          aria-live="polite"
        >
          <oc-progress indeterminate class="ext:mt-1" />
          <p v-if="refreshFailed" class="ext:text-sm">
            {{ $gettext('Could not refresh the status. Retrying…') }}
          </p>
        </NoticeBanner>

        <NoticeBanner
          v-if="state === 'failed'"
          tone="danger"
          :title="$gettext('The last backup failed')"
          :message="stateAdvice(state, $gettext)"
          data-testid="last-error"
        >
          <p v-if="status.last_error" class="ext:text-sm">{{ status.last_error }}</p>
        </NoticeBanner>

        <NoticeBanner
          v-if="status.stale && status.stale_since"
          tone="warning"
          :title="
            $gettext('No successful backup since %{when}.', {
              when: format.when(status.stale_since)
            })
          "
          :message="stateAdvice('stale', $gettext)"
          data-testid="stale"
        />

        <NoticeBanner
          v-if="showsAdvice || action"
          :tone="stateLook(state).tone"
          :title="showsAdvice ? stateAdvice(state, $gettext) : stateLabel(state, $gettext)"
          data-testid="state-advice"
        >
          <p v-if="action === 'needs_manager'" data-testid="setup-action">
            {{ setupActionLabel(action, $gettext) }}
          </p>
          <template v-if="action && action !== 'needs_manager'" #actions>
            <oc-button
              type="router-link"
              :to="{ name: 'backup-vault-setup', params: { spaceId } }"
              appearance="filled"
              data-testid="setup-action"
            >
              {{ setupActionLabel(action, $gettext) }}
            </oc-button>
          </template>
        </NoticeBanner>

        <dl v-if="isSetUp" class="ext:grid ext:gap-4 ext:sm:grid-cols-3" data-testid="summary">
          <div class="ext:rounded-xl ext:border ext:border-role-outline-variant ext:p-4">
            <dt class="ext:text-sm ext:text-role-on-surface-variant">
              {{ $gettext('Last successful backup') }}
            </dt>
            <dd class="ext:mt-1 ext:text-lg ext:font-semibold" data-testid="last-success">
              <template v-if="status.last_successful_run">
                {{ format.when(status.last_successful_run.created_at) }}
              </template>
              <template v-else>{{ $gettext('None yet') }}</template>
            </dd>
          </div>

          <div class="ext:rounded-xl ext:border ext:border-role-outline-variant ext:p-4">
            <dt class="ext:text-sm ext:text-role-on-surface-variant">
              {{ $gettext('Next backup') }}
            </dt>
            <dd class="ext:mt-1 ext:text-lg ext:font-semibold" data-testid="next-run">
              <template v-if="!status.enabled">{{
                $gettext('Scheduled backups are off')
              }}</template>
              <template v-else-if="status.next_run">{{ format.when(status.next_run) }}</template>
              <template v-else>{{ $gettext('Not scheduled') }}</template>
            </dd>
          </div>

          <div class="ext:rounded-xl ext:border ext:border-role-outline-variant ext:p-4">
            <dt class="ext:text-sm ext:text-role-on-surface-variant">
              {{ $gettext('Keeps backups for') }}
            </dt>
            <dd class="ext:mt-1">
              <RetentionEditor
                v-if="status.retention_days !== undefined"
                :space-id="spaceId"
                :retention-days="status.retention_days"
                :editable="mayOperate"
                @saved="onRetentionSaved"
              />
            </dd>
          </div>
        </dl>

        <section class="ext:flex ext:flex-col ext:gap-2">
          <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Recent activity') }}</h2>
          <RunHistory :space-id="spaceId" :runs="runs" />
        </section>
      </template>
    </RequestState>
  </PageLayout>
</template>
