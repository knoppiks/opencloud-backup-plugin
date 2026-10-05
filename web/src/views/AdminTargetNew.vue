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
import PageLayout from '../components/PageLayout.vue'
import { destinationsCrumbs } from '../layout/breadcrumbs'
import TargetSettingsForm from '../components/admin/TargetSettingsForm.vue'

const router = useRouter()

function created(target: AdminTarget): void {
  void router.replace({ name: 'backup-vault-admin-target', params: { targetId: target.id } })
}
</script>

<template>
  <PageLayout
    :crumbs="destinationsCrumbs($gettext, { text: $gettext('Add a backup destination') })"
    narrow
  >
    <AdminOnly>
      <TargetSettingsForm @saved="created" />
    </AdminOnly>
  </PageLayout>
</template>
