<script setup lang="ts">
// The inline failure of an action: what went wrong, what to do about it, and
// the service's own words underneath. Worded by api/errortext.ts, like the
// load-failure panel, so one problem reads as one sentence everywhere.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError } from '../api'
import { errorAdvice, errorTitle, type Gettext } from '../api/errortext'
import type { ApiFailureCode } from '../api/errors'

type Wording = (code: ApiFailureCode, $gettext: Gettext) => string

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
  <div role="alert" class="ext:text-sm" data-testid="action-error">
    <p class="ext:font-medium">{{ headline }}</p>
    <p v-if="hint">{{ hint }}</p>
    <p v-if="error.serverMessage" class="ext:text-role-on-surface-variant">
      {{ error.serverMessage }}
    </p>
  </div>
</template>
