<script setup lang="ts">
// Deleting a target: asked first in the host's own dialog, and refused by the
// server while Spaces still back up to it (8e decision 7).
//
// The dialog is web-pkg's modal store, the one Files uses to confirm a delete
// (Phase 8g). The delete runs from its confirm button and never throws back
// into it: whatever the answer, the dialog closes and the outcome is shown on
// the page, next to the button that started it.
//
// The refusal carries a count and never a Space (decisions.md #15): the admin
// learns how much is in the way, not whose. What was stored on the bucket is
// not touched either way; only the backup service forgets the target.
import { useModals } from '@opencloud-eu/web-pkg'
import { computed, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { asApiError, type ApiError } from '../../api'
import { adminErrorAdvice, adminErrorTitle, spacesInUse } from '../../admin/wording'
import { useAdminApi } from '../../composables/useAdminApi'
import ActionError from '../ActionError.vue'
import NoticeBanner from '../NoticeBanner.vue'

const props = defineProps<{ targetId: string; targetName: string }>()
const emit = defineEmits<{ deleted: [] }>()

const { $gettext } = useGettext()
const api = useAdminApi()
const { dispatchModal } = useModals()

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

const consequence = computed(() =>
  $gettext(
    'Nobody can choose it for new backups any more. The backups already stored in the bucket are not deleted.'
  )
)

function ask(): void {
  error.value = undefined
  dispatchModal({
    title: $gettext('Delete “%{name}”?', { name: props.targetName }),
    message: consequence.value,
    confirmText: $gettext('Delete'),
    onConfirm: remove
  })
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
  } finally {
    deleting.value = false
  }
}
</script>

<template>
  <section class="ext:flex ext:flex-col ext:gap-3" data-testid="delete-target">
    <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Danger zone') }}</h2>

    <div
      class="ext:flex ext:flex-wrap ext:items-center ext:justify-between ext:gap-3 ext:rounded-xl ext:border ext:border-role-error ext:px-4 ext:py-3"
    >
      <div class="ext:flex ext:flex-col ext:gap-1 ext:min-w-0 ext:flex-1">
        <p class="ext:font-semibold">{{ $gettext('Delete this destination') }}</p>
        <p class="ext:text-sm ext:text-role-on-surface-variant">{{ consequence }}</p>
      </div>
      <oc-button
        appearance="outline"
        :disabled="deleting"
        :show-spinner="deleting"
        data-testid="delete"
        @click="ask"
      >
        {{ $gettext('Delete…') }}
      </oc-button>
    </div>

    <NoticeBanner
      v-if="inUse"
      tone="danger"
      :title="adminErrorTitle('target_in_use', $gettext)"
      :message="inUseText"
      data-testid="in-use"
    />
    <ActionError
      v-else-if="error"
      :error="error"
      :title="adminErrorTitle"
      :advice="adminErrorAdvice"
    />
  </section>
</template>
