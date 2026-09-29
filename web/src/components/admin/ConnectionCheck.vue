<script setup lang="ts">
// "Check connection": tries the typed settings and keys against the bucket and
// says one outcome per key pair (8e decision 6).
//
// It never runs by itself, and it only ever sends what is typed. The server's
// check is stateless on purpose — checking stored keys against an edited
// endpoint would hand them to whatever host the endpoint names — so an
// existing target is checked by typing its keys again.
//
// A result describes the inputs it was run with. Any edit clears it, so a
// green "Works" never sits next to settings it was not about.
import { ref, watch } from 'vue'
import { useGettext } from 'vue3-gettext'
import { asApiError, type AdminTargetRequest, type ApiError, type CheckResult } from '../../api'
import {
  adminErrorAdvice,
  adminErrorTitle,
  checkOutcomeText,
  checkRoleText
} from '../../admin/wording'
import { useAdminApi } from '../../composables/useAdminApi'
import ActionError from '../ActionError.vue'

const props = defineProps<{
  /** request is the body to check, or undefined while the form has problems. */
  request: AdminTargetRequest | undefined
  /** ready enables the button: a backup key pair has been typed. */
  ready: boolean
}>()

const emit = defineEmits<{ invalid: [] }>()

const { $gettext } = useGettext()
const api = useAdminApi()

const checking = ref(false)
const results = ref<CheckResult[]>([])
const error = ref<ApiError | undefined>(undefined)

watch(
  () => props.request,
  () => {
    results.value = []
    error.value = undefined
  }
)

async function check(): Promise<void> {
  const body = props.request
  if (body === undefined) {
    emit('invalid')
    return
  }
  checking.value = true
  results.value = []
  error.value = undefined
  try {
    results.value = await api.checkTarget(body)
  } catch (err: unknown) {
    error.value = asApiError(err)
  } finally {
    checking.value = false
  }
}
</script>

<template>
  <div class="ext:flex ext:flex-col ext:gap-2" data-testid="connection-check">
    <div>
      <oc-button
        appearance="outline"
        :disabled="!ready || checking"
        :show-spinner="checking"
        data-testid="check"
        @click="check"
      >
        {{ $gettext('Check connection') }}
      </oc-button>
    </div>
    <ul v-if="results.length > 0" aria-live="polite" data-testid="check-results">
      <li
        v-for="result in results"
        :key="result.role"
        :data-role="result.role"
        :data-outcome="result.outcome"
      >
        <span class="ext:font-medium">{{ checkRoleText(result.role, $gettext) }}:</span>
        {{ checkOutcomeText(result.outcome, $gettext) }}
      </li>
    </ul>
    <ActionError v-if="error" :error="error" :title="adminErrorTitle" :advice="adminErrorAdvice" />
  </div>
</template>
