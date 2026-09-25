<script setup lang="ts">
// Replace the Recovery Key (flow item 6; decisions.md #18; 8d.4 decisions).
//
// All decisions live in recoverykey/rotation.ts; this file renders its state
// and forwards clicks. The current key is typed into this component's input
// and handed to the machine; the new key arrives in the machine's state to be
// shown once. Neither is ever in a store, a route, the URL or browser storage
// (lint bans storage in this file), and both are gone when the page is left.
import { computed, onBeforeUnmount, onMounted, ref, shallowRef, watch } from 'vue'
import ActionError from '../components/ActionError.vue'
import LostKeyNotice from '../components/LostKeyNotice.vue'
import RecoveryKeyDisplay from '../components/RecoveryKeyDisplay.vue'
import RecoveryKeyGate from '../components/RecoveryKeyGate.vue'
import RequestState from '../components/RequestState.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { performRecoveryKeyRotation, recoveryKeyOpens } from '../crypto'
import { cryptoRandomInt, nextFrame } from '../recoverykey/gate'
import { RecoveryKeyRotation, type RotationState } from '../recoverykey/rotation'

const props = defineProps<{ spaceId: string }>()

const state = shallowRef<RotationState>({ step: 'loading' })
const rotation = new RecoveryKeyRotation(props.spaceId, {
  api: useBackupApi(),
  rotate: (options) => performRecoveryKeyRotation(options),
  keyOpens: recoveryKeyOpens,
  randomInt: cryptoRandomInt,
  yieldToRender: nextFrame,
  onChange: (next) => {
    state.value = next
  }
})

/**
 * currentKey is the typed current key. It stays while it may need correcting
 * (a typo, a wrong key) and is cleared once it has opened the envelope: from
 * then on the machine holds it, and only until the rotation is settled.
 */
const currentKey = ref('')

watch(
  () => state.value.step,
  (step) => {
    if (step !== 'enter_current' && step !== 'working') {
      currentKey.value = ''
    }
  }
)

const loadError = computed(() =>
  state.value.step === 'load_failed' ? state.value.error : undefined
)

onMounted(() => rotation.start())
onBeforeUnmount(() => {
  rotation.dispose()
  currentKey.value = ''
})
</script>

