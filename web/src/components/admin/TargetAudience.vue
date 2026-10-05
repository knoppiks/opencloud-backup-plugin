<script setup lang="ts">
// Who can back up to a target: everyone, or only the people listed.
//
// Saving replaces the whole grant list, so what this sends has to be the
// complete audience. Space grants cannot be edited here yet (8e decision 3);
// they are listed read-only and sent back as they were read, every time.
//
// People are named by asking the user directory for each granted id (8e
// decision 8). A name that cannot be found shows the id instead, and the
// person can still be removed: a deleted account must not leave a grant
// nobody can take away.
import { computed, onMounted, reactive, ref, shallowRef } from 'vue'
import { useGettext } from 'vue3-gettext'
import { asApiError, type ApiError } from '../../api'
import {
  audienceFromGrants,
  grantsFromAudience,
  hasRedundantPeople,
  reachesNobody,
  sameGrants,
  withMode,
  withUser,
  withoutUser,
  type Audience,
  type AudienceMode
} from '../../admin/audience'
import type { DirectoryUser } from '../../admin/directory'
import { adminErrorAdvice, adminErrorTitle } from '../../admin/wording'
import { useAdminApi } from '../../composables/useAdminApi'
import { useUserDirectory } from '../../composables/useUserDirectory'
import ActionError from '../ActionError.vue'
import NoticeBanner from '../NoticeBanner.vue'
import RequestState from '../RequestState.vue'
import UserPicker from './UserPicker.vue'

const props = defineProps<{ targetId: string }>()

const { $gettext } = useGettext()
const api = useAdminApi()
const directory = useUserDirectory()

const loading = ref(true)
const loadError = ref<ApiError | undefined>(undefined)
const stored = shallowRef<Audience>(audienceFromGrants([]))
const audience = shallowRef<Audience>(audienceFromGrants([]))
const saving = ref(false)
const saveError = ref<ApiError | undefined>(undefined)
const savedNotice = ref(false)

/** names maps a user id to its directory entry; null means it could not be named. */
const names = reactive<Record<string, DirectoryUser | null>>({})

const dirty = computed(() => !sameGrants(audience.value, stored.value))

function setAudience(next: Audience): void {
  audience.value = next
  savedNotice.value = false
}

function nameOf(id: string): string {
  const entry = names[id]
  if (entry === undefined) {
    return '…'
  }
  return entry === null ? $gettext('Unknown user') : entry.displayName
}

async function nameAll(ids: string[]): Promise<void> {
  await Promise.all(
    ids
      .filter((id) => names[id] === undefined)
      .map(async (id) => {
        names[id] = (await directory.lookup(id)) ?? null
      })
  )
}

async function load(): Promise<void> {
  loading.value = true
  loadError.value = undefined
  try {
    const read = audienceFromGrants(await api.grants(props.targetId))
    stored.value = read
    audience.value = read
  } catch (err: unknown) {
    loadError.value = asApiError(err)
    return
  } finally {
    loading.value = false
  }
  await nameAll(audience.value.userIds)
}

function choose(mode: AudienceMode): void {
  setAudience(withMode(audience.value, mode))
}

function add(user: DirectoryUser): void {
  names[user.id] = user
  setAudience(withUser(audience.value, user.id))
}

function remove(id: string): void {
  setAudience(withoutUser(audience.value, id))
}

async function save(): Promise<void> {
  saving.value = true
  saveError.value = undefined
  try {
    const written = audienceFromGrants(
      await api.replaceGrants(props.targetId, grantsFromAudience(audience.value))
    )
    stored.value = written
    audience.value = written
    savedNotice.value = true
    await nameAll(written.userIds)
  } catch (err: unknown) {
    saveError.value = asApiError(err)
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>

<template>
  <section class="ext:flex ext:flex-col ext:gap-3" data-testid="audience">
    <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Who can back up here') }}</h2>

    <RequestState
      :loading="loading"
      :error="loadError"
      :title="adminErrorTitle"
      :advice="adminErrorAdvice"
      @retry="load"
    >
      <fieldset class="ext:flex ext:flex-col ext:gap-2">
        <legend class="ext:sr-only">{{ $gettext('Who can back up here') }}</legend>
        <oc-radio
          :model-value="audience.mode"
          option="everyone"
          :label="$gettext('Everyone')"
          :disabled="saving"
          data-testid="mode-everyone"
          @update:model-value="choose('everyone')"
        />
        <oc-radio
          :model-value="audience.mode"
          option="people"
          :label="$gettext('Only these people')"
          :disabled="saving"
          data-testid="mode-people"
          @update:model-value="choose('people')"
        />
      </fieldset>

      <NoticeBanner
        v-if="hasRedundantPeople(audience)"
        tone="info"
        role="note"
        :message="
          $gettext(
            'Some people are also listed by name. Everyone can back up here anyway, so saving removes that list.'
          )
        "
        data-testid="redundant-people"
      />

      <template v-if="audience.mode === 'people'">
        <ul
          v-if="audience.userIds.length > 0"
          class="ext:flex ext:flex-col ext:gap-2"
          data-testid="people"
        >
          <li
            v-for="id in audience.userIds"
            :key="id"
            class="ext:flex ext:items-center ext:gap-3"
            :data-user-id="id"
          >
            <oc-avatar :user-name="names[id]?.displayName ?? id" :width="32" />
            <span class="ext:flex-1 ext:min-w-0">
              {{ nameOf(id) }}
              <span v-if="names[id]?.mail" class="ext:text-role-on-surface-variant"
                >· {{ names[id]?.mail }}</span
              >
              <span
                v-if="names[id] === null"
                class="ext:text-sm ext:text-role-on-surface-variant"
                data-testid="unknown-id"
                >({{ id }})</span
              >
            </span>
            <oc-button
              appearance="raw"
              :disabled="saving"
              data-testid="remove-user"
              @click="remove(id)"
            >
              {{ $gettext('Remove') }}
            </oc-button>
          </li>
        </ul>
        <UserPicker :exclude-ids="audience.userIds" @pick="add" />
      </template>

      <div v-if="audience.kept.length > 0" data-testid="kept-grants">
        <p class="ext:text-sm">
          {{
            $gettext(
              'Also granted to these spaces. This cannot be changed here yet, and saving keeps it:'
            )
          }}
        </p>
        <ul class="ext:text-sm ext:text-role-on-surface-variant">
          <li v-for="(grant, index) in audience.kept" :key="index">
            {{ grant.space_id ?? grant.scope }}
          </li>
        </ul>
      </div>

      <NoticeBanner
        v-if="reachesNobody(audience)"
        tone="warning"
        role="note"
        :message="$gettext('Nobody can back up here yet.')"
        data-testid="nobody"
      />

      <ActionError
        v-if="saveError"
        :error="saveError"
        :title="adminErrorTitle"
        :advice="adminErrorAdvice"
      />
      <NoticeBanner
        v-if="savedNotice"
        tone="success"
        :message="$gettext('Saved.')"
        data-testid="audience-saved"
      />

      <div>
        <oc-button
          appearance="filled"
          :disabled="!dirty || saving"
          :show-spinner="saving"
          data-testid="save-audience"
          @click="save"
        >
          {{ $gettext('Save who can back up here') }}
        </oc-button>
      </div>
    </RequestState>
  </section>
</template>
