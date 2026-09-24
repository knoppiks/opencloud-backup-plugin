<script setup lang="ts">
// One Space on the overview: its name, whether it is protected, and the one
// fact that supports that answer.
//
// The card loads nothing itself. The overview fetches every Space's status in
// parallel and hands each card its own result, so one Space whose status
// cannot be read shows an error on its own card instead of blanking the page.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import type { ApiError, BackupStatus, Space } from '../api'
import { errorTitle } from '../api/errortext'
import { useFormat } from '../composables/useFormat'
import { needsAttention, spaceState } from '../status/spacestate'
import { stateAdvice, stateLabel } from '../status/statetext'

const props = defineProps<{
  space: Space
  status: BackupStatus | undefined
  error: ApiError | undefined
}>()

const { $gettext } = useGettext()
const format = useFormat()

const state = computed(() => (props.status ? spaceState(props.status) : undefined))

const kindLabel = computed(() =>
  props.space.type === 'personal' ? $gettext('Personal space') : $gettext('Shared space')
)

/** summary is the one supporting fact under the badge. */
const summary = computed(() => {
  const status = props.status
  if (!status || !state.value) {
    return ''
  }
  switch (state.value) {
    case 'running':
      return status.current_job
        ? $gettext('Running since %{when}', { when: format.when(status.current_job.created_at) })
        : ''
    case 'active':
      return status.last_successful_run
        ? $gettext('Last backup %{when}', {
            when: format.when(status.last_successful_run.created_at)
          })
        : ''
    case 'stale':
      return status.stale_since
        ? $gettext('No successful backup since %{when}', { when: format.when(status.stale_since) })
        : stateAdvice(state.value, $gettext)
    case 'failed':
      return status.last_successful_run
        ? $gettext('Last successful backup %{when}', {
            when: format.when(status.last_successful_run.created_at)
          })
        : $gettext('No backup has succeeded yet.')
    default:
      return stateAdvice(state.value, $gettext)
  }
})

const attention = computed(() => (state.value ? needsAttention(state.value) : false))
</script>

<template>
  <article
    class="ext:rounded ext:border ext:p-4"
    :class="attention ? 'ext:border-role-error' : 'ext:border-role-outline-variant'"
    :data-state="state ?? (error ? 'error' : 'loading')"
  >
    <header class="ext:flex ext:items-baseline ext:justify-between ext:gap-2">
      <router-link
        :to="{ name: 'backup-vault-space', params: { spaceId: space.id } }"
        class="ext:font-semibold"
      >
        {{ space.name }}
      </router-link>
      <span class="ext:text-sm ext:text-role-on-surface-variant">{{ kindLabel }}</span>
    </header>

    <div v-if="status && state" class="ext:mt-2">
      <p
        class="ext:font-medium"
        :class="attention ? 'ext:text-role-error' : ''"
        data-testid="state-label"
      >
        {{ stateLabel(state, $gettext) }}
      </p>
      <p v-if="summary" class="ext:text-sm" data-testid="state-summary">{{ summary }}</p>
    </div>
    <p v-else-if="error" class="ext:mt-2 ext:text-sm" role="alert">
      {{ errorTitle(error.code, $gettext) }}
    </p>
    <p v-else class="ext:mt-2 ext:text-sm ext:text-role-on-surface-variant">
      {{ $gettext('Loading…') }}
    </p>
  </article>
</template>
