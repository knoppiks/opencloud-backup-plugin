<script setup lang="ts">
// Admin: one backup destination — its settings and keys, who may use it, and
// deleting it. Three independent saves, because they are three independent
// routes on the server and a failure in one should not cost the others.
import { useRouter } from '@opencloud-eu/web-pkg'
import { onMounted, ref } from 'vue'
import { asApiError, type AdminTarget, type ApiError } from '../api'
import { adminErrorAdvice, adminErrorTitle } from '../admin/wording'
import AdminOnly from '../components/admin/AdminOnly.vue'
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
  <main class="ext:p-4 ext:flex ext:flex-col ext:gap-6 ext:max-w-2xl">
    <router-link
      :to="{ name: 'backup-vault-admin-targets' }"
      class="ext:text-sm"
      data-testid="back"
    >
      {{ $gettext('Back to backup destinations') }}
    </router-link>

    <h1 class="ext:text-xl ext:font-semibold">
      {{ target ? target.name : $gettext('Backup destination') }}
    </h1>

    <AdminOnly>
      <RequestState
        :loading="loading"
        :error="error"
        :title="adminErrorTitle"
        :advice="adminErrorAdvice"
        @retry="load"
      >
        <template v-if="target">
          <section class="ext:flex ext:flex-col ext:gap-3" data-testid="settings">
            <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Settings') }}</h2>
            <TargetSettingsForm :target="target" @saved="saved" />
          </section>
          <TargetAudience :target-id="target.id" />
          <DeleteTarget :target-id="target.id" :target-name="target.name" @deleted="deleted" />
        </template>
      </RequestState>
    </AdminOnly>
  </main>
</template>
