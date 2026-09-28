<script setup lang="ts">
// The Backup Vault landing page: one card per Space, each saying whether that
// Space is protected.
//
// The Space list gates the page; each Space's status then loads on its own.
// A status that fails shows on its card only. One Space whose status cannot
// be read is not a reason to hide the others, least of all the ones that are
// fine.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { computed, onMounted, reactive, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { useBackupApi } from '../composables/useBackupApi'
import RequestState from '../components/RequestState.vue'
import SpaceCard from '../components/SpaceCard.vue'
import { ApiError, isApiError, type BackupStatus, type Space, type Target } from '../api'

const { $gettext } = useGettext()
const api = useBackupApi()

const loading = ref(true)
const error = ref<ApiError | undefined>(undefined)
const spaces = ref<Space[]>([])
const targets = ref<Target[]>([])

/** SpaceResult is one card's status, or why it has none. */
interface SpaceResult {
  status?: BackupStatus
  error?: ApiError
}
const results = reactive<Record<string, SpaceResult>>({})

/** Personal Space first, then shared ones by name: "mine" is what people look for. */
const sortedSpaces = computed(() =>
  [...spaces.value].sort((a, b) => {
    const personal = Number(b.type === 'personal') - Number(a.type === 'personal')
    return personal !== 0 ? personal : a.name.localeCompare(b.name)
  })
)

function asApiError(err: unknown): ApiError {
  // An unexpected throw is still shown, not swallowed: a blank page is the one
  // outcome worse than an ugly error.
  return isApiError(err) ? err : new ApiError('unknown', $gettext('Something went wrong'))
}

async function loadStatus(space: Space): Promise<void> {
  try {
    results[space.id] = { status: await api.status(space.id) }
  } catch (err: unknown) {
    results[space.id] = { error: asApiError(err) }
  }
}

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
    error.value = asApiError(err)
    return
  } finally {
    loading.value = false
  }
  for (const id of Object.keys(results)) {
    delete results[id]
  }
  await Promise.all(spaces.value.map(loadStatus))
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
      <p v-if="targets.length === 0" class="ext:mt-3" role="status" data-testid="no-targets">
        {{
          $gettext(
            'No backup destination has been shared with you yet. Ask your administrator to grant you one.'
          )
        }}
      </p>

      <ul class="ext:mt-4 ext:grid ext:gap-3 ext:sm:grid-cols-2 ext:xl:grid-cols-3">
        <li v-for="space in sortedSpaces" :key="space.id">
          <SpaceCard
            :space="space"
            :status="results[space.id]?.status"
            :error="results[space.id]?.error"
          />
        </li>
      </ul>
    </RequestState>
  </main>
</template>
