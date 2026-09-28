<script setup lang="ts">
// How long a Space keeps its backups, and — for editors — changing it.
//
// Saving sends only `retention_days` through PATCH, never the rest of the
// record: re-sending the target and the enabled flag as read a moment ago
// would silently undo someone else's concurrent change to them (8d decision
// 6). The floor is explained beside the field, before the server has to refuse
// anything; the server's refusal is still shown if it comes.
import { computed, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { ApiError, isApiError } from '../api'
import { errorTitle } from '../api/errortext'
import { useBackupApi } from '../composables/useBackupApi'
import { MIN_RETENTION_DAYS, parseRetentionDays } from '../status/retention'

const props = defineProps<{
  spaceId: string
  retentionDays: number
  editable: boolean
}>()

const emit = defineEmits<{ saved: [days: number] }>()

const { $gettext } = useGettext()
const api = useBackupApi()

const editing = ref(false)
const input = ref('')
const saving = ref(false)
const saveError = ref<ApiError | undefined>(undefined)

const parsed = computed(() => parseRetentionDays(input.value))

const inputProblem = computed(() => {
  const result = parsed.value
  if (!('problem' in result)) {
    return ''
  }
  return result.problem === 'below_floor'
    ? $gettext('Backups must be kept for at least %{days} days.', {
        days: String(MIN_RETENTION_DAYS)
      })
    : $gettext('Enter a whole number of days.')
})

function startEditing(): void {
  input.value = String(props.retentionDays)
  saveError.value = undefined
  editing.value = true
}

function cancel(): void {
  editing.value = false
  saveError.value = undefined
}

async function save(): Promise<void> {
  const result = parsed.value
  if (!('days' in result)) {
    return
  }
  saving.value = true
  saveError.value = undefined
  try {
    const config = await api.patchBackupConfig(props.spaceId, { retention_days: result.days })
    editing.value = false
    emit('saved', config.retention_days)
  } catch (err: unknown) {
    saveError.value = isApiError(err)
      ? err
      : new ApiError('unknown', $gettext('Something went wrong'))
  } finally {
    saving.value = false
  }
}
</script>

<template>
  <div>
    <template v-if="!editing">
      <span data-testid="retention-days">
        {{ $gettext('%{days} days', { days: String(retentionDays) }) }}
      </span>
      <oc-button
        v-if="editable"
        class="ext:ml-2"
        appearance="raw"
        data-testid="retention-edit"
        @click="startEditing"
      >
        {{ $gettext('Change') }}
      </oc-button>
    </template>

    <form v-else class="ext:flex ext:flex-col ext:gap-2" @submit.prevent="save">
      <oc-text-input
        v-model="input"
        type="number"
        :label="$gettext('Keep backups for (days)')"
        :error-message="inputProblem"
        :description-message="
          $gettext(
            'At least %{days} days. A longer history gives you more time to notice that files were damaged or encrypted before the last good copy is gone.',
            { days: String(MIN_RETENTION_DAYS) }
          )
        "
        :disabled="saving"
      />
      <p v-if="saveError" role="alert" class="ext:text-sm">
        {{ errorTitle(saveError.code, $gettext) }}
        <span v-if="saveError.serverMessage" class="ext:block ext:text-role-on-surface-variant">
          {{ saveError.serverMessage }}
        </span>
      </p>
      <div class="ext:flex ext:gap-2">
        <oc-button
          submit="submit"
          appearance="filled"
          :disabled="saving || inputProblem !== ''"
          :show-spinner="saving"
          data-testid="retention-save"
        >
          {{ $gettext('Save') }}
        </oc-button>
        <oc-button appearance="outline" :disabled="saving" @click="cancel">
          {{ $gettext('Cancel') }}
        </oc-button>
      </div>
    </form>
  </div>
</template>
