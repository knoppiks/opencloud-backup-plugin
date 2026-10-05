<script setup lang="ts">
// Admin: one backup destination — its connection and keys, who may use it,
// and deleting it, each under its own heading (Phase 8g). Three independent
// saves, because they are three independent routes on the server and a
// failure in one should not cost the others. Connection and keys share one
// save: the server takes them in one request.
import { useRouter } from '@opencloud-eu/web-pkg'
import { onMounted, ref } from 'vue'
import { asApiError, type AdminTarget, type ApiError } from '../api'
import { adminErrorAdvice, adminErrorTitle } from '../admin/wording'
import AdminOnly from '../components/admin/AdminOnly.vue'
import PageLayout from '../components/PageLayout.vue'
import { destinationsCrumbs } from '../layout/breadcrumbs'
import DeleteTarget from '../components/admin/DeleteTarget.vue'
import TargetAudience from '../components/admin/TargetAudience.vue'
import TargetSettingsForm from '../components/admin/TargetSettingsForm.vue'
import RequestState from '../components/RequestState.vue'
import { useAdminApi } from '../composables/useAdminApi'
import { useIsAdmin } from '../composables/useIsAdmin'

const props = defineProps<{ targetId: string }>()

const api = useAdminApi()
const isAdmin = useIsAdmin()
const router = useRouter()

const loading = ref(true)
const error = ref<ApiError | undefined>(undefined)
const target = ref<AdminTarget | undefined>(undefined)

async function load(): Promise<void> {
  loading.value = true
  error.value = undefined
  try {
    target.value = await api.target(props.targetId)
  } catch (err: unknown) {
    error.value = asApiError(err)
  } finally {
    loading.value = false
  }
}

function saved(stored: AdminTarget): void {
  target.value = stored
}

function deleted(): void {
  void router.replace({ name: 'backup-vault-admin-targets' })
}

onMounted(() => {
  if (isAdmin) {
    void load()
  }
})
</script>

<template>
  <PageLayout
    :crumbs="
      destinationsCrumbs($gettext, { text: target ? target.name : $gettext('Backup destination') })
    "
    narrow
  >
    <AdminOnly>
      <RequestState
        :loading="loading"
        :error="error"
        :title="adminErrorTitle"
        :advice="adminErrorAdvice"
        @retry="load"
      >
        <template v-if="target">
          <div class="ext:flex ext:flex-col ext:gap-8">
            <div data-testid="settings">
              <TargetSettingsForm :target="target" @saved="saved" />
            </div>
            <TargetAudience :target-id="target.id" />
            <DeleteTarget :target-id="target.id" :target-name="target.name" @deleted="deleted" />
          </div>
        </template>
      </RequestState>
    </AdminOnly>
  </PageLayout>
</template>
