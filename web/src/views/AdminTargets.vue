<script setup lang="ts">
// Admin: every backup destination, with where it points, as a table like
// Files (Phase 8g). Never its keys: the API does not return them
// (decisions.md #14).
//
// This page manages destinations and who may use them, nothing else. It shows
// no Space, no run and no user data (decisions.md #15). Above the list it
// warns when OpenCloud runs a version this release was not tested with
// (compatibility-policy.md §3), or when this bundle and the service differ
// (#102); below it, both versions, for a bug report.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { computed, onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { asApiError, type AdminTarget, type ApiError } from '../api'
import { openCloudNotice } from '../admin/compat'
import { targetRow } from '../admin/targetrow'
import { adminErrorAdvice, adminErrorTitle } from '../admin/wording'
import AdminOnly from '../components/admin/AdminOnly.vue'
import EmptyState from '../components/EmptyState.vue'
import NoticeBanner from '../components/NoticeBanner.vue'
import PageLayout from '../components/PageLayout.vue'
import { destinationsCrumbs } from '../layout/breadcrumbs'
import RequestState from '../components/RequestState.vue'
import VersionNotice from '../components/VersionNotice.vue'
import { useAdminApi } from '../composables/useAdminApi'
import { useIsAdmin } from '../composables/useIsAdmin'
import { APP_VERSION } from '../version/appversion'
import { useServiceVersion } from '../version/useServiceVersion'

const { $gettext } = useGettext()
const api = useAdminApi()
const isAdmin = useIsAdmin()

const loading = ref(true)
const error = ref<ApiError | undefined>(undefined)
const targets = ref<AdminTarget[]>([])

// One answer per page load, shared with VersionNotice. Advisory: if the
// service cannot say, the page says nothing about it rather than showing an
// error next to working destinations.
// Not asked for a non-admin, who is shown nothing here.
const service = isAdmin ? useServiceVersion(api) : ref(undefined)

const rows = computed(() => targets.value.map((t) => targetRow(t, $gettext)))
const compatNotice = computed(() => openCloudNotice(service.value?.opencloud, $gettext))

/** fields are the columns; the location gives way on narrow screens. */
const fields = computed(() => [
  { name: 'name', title: $gettext('Name'), type: 'slot', width: 'expand' },
  {
    name: 'location',
    title: $gettext('Location'),
    thClass: 'ext:hidden ext:md:table-cell',
    tdClass: 'ext:hidden ext:md:table-cell ext:text-role-on-surface-variant ext:break-all'
  },
  { name: 'keyPairs', title: $gettext('Access keys'), width: 'shrink', wrap: 'nowrap' }
])

async function load(): Promise<void> {
  loading.value = true
  error.value = undefined
  try {
    targets.value = await api.listTargets()
  } catch (err: unknown) {
    error.value = asApiError(err)
  } finally {
    loading.value = false
  }
}

onMounted(() => {
  if (isAdmin) {
    void load()
  }
})
</script>

<template>
  <PageLayout :crumbs="destinationsCrumbs($gettext)">
    <template v-if="isAdmin" #actions>
      <oc-button
        type="router-link"
        :to="{ name: 'backup-vault-admin-target-new' }"
        appearance="filled"
        data-testid="add"
      >
        <oc-icon name="add" fill-type="line" size="small" />
        {{ $gettext('Add a backup destination') }}
      </oc-button>
    </template>
    <AdminOnly>
      <VersionNotice :api="api" :is-admin="isAdmin" class="ext:mb-4" />
      <NoticeBanner
        v-if="compatNotice"
        tone="warning"
        :title="compatNotice.title"
        :message="compatNotice.message"
        class="ext:mb-4"
        data-testid="opencloud-untested"
      />
      <RequestState
        :loading="loading"
        :error="error"
        :title="adminErrorTitle"
        :advice="adminErrorAdvice"
        @retry="load"
      >
        <EmptyState
          v-if="rows.length === 0"
          icon="server"
          :message="
            $gettext(
              'There are no backup destinations yet. Nobody can set up backups until there is one.'
            )
          "
          data-testid="no-destinations"
        />
        <oc-table v-else :data="rows" :fields="fields" id-key="id" data-testid="destinations">
          <template #name="{ item }">
            <router-link
              :to="{ name: 'backup-vault-admin-target', params: { targetId: item.id } }"
              class="ext:inline-flex ext:items-center ext:gap-2 ext:font-medium ext:hover:underline"
              data-testid="destination-link"
            >
              <oc-icon name="hard-drive-2" fill-type="line" />
              {{ item.name }}
            </router-link>
          </template>
        </oc-table>
      </RequestState>
      <p class="ext:mt-6 ext:text-sm ext:text-role-on-surface-variant" data-testid="versions">
        {{
          service
            ? $gettext('Backup Vault %{bundle}, backup service %{service}', {
                bundle: APP_VERSION,
                service: service.version
              })
            : $gettext('Backup Vault %{bundle}', { bundle: APP_VERSION })
        }}
      </p>
    </AdminOnly>
  </PageLayout>
</template>
