<script setup lang="ts">
// Find an OpenCloud user to grant a target to.
//
// Searches as the admin types, the way upstream share dialogs do: after a
// pause of SEARCH_DELAY_MS, and only once the term is as long as the server's
// sharing capability asks. An answer that arrives after a newer search was
// started is dropped, so the list always belongs to what is in the box.
import { onBeforeUnmount, ref, watch } from 'vue'
import { useGettext } from 'vue3-gettext'
import { searchTerm, type DirectoryUser } from '../../admin/directory'
import { useUserDirectory, useUserSearchMinLength } from '../../composables/useUserDirectory'
import NoticeBanner from '../NoticeBanner.vue'

/** SEARCH_DELAY_MS matches the debounce of upstream share dialogs. */
const SEARCH_DELAY_MS = 500

const props = defineProps<{ excludeIds: string[] }>()
const emit = defineEmits<{ pick: [user: DirectoryUser] }>()

const { $gettext } = useGettext()
const directory = useUserDirectory()
const minLength = Math.max(1, useUserSearchMinLength())

const query = ref('')
const results = ref<DirectoryUser[]>([])
const searching = ref(false)
const failed = ref(false)
const searched = ref(false)

let timer: ReturnType<typeof globalThis.setTimeout> | undefined
let generation = 0

function tooShort(): boolean {
  return searchTerm(query.value).length < minLength
}

function reset(): void {
  results.value = []
  searched.value = false
  failed.value = false
}

watch(query, () => {
  globalThis.clearTimeout(timer)
  generation++
  searching.value = false
  reset()
  if (!tooShort()) {
    timer = globalThis.setTimeout(search, SEARCH_DELAY_MS)
  }
})

async function search(): Promise<void> {
  const mine = ++generation
  searching.value = true
  try {
    const found = await directory.search(query.value)
    if (mine === generation) {
      results.value = found
      searched.value = true
    }
  } catch {
    if (mine === generation) {
      failed.value = true
    }
  } finally {
    if (mine === generation) {
      searching.value = false
    }
  }
}

function pick(user: DirectoryUser): void {
  emit('pick', user)
}

onBeforeUnmount(() => globalThis.clearTimeout(timer))
</script>

<template>
  <div class="ext:flex ext:flex-col ext:gap-2" data-testid="user-picker">
    <oc-text-input
      v-model="query"
      :label="$gettext('Add a person')"
      :description-message="
        $gettext('Type at least %{count} characters of a name or email address.', {
          count: String(minLength)
        })
      "
      autocomplete="off"
      data-testid="user-search"
    />
    <p v-if="searching" class="ext:text-sm" data-testid="searching">
      {{ $gettext('Searching…') }}
    </p>
    <NoticeBanner
      v-else-if="failed"
      tone="danger"
      :message="$gettext('Could not search for people. Try again in a moment.')"
      data-testid="search-failed"
    />
    <p v-else-if="searched && results.length === 0" class="ext:text-sm" data-testid="no-matches">
      {{ $gettext('Nobody matches.') }}
    </p>
    <ul v-if="results.length > 0" class="ext:flex ext:flex-col ext:gap-2" data-testid="matches">
      <li v-for="user in results" :key="user.id" class="ext:flex ext:items-center ext:gap-3">
        <oc-avatar :user-name="user.displayName" :width="32" />
        <span class="ext:flex-1 ext:min-w-0">
          {{ user.displayName }}
          <span v-if="user.mail" class="ext:text-role-on-surface-variant">· {{ user.mail }}</span>
        </span>
        <span v-if="props.excludeIds.includes(user.id)" class="ext:text-sm" data-testid="already">
          {{ $gettext('Already listed') }}
        </span>
        <oc-button v-else appearance="outline" data-testid="add-user" @click="pick(user)">
          {{ $gettext('Add') }}
        </oc-button>
      </li>
    </ul>
  </div>
</template>
