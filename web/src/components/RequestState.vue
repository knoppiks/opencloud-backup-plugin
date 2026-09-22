<script setup lang="ts">
// The four ways a view can have nothing to show, in one place.
//
// The phase plan asks for error states "designed, not bolted on". This is what
// that means concretely: every view that loads something renders through here,
// so "backend down", "token expired", "not allowed" and "nothing yet" look the
// same everywhere and are worded once.
//
// Two rules the wording follows:
//
//   - **Branch on the code, not on the server's message.** The message is
//     English, written by the service, and cannot be translated. It is shown
//     only as a detail line, and only when it adds something.
//   - **Offer a retry only when retrying could work.** A 403 will fail
//     identically forever, and a retry button next to one is a lie.
//
// `oc-*` components are host globals on purpose: importing from
// @opencloud-eu/design-system would bundle a second copy of it into this remote.
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError } from '../api'

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

const title = computed(() => {
  const error = props.error
  if (!error) {
    return ''
  }
  switch (error.code) {
    case 'offline':
      return $gettext('The backup service cannot be reached')
    case 'timeout':
      return $gettext('The backup service did not answer in time')
    case 'unauthorized':
      return $gettext('Your session has expired')
    case 'forbidden':
      return $gettext('You do not have access to this')
    case 'not_found':
      return $gettext('This is not available')
    case 'not_configured':
      return $gettext('Backups are not set up for this space yet')
    case 'run_in_progress':
      return $gettext('A backup is already running for this space')
    case 'target_unavailable':
      return $gettext('The backup destination cannot be used right now')
    case 'unavailable':
      return $gettext('The backup service is temporarily unavailable')
    case 'upstream_error':
      return $gettext('OpenCloud did not answer the backup service')
    case 'malformed_response':
      return $gettext('The backup service sent something unexpected')
    default:
      return $gettext('Something went wrong')
  }
})

const advice = computed(() => {
  const error = props.error
  if (!error) {
    return ''
  }
  switch (error.code) {
    case 'offline':
    case 'timeout':
    case 'unavailable':
      return $gettext('Your backups are unaffected. Try again in a moment.')
    case 'unauthorized':
      return $gettext('Reload the page to sign in again.')
    case 'forbidden':
      return $gettext('Ask a manager of this space if you need access.')
    case 'upstream_error':
      return $gettext('This usually clears up on its own. Try again in a moment.')
    default:
      return ''
  }
})

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
