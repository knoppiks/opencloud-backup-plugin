<script setup lang="ts">
// The Backup Vault landing page: every Space in a table, like the Files app,
// each row saying whether that Space is protected (Phase 8g decision 2).
//
// The Space list gates the page; each Space's status then loads on its own.
// A status that fails shows in its own row only. One Space whose status
// cannot be read is not a reason to hide the others, least of all the ones
// that are fine.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { computed, onMounted, reactive, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { useIsAdmin } from '../composables/useIsAdmin'
import EmptyState from '../components/EmptyState.vue'
import NoticeBanner from '../components/NoticeBanner.vue'
import PageLayout from '../components/PageLayout.vue'
import RequestState from '../components/RequestState.vue'
import StatusTag from '../components/StatusTag.vue'
import { spacesCrumbs } from '../layout/breadcrumbs'
import { ApiError, isApiError, type Space, type Target } from '../api'
import { errorTitle } from '../api/errortext'
import {
  compareSpaces,
  overviewRow,
  overviewSummary,
  type OverviewRow,
  type SpaceResult
} from '../status/overviewrow'
import { setupActionLabel } from '../status/setupaction'

const { $gettext } = useGettext()
const api = useBackupApi()
const format = useFormat()
/** isAdmin only decides what is offered, never what is allowed (8e decision 4). */
const isAdmin = useIsAdmin()

const loading = ref(true)
const error = ref<ApiError | undefined>(undefined)
const spaces = ref<Space[]>([])
const targets = ref<Target[]>([])
const results = reactive<Record<string, SpaceResult>>({})

/** rows are the table's rows: personal Space first, then by name. */
const rows = computed<OverviewRow[]>(() =>
  [...spaces.value]
    .sort(compareSpaces)
    .map((space) => overviewRow(space, results[space.id], $gettext, format.when))
)
const summary = computed(() => overviewSummary(rows.value))

/**
 * fields are the columns. Type and next backup give way on narrow screens;
 * name, state and what to do next never do.
 */
const fields = computed(() => [
  { name: 'name', title: $gettext('Space'), type: 'slot', width: 'expand' },
  {
    name: 'kind',
    title: $gettext('Type'),
    width: 'shrink',
    wrap: 'nowrap',
    thClass: 'ext:hidden ext:md:table-cell',
    tdClass: 'ext:hidden ext:md:table-cell ext:text-role-on-surface-variant'
  },
  { name: 'state', title: $gettext('Status'), type: 'slot', width: 'shrink' },
  {
    name: 'lastBackup',
    title: $gettext('Last backup'),
    width: 'shrink',
    wrap: 'nowrap',
    thClass: 'ext:whitespace-nowrap'
  },
  {
    name: 'nextBackup',
    title: $gettext('Next backup'),
    width: 'shrink',
    wrap: 'nowrap',
    thClass: 'ext:hidden ext:md:table-cell ext:whitespace-nowrap',
    tdClass: 'ext:hidden ext:md:table-cell'
  },
  {
    name: 'action',
    title: $gettext('Actions'),
    type: 'slot',
    alignH: 'right',
    width: 'shrink',
    wrap: 'nowrap'
  }
])

function asApiError(err: unknown): ApiError {
  // An unexpected throw is still shown, not swallowed: a blank page is the one
  // outcome worse than an ugly error.
  return isApiError(err) ? err : new ApiError('unknown', $gettext('Something went wrong'))
}

async function loadStatus(space: Space): Promise<void> {
  try {
    results[space.id] = { status: await api.status(space.id) }
  } catch (err: unknown) {
    results[space.id] = { error: asApiError(err) }
  }
}

async function load(): Promise<void> {
  loading.value = true
  error.value = undefined
  try {
    // Both are cheap and neither depends on the other, so one round trip's
    // worth of latency instead of two.
    const [loadedSpaces, loadedTargets] = await Promise.all([api.listSpaces(), api.listTargets()])
    spaces.value = loadedSpaces
    targets.value = loadedTargets
  } catch (err: unknown) {
    error.value = asApiError(err)
    return
  } finally {
    loading.value = false
  }
  for (const id of Object.keys(results)) {
    delete results[id]
  }
  await Promise.all(spaces.value.map(loadStatus))
}

onMounted(load)
</script>

<template>
  <PageLayout :crumbs="spacesCrumbs($gettext, true)">
    <RequestState :loading="loading" :error="error" @retry="load">
      <!--
        No granted target means setup cannot complete, however many Spaces
        there are. Saying so here beats letting a user reach the wizard and
        find an empty picker (decisions.md #12: the admin grants targets).
        An admin is the one who can change that, so they are offered the way.
      -->
      <NoticeBanner
        v-if="targets.length === 0"
        tone="warning"
        :title="$gettext('No backup destination is available to you yet')"
        :message="
          isAdmin
            ? $gettext('Add a backup destination, or share an existing one with yourself.')
            : $gettext(
                'Ask your administrator to share one with you. Until then, no space can be set up.'
              )
        "
        data-testid="no-targets"
      >
        <template v-if="isAdmin" #actions>
          <oc-button
            type="router-link"
            :to="{ name: 'backup-vault-admin-targets' }"
            appearance="outline"
            data-testid="no-targets-admin"
          >
            {{ $gettext('Add or share a destination') }}
          </oc-button>
        </template>
      </NoticeBanner>

      <EmptyState
        v-if="rows.length === 0"
        icon="layout-grid"
        :message="$gettext('You do not have any spaces to back up.')"
      />

      <oc-table v-else :data="rows" :fields="fields" id-key="id" data-testid="spaces">
        <template #name="{ item }">
          <router-link
            :to="{ name: 'backup-vault-space', params: { spaceId: item.id } }"
            class="ext:inline-flex ext:items-center ext:gap-2 ext:font-medium ext:hover:underline"
            data-testid="space-link"
          >
            <oc-icon :name="item.personal ? 'user' : 'group'" fill-type="line" />
            {{ item.name }}
          </router-link>
        </template>
        <template #state="{ item }">
          <StatusTag v-if="item.state" :state="item.state" />
          <span
            v-else-if="item.error"
            class="ext:inline-flex ext:items-center ext:gap-1 ext:text-sm ext:text-role-error"
            role="alert"
            data-testid="row-error"
          >
            <oc-icon name="error-warning" fill-type="line" size="small" />
            {{ errorTitle(item.error.code, $gettext) }}
          </span>
          <oc-spinner v-else size="small" :aria-label="$gettext('Loading')" />
        </template>
        <template #action="{ item }">
          <oc-button
            v-if="item.action"
            type="router-link"
            :to="{ name: 'backup-vault-setup', params: { spaceId: item.id } }"
            appearance="outline"
            size="small"
            data-testid="setup-action"
          >
            {{ setupActionLabel(item.action, $gettext) }}
          </oc-button>
        </template>
        <template #footer>
          <span data-testid="summary">
            {{
              $gettext('Spaces: %{total} · Protected: %{protected}', {
                total: format.count(summary.total),
                protected: format.count(summary.protected)
              })
            }}
            <template v-if="summary.attention > 0">
              ·
              {{ $gettext('Need attention: %{count}', { count: format.count(summary.attention) }) }}
            </template>
          </span>
        </template>
      </oc-table>
    </RequestState>
  </PageLayout>
</template>
