<script setup lang="ts">
// A Space's Recovery Key page: "Check my Recovery Key", the key file download,
// and what losing the key means (8d decision 7; 8d.4 decisions 3, 4, 6, 7).
//
// Every member may use it (decisions.md #7). All decisions live in
// recoverykey/check.ts. The key typed here is held in this component's input
// only, is handed to the machine for one check and not kept there, and is
// cleared when the page is left. Lint bans browser storage in this file.
import { computed, onBeforeUnmount, onMounted, ref, shallowRef } from 'vue'
import { useGettext } from 'vue3-gettext'
import ActionError from '../components/ActionError.vue'
import LostKeyNotice from '../components/LostKeyNotice.vue'
import NoticeBanner from '../components/NoticeBanner.vue'
import PageLayout from '../components/PageLayout.vue'
import { recoveryKeyCrumb, spaceCrumbs } from '../layout/breadcrumbs'
import RequestState from '../components/RequestState.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { recoveryKeyOpens } from '../crypto'
import { RecoveryKeyCheck, type CheckState } from '../recoverykey/check'
import { saveBytes } from '../recoverykey/download'
import { nextFrame } from '../recoverykey/gate'

const props = defineProps<{ spaceId: string }>()

const { $gettext } = useGettext()
const state = shallowRef<CheckState>({ step: 'loading' })
/**
 * crumbs is the page's breadcrumb trail. It names the Space once page has
 * read it, which happens with a state change: reading `state` is what makes
 * this follow, since page.spaceName itself is not reactive.
 */
const crumbs = computed(() => {
  void state.value
  return spaceCrumbs(
    $gettext,
    props.spaceId,
    page.spaceName,
    recoveryKeyCrumb($gettext, props.spaceId, true)
  )
})
const page = new RecoveryKeyCheck(props.spaceId, {
  api: useBackupApi(),
  keyOpens: recoveryKeyOpens,
  saveFile: saveBytes,
  yieldToRender: nextFrame,
  onChange: (next) => {
    state.value = next
  }
})

const input = ref('')

const loadError = computed(() =>
  state.value.step === 'load_failed' ? state.value.error : undefined
)
const result = computed(() => (state.value.step === 'ready' ? state.value.result : undefined))

function onInput(value: string): void {
  input.value = value
  page.clearResult()
}

onMounted(() => page.start())
onBeforeUnmount(() => {
  page.dispose()
  input.value = ''
})
</script>

<template>
  <PageLayout :crumbs="crumbs" narrow>
    <RequestState :loading="state.step === 'loading'" :error="loadError" @retry="page.start()">
      <NoticeBanner
        v-if="state.step === 'not_set_up'"
        tone="info"
        :message="
          $gettext('This space has no Recovery Key yet. It is created when backup is set up.')
        "
        data-step="not_set_up"
      />

      <template v-else-if="state.step === 'ready'">
        <!-- Check my Recovery Key -->
        <form
          class="ext:flex ext:flex-col ext:gap-3"
          data-testid="check-form"
          @submit.prevent="page.check(input)"
        >
          <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Check my Recovery Key') }}</h2>
          <p>
            {{
              $gettext(
                'Make sure the Recovery Key you saved still opens this space’s backups. It is checked on this device only and is not sent anywhere.'
              )
            }}
          </p>
          <oc-text-input
            :model-value="input"
            :label="$gettext('Recovery Key')"
            :disabled="state.checking"
            autocomplete="off"
            spellcheck="false"
            data-testid="check-input"
            @update:model-value="onInput"
          />
          <div>
            <oc-button
              submit="submit"
              appearance="filled"
              :disabled="state.checking"
              :show-spinner="state.checking"
              data-testid="check-submit"
            >
              {{ $gettext('Check') }}
            </oc-button>
          </div>
          <p v-if="state.checking" aria-live="polite" data-testid="checking">
            {{ $gettext('Checking. This takes a moment…') }}
          </p>

          <NoticeBanner
            v-if="result?.kind === 'opens'"
            tone="success"
            :message="$gettext('This Recovery Key opens this space’s backups. Keep it safe.')"
            data-result="opens"
          />
          <NoticeBanner
            v-else-if="result?.kind === 'malformed'"
            tone="danger"
            :message="
              $gettext(
                'This is not a Recovery Key. Check it for typing mistakes: a Recovery Key has seven groups of letters and numbers.'
              )
            "
            data-result="malformed"
          />
          <NoticeBanner
            v-else-if="result?.kind === 'wrong'"
            tone="danger"
            :title="$gettext('This Recovery Key does not open this space’s backups.')"
            :message="
              $gettext('Check that it is the key for this space and that it was copied in full.')
            "
            data-result="wrong"
          >
            <LostKeyNotice class="ext:mt-2" />
          </NoticeBanner>
          <div
            v-else-if="result?.kind === 'cannot_tell'"
            class="ext:flex ext:flex-col ext:gap-2"
            data-result="cannot_tell"
          >
            <NoticeBanner
              tone="warning"
              :message="
                $gettext(
                  'The key could not be checked. This says nothing about whether it is right.'
                )
              "
            />
            <ActionError :error="result.error" />
          </div>
        </form>

        <LostKeyNotice v-if="result?.kind !== 'wrong'" />

        <!-- The key file -->
        <section class="ext:flex ext:flex-col ext:gap-3" data-testid="download">
          <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Key file') }}</h2>
          <p>
            {{
              $gettext(
                'If this server is ever lost, your administrator can give you a copy of your backups, and the decrypt program restores it with your Recovery Key. Keep this key file with your Recovery Key: it is locked, and useless without it.'
              )
            }}
          </p>
          <div>
            <oc-button
              appearance="outline"
              :disabled="state.download.kind === 'working'"
              :show-spinner="state.download.kind === 'working'"
              data-testid="download-envelope"
              @click="page.download()"
            >
              {{ $gettext('Download recovery.ocbke') }}
            </oc-button>
          </div>
          <NoticeBanner
            v-if="state.download.kind === 'done'"
            tone="success"
            :message="$gettext('Downloaded. Download it again after the Recovery Key is replaced.')"
            data-testid="downloaded"
          />
          <ActionError v-else-if="state.download.kind === 'failed'" :error="state.download.error" />
        </section>

        <!-- Managers only: the server refuses anyone else. -->
        <section v-if="state.mayReplace" data-testid="replace">
          <oc-button
            type="router-link"
            :to="{ name: 'backup-vault-recovery-key-replace', params: { spaceId } }"
            appearance="outline"
          >
            {{ $gettext('Replace the Recovery Key') }}
          </oc-button>
        </section>
      </template>
    </RequestState>
  </PageLayout>
</template>
