<script setup lang="ts">
// The inline failure of an action: what went wrong, what to do about it, and
// the service's own words underneath. Worded by api/errortext.ts, like the
// load-failure panel, so one problem reads as one sentence everywhere. Drawn
// as a danger notice, so it is read out at once.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError } from '../api'
import { errorAdvice, errorTitle, type Wording } from '../api/errortext'
import NoticeBanner from './NoticeBanner.vue'

const props = defineProps<{
  error: ApiError
  /** title and advice replace the shared wording where a page says it better. */
  title?: Wording
  advice?: Wording
}>()

const { $gettext } = useGettext()
const headline = computed(() => (props.title ?? errorTitle)(props.error.code, $gettext))
const hint = computed(() => (props.advice ?? errorAdvice)(props.error.code, $gettext))
</script>

<template>
  <NoticeBanner tone="danger" :title="headline" :message="hint" data-testid="action-error">
    <p v-if="error.serverMessage" class="ext:text-sm ext:opacity-80">
      {{ error.serverMessage }}
    </p>
  </NoticeBanner>
</template>
