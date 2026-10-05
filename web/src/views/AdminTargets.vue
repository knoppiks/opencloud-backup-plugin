<script setup lang="ts">
// Admin: every backup destination, with where it points. Never its keys: the
// API does not return them (decisions.md #14).
//
// This page manages destinations and who may use them, nothing else. It shows
// no Space, no run and no user data (decisions.md #15).
import { onMounted, ref } from 'vue'
import { asApiError, type AdminTarget, type ApiError } from '../api'
import { adminErrorAdvice, adminErrorTitle } from '../admin/wording'
import AdminOnly from '../components/admin/AdminOnly.vue'
import PageLayout from '../components/PageLayout.vue'
import { destinationsCrumbs } from '../layout/breadcrumbs'
import RequestState from '../components/RequestState.vue'
import { useAdminApi } from '../composables/useAdminApi'
import { useIsAdmin } from '../composables/useIsAdmin'

const api = useAdminApi()
const isAdmin = useIsAdmin()

const loading = ref(true)
const error = ref<ApiError | undefined>(undefined)
const targets = ref<AdminTarget[]>([])

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

/** location is "endpoint / bucket / prefix", as far as it goes. */
function location(target: AdminTarget): string {
  return [target.endpoint, target.bucket, target.prefix].filter(Boolean).join(' / ')
}

onMounted(() => {
  if (isAdmin) {
    void load()
  }
})
</script>

<template>
  <PageLayout :crumbs="destinationsCrumbs($gettext)" narrow>
    <AdminOnly>
      <RequestState
        :loading="loading"
        :error="error"
        :title="adminErrorTitle"
        :advice="adminErrorAdvice"
        @retry="load"
      >
        <p v-if="targets.length === 0" data-testid="no-destinations">
          {{
            $gettext(
              'There are no backup destinations yet. Nobody can set up backups until there is one.'
            )
          }}
        </p>
        <ul v-else class="ext:flex ext:flex-col ext:gap-2" data-testid="destinations">
          <li
            v-for="target in targets"
            :key="target.id"
            class="ext:rounded ext:border ext:p-3"
            :data-target-id="target.id"
          >
            <router-link
              :to="{ name: 'backup-vault-admin-target', params: { targetId: target.id } }"
              class="ext:font-medium"
            >
              {{ target.name }}
            </router-link>
            <p class="ext:text-sm ext:text-role-on-surface-variant" data-testid="location">
              {{ location(target) }}
            </p>
            <p class="ext:text-sm ext:text-role-on-surface-variant" data-testid="key-pairs">
              {{
                target.maintenance_configured
                  ? $gettext('Separate maintenance keys')
                  : $gettext('One key pair for everything')
              }}
            </p>
          </li>
        </ul>

        <div class="ext:mt-3">
          <router-link :to="{ name: 'backup-vault-admin-target-new' }" data-testid="add">
            {{ $gettext('Add a backup destination') }}
          </router-link>
        </div>
      </RequestState>
    </AdminOnly>
  </PageLayout>
</template>
