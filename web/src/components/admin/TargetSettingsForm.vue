<script setup lang="ts">
// Where a target's backups go, and the keys to get there.
//
// Without a `target` this creates one and the keys are required. With one it
// edits it, and the keys sit behind "Replace keys" (8e decision 5): closed, the
// save sends no keys and the stored ones stay; open, the save replaces both
// pairs at once. The stored keys are sealed on the server and the admin path
// cannot open them, which is why "change only the maintenance pair" is not a
// thing this form can offer.
//
// Key fields start empty and are emptied again after every save and when the
// section is closed. Nothing here is ever filled from the server.
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import { useGettext } from 'vue3-gettext'
import { asApiError, type AdminTarget, type ApiError } from '../../api'
import {
  buildTargetRequest,
  emptyCredentials,
  emptySettings,
  hasProblems,
  pairState,
  settingsFromTarget,
  validateTarget,
  type CredentialsInput,
  type TargetField
} from '../../admin/targetform'
import { adminErrorAdvice, adminErrorTitle } from '../../admin/wording'
import { useAdminApi } from '../../composables/useAdminApi'
import ActionError from '../ActionError.vue'
import NoticeBanner from '../NoticeBanner.vue'
import ConnectionCheck from './ConnectionCheck.vue'
import KeyPairFields from './KeyPairFields.vue'

const props = defineProps<{ target?: AdminTarget }>()

const emit = defineEmits<{ saved: [target: AdminTarget] }>()

const { $gettext } = useGettext()
const api = useAdminApi()

const creating = props.target === undefined
const settings = reactive(props.target ? settingsFromTarget(props.target) : emptySettings())
const credentials = reactive<CredentialsInput>(emptyCredentials())
const replacingKeys = ref(creating)
const showProblems = ref(false)
const saving = ref(false)
const saveError = ref<ApiError | undefined>(undefined)
const savedNotice = ref(false)

/** keysToSend is the key pairs the next request carries, or undefined for "keep". */
const keysToSend = computed(() => (replacingKeys.value ? credentials : undefined))

const problems = computed(() => validateTarget(settings, keysToSend.value))

/** checkRequest is the body a connection check would send, when there is one. */
const checkRequest = computed(() =>
  replacingKeys.value && !hasProblems(problems.value)
    ? buildTargetRequest(settings, credentials)
    : undefined
)

const backupPairTyped = computed(() => pairState(credentials.backup) === 'complete')

function problemFor(field: TargetField): string {
  if (!showProblems.value || problems.value[field] === undefined) {
    return ''
  }
  return $gettext('Required')
}

function forgetKeys(): void {
  Object.assign(credentials, emptyCredentials())
}

function openKeys(): void {
  replacingKeys.value = true
  savedNotice.value = false
}

function keepStoredKeys(): void {
  forgetKeys()
  replacingKeys.value = false
}

async function save(): Promise<void> {
  savedNotice.value = false
  if (hasProblems(problems.value)) {
    showProblems.value = true
    return
  }
  saving.value = true
  saveError.value = undefined
  const body = buildTargetRequest(settings, keysToSend.value)
  try {
    const stored = props.target
      ? await api.updateTarget(props.target.id, body)
      : await api.createTarget(body)
    forgetKeys()
    showProblems.value = false
    if (!creating) {
      replacingKeys.value = false
      Object.assign(settings, settingsFromTarget(stored))
      savedNotice.value = true
    }
    emit('saved', stored)
  } catch (err: unknown) {
    saveError.value = asApiError(err)
  } finally {
    saving.value = false
  }
}

onBeforeUnmount(forgetKeys)
</script>

