<script setup lang="ts">
// Admin: a new backup destination. Creating it saves the settings and keys
// only; the page then moves to the destination's own page, where who may use
// it is chosen (8e decision 10). Until then nobody can.
//
// The router comes from web-pkg, which the host shares: importing vue-router
// itself would bundle a second copy (see composables/useBackupApi.ts).
import { useRouter } from '@opencloud-eu/web-pkg'
import type { AdminTarget } from '../api'
import AdminOnly from '../components/admin/AdminOnly.vue'
import TargetSettingsForm from '../components/admin/TargetSettingsForm.vue'

const router = useRouter()

function created(target: AdminTarget): void {
  void router.replace({ name: 'backup-vault-admin-target', params: { targetId: target.id } })
}
</script>

<template>
  <main class="ext:p-4 ext:flex ext:flex-col ext:gap-4 ext:max-w-2xl">
    <router-link
      :to="{ name: 'backup-vault-admin-targets' }"
      class="ext:text-sm"
      data-testid="back"
    >
      {{ $gettext('Back to backup destinations') }}
    </router-link>

    <h1 class="ext:text-xl ext:font-semibold">{{ $gettext('Add a backup destination') }}</h1>

    <AdminOnly>
      <TargetSettingsForm @saved="created" />
    </AdminOnly>
  </main>
</template>
