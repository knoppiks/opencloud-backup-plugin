<script setup lang="ts">
// The inline failure of an action: what went wrong, what to do about it, and
// the service's own words underneath. Worded by api/errortext.ts, like the
// load-failure panel, so one problem reads as one sentence everywhere.
import { ApiError } from '../api'
import { errorAdvice, errorTitle } from '../api/errortext'

defineProps<{ error: ApiError }>()
</script>

<template>
  <div role="alert" class="ext:text-sm" data-testid="action-error">
    <p class="ext:font-medium">{{ errorTitle(error.code, $gettext) }}</p>
    <p v-if="errorAdvice(error.code, $gettext)">{{ errorAdvice(error.code, $gettext) }}</p>
    <p v-if="error.serverMessage" class="ext:text-role-on-surface-variant">
      {{ error.serverMessage }}
    </p>
  </div>
</template>