<template>
  <form class="ext:flex ext:flex-col ext:gap-8" novalidate @submit.prevent="save">
    <section class="ext:flex ext:flex-col ext:gap-3" data-testid="connection">
      <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Connection') }}</h2>
      <oc-text-input
        v-model="settings.name"
        :label="$gettext('Name')"
        :description-message="$gettext('People see this name when they choose where to back up.')"
        :error-message="problemFor('name')"
        :disabled="saving"
        data-testid="name"
      />
      <oc-text-input
        v-model="settings.endpoint"
        :label="$gettext('Endpoint')"
        :description-message="
          $gettext('Address of the S3 service, e.g. s3.example.org or garage:3900')
        "
        :error-message="problemFor('endpoint')"
        :disabled="saving"
        data-testid="endpoint"
      />
      <oc-text-input
        v-model="settings.bucket"
        :label="$gettext('Bucket')"
        :error-message="problemFor('bucket')"
        :disabled="saving"
        data-testid="bucket"
      />
      <oc-text-input
        v-model="settings.region"
        :label="$gettext('Region (optional)')"
        :disabled="saving"
        data-testid="region"
      />
      <oc-text-input
        v-model="settings.prefix"
        :label="$gettext('Prefix (optional)')"
        :description-message="$gettext('A folder inside the bucket to keep the backups in.')"
        :disabled="saving"
        data-testid="prefix"
      />
      <oc-checkbox
        v-model="settings.usePathStyle"
        :label="$gettext('Use path-style addresses (needed by most self-hosted S3 services)')"
        :disabled="saving"
        data-testid="path-style"
      />
      <oc-checkbox
        v-model="settings.disableTls"
        :label="$gettext('Connect without TLS (only inside a trusted network)')"
        :disabled="saving"
        data-testid="disable-tls"
      />
    </section>

    <section class="ext:flex ext:flex-col ext:gap-3" data-testid="keys">
      <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Access keys') }}</h2>

      <template v-if="!replacingKeys">
        <p class="ext:text-sm ext:text-role-on-surface-variant" data-testid="keys-stored">
          {{
            $gettext(
              'The stored keys are kept. They are never shown: to use other keys, replace them.'
            )
          }}
        </p>
        <div>
          <oc-button appearance="outline" data-testid="replace-keys" @click="openKeys">
            {{ $gettext('Replace keys') }}
          </oc-button>
        </div>
      </template>

      <template v-else>
        <NoticeBanner
          v-if="target?.maintenance_configured"
          tone="warning"
          role="note"
          :message="
            $gettext(
              'Saving replaces both key pairs. If you leave the maintenance keys empty, the stored maintenance keys are removed and the backup keys are used for both.'
            )
          "
          data-testid="replace-both-warning"
        />
        <KeyPairFields
          v-model="credentials.backup"
          :legend="$gettext('Backup keys')"
          :description="$gettext('Used to write new backups and to restore them.')"
          :id-error="problemFor('backupAccessKeyId')"
          :secret-error="problemFor('backupSecretAccessKey')"
          :disabled="saving"
          data-testid="backup-keys"
        />
        <KeyPairFields
          v-model="credentials.maintenance"
          :legend="$gettext('Maintenance keys (optional)')"
          :description="
            $gettext(
              'A separate pair used only to remove old backups. Leave empty to use the backup keys for that too.'
            )
          "
          :id-error="problemFor('maintenanceAccessKeyId')"
          :secret-error="problemFor('maintenanceSecretAccessKey')"
          :disabled="saving"
          data-testid="maintenance-keys"
        />
        <ConnectionCheck
          :request="checkRequest"
          :ready="backupPairTyped"
          @invalid="showProblems = true"
        />
        <div v-if="!creating">
          <oc-button
            appearance="raw"
            :disabled="saving"
            data-testid="keep-keys"
            @click="keepStoredKeys"
          >
            {{ $gettext('Keep the stored keys') }}
          </oc-button>
        </div>
      </template>
    </section>

    <div class="ext:flex ext:flex-col ext:items-start ext:gap-3">
      <ActionError
        v-if="saveError"
        class="ext:self-stretch"
        :error="saveError"
        :title="adminErrorTitle"
        :advice="adminErrorAdvice"
      />
      <NoticeBanner
        v-if="savedNotice"
        class="ext:self-stretch"
        tone="success"
        :message="$gettext('Saved.')"
        data-testid="saved"
      />

      <oc-button
        submit="submit"
        appearance="filled"
        :disabled="saving"
        :show-spinner="saving"
        data-testid="save"
      >
        {{ creating ? $gettext('Create backup destination') : $gettext('Save') }}
      </oc-button>
    </div>
  </form>
</template>
