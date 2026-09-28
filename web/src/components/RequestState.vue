<script setup lang="ts">
// The four ways a view can have nothing to show, in one place.
//
// The phase plan asks for error states "designed, not bolted on". This is what
// that means concretely: every view that loads something renders through here,
// so "backend down", "token expired", "not allowed" and "nothing yet" look the
// same everywhere and are worded once.
//
// The wording lives in api/errortext.ts, shared with inline action failures.
// The rule this component adds: **offer a retry only when retrying could
// work.** A 403 will fail identically forever, and a retry button next to one
// is a lie.
//
// `oc-*` components are host globals on purpose: importing from
// @opencloud-eu/design-system would bundle a second copy of it into this remote.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError } from '../api'
import { errorAdvice, errorTitle } from '../api/errortext'

const props = defineProps<{
  loading: boolean
  error: ApiError | undefined
  /** empty says the request succeeded and returned nothing. */
  empty?: boolean
  /** emptyMessage overrides the generic "nothing here yet" wording. */
  emptyMessage?: string
}>()

const emit = defineEmits<{ retry: [] }>()

const { $gettext } = useGettext()

const title = computed(() => (props.error ? errorTitle(props.error.code, $gettext) : ''))

const advice = computed(() => (props.error ? errorAdvice(props.error.code, $gettext) : ''))

/**
 * detail is the service's own message.
 *
 * Only shown when it is not simply a restatement of the title, so an operator
 * reading a bug report gets the specifics without a user reading the same
 * sentence twice.
 */
const detail = computed(() => props.error?.serverMessage ?? '')

const showRetry = computed(() => props.error?.isRetryable === true)
</script>

<template>
  <div v-if="loading" class="ext:flex ext:items-center ext:gap-3 ext:p-6">
    <oc-spinner :aria-label="$gettext('Loading')" />
    <span class="ext:text-role-on-surface-variant">{{ $gettext('Loading…') }}</span>
  </div>

  <div v-else-if="error" class="ext:p-6" role="alert">
    <h2 class="ext:text-lg ext:font-semibold">{{ title }}</h2>
    <p v-if="advice" class="ext:mt-1">{{ advice }}</p>
    <p v-if="detail" class="ext:mt-1 ext:text-sm ext:text-role-on-surface-variant">{{ detail }}</p>
    <oc-button v-if="showRetry" class="ext:mt-3" appearance="outline" @click="emit('retry')">
      {{ $gettext('Try again') }}
    </oc-button>
  </div>

  <div v-else-if="empty" class="ext:p-6 ext:text-role-on-surface-variant">
    {{ emptyMessage || $gettext('There is nothing here yet.') }}
  </div>

  <slot v-else />
</template>
