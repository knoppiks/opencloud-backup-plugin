<script setup lang="ts">
// Deleting a target: asked first, and refused by the server while Spaces
// still back up to it (8e decision 7).
//
// The refusal carries a count and never a Space (decisions.md #15): the admin
// learns how much is in the way, not whose. What was stored on the bucket is
// not touched either way; only the backup service forgets the target.
import { computed, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { asApiError, type ApiError } from '../../api'
import { adminErrorAdvice, adminErrorTitle, spacesInUse } from '../../admin/wording'
import { useAdminApi } from '../../composables/useAdminApi'
import ActionError from '../ActionError.vue'

const props = defineProps<{ targetId: string; targetName: string }>()
const emit = defineEmits<{ deleted: [] }>()

const { $gettext } = useGettext()
const api = useAdminApi()

const confirming = ref(false)
const deleting = ref(false)
const error = ref<ApiError | undefined>(undefined)

const inUse = computed(() => error.value?.code === 'target_in_use')

const inUseText = computed(() => {
  const count = spacesInUse(error.value?.serverMessage)
  return count === undefined
    ? $gettext(
        'Some spaces still back up to this destination. They have to be switched to another destination first.'
      )
    : // Worded around the number, not with it: "1 spaces" is wrong, and the
      // catalogue has no plural forms.
      $gettext(
        'Spaces still backing up to this destination: %{count}. They have to be switched to another destination first.',
        { count: String(count) }
      )
})

function ask(): void {
  error.value = undefined
  confirming.value = true
}

async function remove(): Promise<void> {
  deleting.value = true
  error.value = undefined
  try {
    await api.deleteTarget(props.targetId)
    emit('deleted')
  } catch (err: unknown) {
    const failure = asApiError(err)
    // Already gone is what was asked for.
    if (failure.code === 'not_found') {
      emit('deleted')
      return
    }
    error.value = failure
    confirming.value = false
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <section class="ext:flex ext:flex-col ext:gap-2" data-testid="delete-target">
    <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Delete this destination') }}</h2>

    <div v-if="!confirming">
      <oc-button appearance="outline" data-testid="delete" @click="ask">
        {{ $gettext('Delete…') }}
      </oc-button>
    </div>

    <div v-else role="group" class="ext:flex ext:flex-col ext:gap-2" data-testid="confirm-delete">
      <p class="ext:font-medium">
        {{ $gettext('Delete “%{name}”?', { name: targetName }) }}
      </p>
      <p class="ext:text-sm">
        {{
          $gettext(
            'Nobody can choose it for new backups any more. The backups already stored in the bucket are not deleted.'
          )
        }}
      </p>
      <div class="ext:flex ext:gap-2">
        <oc-button
          appearance="filled"
          :disabled="deleting"
          :show-spinner="deleting"
          data-testid="confirm"
          @click="remove"
        >
          {{ $gettext('Delete') }}
        </oc-button>
        <oc-button
          appearance="outline"
          :disabled="deleting"
          data-testid="cancel-delete"
          @click="confirming = false"
        >
          {{ $gettext('Cancel') }}
        </oc-button>
      </div>
    </div>

    <div v-if="inUse" role="alert" class="ext:text-sm" data-testid="in-use">
      <p class="ext:font-medium">{{ adminErrorTitle('target_in_use', $gettext) }}</p>
      <p>{{ inUseText }}</p>
    </div>
    <ActionError
      v-else-if="error"
      :error="error"
      :title="adminErrorTitle"
      :advice="adminErrorAdvice"
    />
  </section>
</template>
