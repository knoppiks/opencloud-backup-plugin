<script setup lang="ts">
// The Backup Vault landing page.
//
// Sub-phase 8c's job is the skeleton: this view proves the extension loads,
// that the API client reaches backupd through the same origin with the user's
// own token, and that every failure has somewhere to be shown. The per-Space
// cards, the setup wizard and the status board are 8d — what is here is the
// Space list plus whether each Space has been set up, which is exactly the data
// those cards will hang off.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { onMounted, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useBackupApi } from '../composables/useBackupApi'
import RequestState from '../components/RequestState.vue'
import { ApiError, isApiError, type Space, type Target } from '../api'

const { $gettext } = useGettext()
const api = useBackupApi()

const loading = ref(true)
const error = ref<ApiError | undefined>(undefined)
const spaces = ref<Space[]>([])
const targets = ref<Target[]>([])

async function load(): Promise<void> {
  loading.value = true
  error.value = undefined
  try {
    // Both are cheap and neither depends on the other, so one round trip's
    // worth of latency instead of two.
    const [loadedSpaces, loadedTargets] = await Promise.all([api.listSpaces(), api.listTargets()])
    spaces.value = loadedSpaces
    targets.value = loadedTargets
  } catch (err: unknown) {
    // An unexpected throw is still shown, not swallowed: a blank page is the
    // one outcome worse than an ugly error.
    error.value = isApiError(err) ? err : new ApiError('unknown', $gettext('Something went wrong'))
  } finally {
    loading.value = false
  }
}

onMounted(load)
</script>

<template>
  <main class="ext:p-4">
    <h1 class="ext:text-xl ext:font-semibold">{{ $gettext('Backup Vault') }}</h1>

    <RequestState
      :loading="loading"
      :error="error"
      :empty="spaces.length === 0"
      :empty-message="$gettext('You do not have any spaces to back up.')"
      @retry="load"
    >
      <!--
        No granted target means setup cannot complete, however many Spaces
        there are. Saying so here beats letting a user reach the wizard and
        find an empty picker (decisions.md #12: the admin grants targets).
      -->
      <p v-if="targets.length === 0" class="ext:mt-3" role="status">
        {{
          $gettext(
            'No backup destination has been shared with you yet. Ask your administrator to grant you one.'
          )
        }}
      </p>

      <ul class="ext:mt-4 ext:flex ext:flex-col ext:gap-2">
        <li
          v-for="space in spaces"
          :key="space.id"
          class="ext:rounded ext:border ext:border-role-outline-variant ext:p-3"
        >
          <span class="ext:font-medium">{{ space.name }}</span>
          <span class="ext:ml-2 ext:text-sm ext:text-role-on-surface-variant">
            {{ space.type }}
          </span>
        </li>
      </ul>
    </RequestState>
  </main>
</template>