<template>
  <main class="ext:p-4 ext:flex ext:flex-col ext:gap-4 ext:max-w-2xl">
    <router-link
      :to="{ name: 'backup-vault-recovery-key', params: { spaceId } }"
      class="ext:text-sm"
      data-testid="back"
    >
      {{ $gettext('Back to the Recovery Key') }}
    </router-link>

    <h1 class="ext:text-xl ext:font-semibold">
      <template v-if="rotation.spaceName">
        {{ $gettext('Replace the Recovery Key for %{space}', { space: rotation.spaceName }) }}
      </template>
      <template v-else>{{ $gettext('Replace the Recovery Key') }}</template>
    </h1>

    <RequestState :loading="state.step === 'loading'" :error="loadError" @retry="rotation.start()">
      <section v-if="state.step === 'not_set_up'" data-step="not_set_up">
        <p>
          {{ $gettext('This space has no Recovery Key yet. It is created when backup is set up.') }}
        </p>
      </section>

      <section v-else-if="state.step === 'needs_manager'" data-step="needs_manager">
        <p>
          {{
            $gettext(
              'Only a manager of this space can replace its Recovery Key, because every member who kept the old one would need the new one.'
            )
          }}
        </p>
      </section>

      <!-- The current key -->
      <form
        v-else-if="state.step === 'enter_current'"
        data-step="enter_current"
        class="ext:flex ext:flex-col ext:gap-2"
        @submit.prevent="rotation.useCurrentKey(currentKey)"
      >
        <p v-if="state.discardNew" role="alert" data-testid="discard-new">
          {{
            $gettext(
              'The new Recovery Key shown before was not taken into use. Throw away any copy of it. The current Recovery Key still works.'
            )
          }}
        </p>
        <p>
          {{
            $gettext(
              'A new Recovery Key replaces the current one. Existing backups stay readable, and nothing is uploaded again.'
            )
          }}
        </p>
        <p>{{ $gettext('To make the new key, enter the current Recovery Key.') }}</p>
        <oc-text-input
          v-model="currentKey"
          :label="$gettext('Current Recovery Key')"
          autocomplete="off"
          spellcheck="false"
          data-testid="current-key"
        />
        <p v-if="state.failure?.kind === 'malformed'" role="alert" data-failure="malformed">
          {{
            $gettext(
              'This is not a Recovery Key. Check it for typing mistakes: a Recovery Key has seven groups of letters and numbers.'
            )
          }}
        </p>
        <p v-else-if="state.failure?.kind === 'wrong'" role="alert" data-failure="wrong">
          {{ $gettext('This Recovery Key does not open this space’s backups.') }}
        </p>
        <p v-else-if="state.failure?.kind === 'ceremony'" role="alert" data-failure="ceremony">
          {{
            $gettext(
              'Creating the new Recovery Key did not work. Nothing was changed. Please try again.'
            )
          }}
        </p>
        <p v-else-if="state.failure?.kind === 'envelope'" role="alert" data-failure="envelope">
          {{
            $gettext(
              'The stored key file could not be read, so the key could not be tried. Nothing was changed.'
            )
          }}
        </p>
        <ActionError v-else-if="state.failure?.kind === 'request'" :error="state.failure.error" />
        <p class="ext:text-sm ext:text-role-on-surface-variant">
          {{ $gettext('This takes a few seconds, and the page may not respond while it does.') }}
        </p>
        <div>
          <oc-button submit="submit" appearance="filled" data-testid="current-submit">
            {{ $gettext('Continue') }}
          </oc-button>
        </div>
        <LostKeyNotice class="ext:mt-2" />
      </form>

      <section
        v-else-if="state.step === 'working'"
        data-step="working"
        aria-live="polite"
        class="ext:flex ext:items-center ext:gap-2"
      >
        <oc-spinner />
        {{ $gettext('Creating your new Recovery Key. This takes a moment…') }}
      </section>

      <!-- The new key, once -->
      <section v-else-if="state.step === 'show_key'" data-step="show_key">
        <h2 class="ext:text-lg ext:font-semibold">
          {{ $gettext('Save your new Recovery Key now') }}
        </h2>
        <p>
          {{
            $gettext(
              'This is the only time it is shown. Save it in your password manager, or write it down and keep it somewhere safe.'
            )
          }}
        </p>
        <p>{{ $gettext('Nothing has changed yet. The current Recovery Key still works.') }}</p>
        <RecoveryKeyDisplay :recovery-key="state.recoveryKey">
          <oc-button appearance="filled" data-testid="key-saved" @click="rotation.keySaved()">
            {{ $gettext('I have saved it') }}
          </oc-button>
        </RecoveryKeyDisplay>
      </section>

      <RecoveryKeyGate
        v-else-if="state.step === 'confirm'"
        data-step="confirm"
        :groups="state.groups"
        :mismatch="state.mismatch"
        :submitting="state.submitting"
        :submit-label="$gettext('Replace the Recovery Key')"
        @submit="(answers) => rotation.confirmKey(answers)"
        @show-again="rotation.showKeyAgain()"
      />

      <!-- The POST may or may not have landed -->
      <section v-else-if="state.step === 'uncertain'" data-step="uncertain">
        <p class="ext:font-medium">
          {{ $gettext('It is not clear whether the Recovery Key was replaced.') }}
        </p>
        <ActionError :error="state.error" />
        <p>
          {{
            $gettext(
              'Keep both the current and the new Recovery Key for now. Checking again finds out which one this space uses.'
            )
          }}
        </p>
        <oc-button
          class="ext:mt-3"
          appearance="filled"
          :disabled="state.checking"
          :show-spinner="state.checking"
          data-testid="check-again"
          @click="rotation.checkAgain()"
        >
          {{ $gettext('Check again') }}
        </oc-button>
      </section>

      <!-- Terminal: another manager got there first -->
      <section v-else-if="state.step === 'replaced_elsewhere'" data-step="replaced_elsewhere">
        <h2 class="ext:text-lg ext:font-semibold">
          {{ $gettext('Someone else replaced the Recovery Key') }}
        </h2>
        <p>
          {{
            $gettext(
              'The Recovery Key of this space was replaced by someone else while you were doing the same. The key shown to you here was not taken into use: throw away any copy of it.'
            )
          }}
        </p>
        <p>{{ $gettext('Ask the other managers of this space for the new Recovery Key.') }}</p>
      </section>

      <!-- Done -->
      <section
        v-else-if="state.step === 'done'"
        data-step="done"
        class="ext:flex ext:flex-col ext:gap-2"
      >
        <h2 class="ext:text-lg ext:font-semibold">
          {{ $gettext('The Recovery Key is replaced') }}
        </h2>
        <p>
          {{
            $gettext(
              'Existing backups stay readable with the new key, and nothing was uploaded again.'
            )
          }}
        </p>
        <p data-testid="old-key">
          {{
            $gettext(
              'The old Recovery Key stops working once the next backup has run. After that, destroy every copy of it.'
            )
          }}
        </p>
        <p>
          {{
            $gettext(
              'Other members of this space who kept the old Recovery Key need the new one. If you downloaded the key file before, download it again.'
            )
          }}
        </p>
        <div v-if="state.mayRunBackup">
          <oc-button
            appearance="filled"
            :disabled="state.backup.kind === 'starting' || state.backup.kind === 'started'"
            :show-spinner="state.backup.kind === 'starting'"
            data-testid="back-up-now"
            @click="rotation.backUpNow()"
          >
            {{ $gettext('Back up now') }}
          </oc-button>
          <p
            v-if="state.backup.kind === 'started'"
            role="status"
            class="ext:mt-2"
            data-testid="backup-started"
          >
            {{
              $gettext(
                'A backup is running. When it has finished, the old Recovery Key stops working.'
              )
            }}
          </p>
          <ActionError
            v-else-if="state.backup.kind === 'failed'"
            class="ext:mt-2"
            :error="state.backup.error"
          />
        </div>
        <router-link :to="{ name: 'backup-vault-space', params: { spaceId } }">
          {{ $gettext('Back to the space') }}
        </router-link>
      </section>
    </RequestState>
  </main>
</template>
