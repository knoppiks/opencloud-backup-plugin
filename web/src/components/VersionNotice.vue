<script setup lang="ts">
// Says when this bundle and the backup service do not belong together
// (version/mismatch.ts). Shown on the landing pages, the member Overview and
// the admin's destinations; the service is asked once per page load.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { API_LEVEL } from '../api/apilevel'
import { APP_VERSION } from '../version/appversion'
import { versionNotice, type BundleVersion } from '../version/mismatch'
import { useServiceVersion, type VersionSource } from '../version/useServiceVersion'
import NoticeBanner from './NoticeBanner.vue'

const props = withDefaults(
  defineProps<{ api: VersionSource; isAdmin: boolean; bundle?: BundleVersion }>(),
  { bundle: () => ({ version: APP_VERSION, apiLevel: API_LEVEL }) }
)

const { $gettext } = useGettext()
const service = useServiceVersion(props.api)
const notice = computed(() => versionNotice(service.value, props.bundle, props.isAdmin, $gettext))
</script>

<template>
  <NoticeBanner
    v-if="notice"
    :tone="notice.tone"
    :title="notice.title"
    :message="notice.message"
    data-testid="version-mismatch"
    :data-audience="isAdmin ? 'admin' : 'member'"
  />
</template>
