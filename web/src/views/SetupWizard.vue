<script setup lang="ts">
// "Yes, back up my data": the setup wizard for one Space.
//
// All decisions live in wizard/machine.ts; this file renders its state and
// forwards clicks. The machine is created per mounted wizard and disposed on
// unmount, which is what keeps the Recovery Key in component state only: it is
// never in a store, a route, the URL or browser storage (8d decision 3; lint
// bans storage here), and it is gone when the page is left.
import { computed, onBeforeUnmount, onMounted, shallowRef } from 'vue'
import { useGettext } from 'vue3-gettext'
import ActionError from '../components/ActionError.vue'
import RecoveryKeyDisplay from '../components/RecoveryKeyDisplay.vue'
import RecoveryKeyGate from '../components/RecoveryKeyGate.vue'
import RequestState from '../components/RequestState.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { performSetupCeremony, recoveryKeyOpens } from '../crypto'
import {
  cryptoRandomInt,
  nextFrame,
  SetupWizard,
  type ScheduleChoice,
  type WizardState
} from '../wizard/machine'

const props = defineProps<{ spaceId: string }>()

const gettext = useGettext()
const { $gettext } = gettext
const format = useFormat()

const state = shallowRef<WizardState>({ step: 'loading' })
const wizard = new SetupWizard(props.spaceId, {
  api: useBackupApi(),
  ceremony: () => performSetupCeremony(),
  keyOpens: recoveryKeyOpens,
  randomInt: cryptoRandomInt,
  yieldToRender: nextFrame,
  onChange: (next) => {
    state.value = next
  }
})

const loadError = computed(() =>
  state.value.step === 'load_failed' ? state.value.error : undefined
)

function onTimeChange(choice: ScheduleChoice, value: string): void {
  const [hour, minute] = value.split(':').map(Number)
  if (Number.isInteger(hour) && Number.isInteger(minute)) {
    wizard.chooseSchedule({ ...choice, hour: hour as number, minute: minute as number })
  }
}

function timeOf(choice: ScheduleChoice): string {
  return `${String(choice.hour).padStart(2, '0')}:${String(choice.minute).padStart(2, '0')}`
}

/** weekdays are named by the browser in the user's language; 0 is Sunday. */
const weekdays = computed(() => {
  const name = new Intl.DateTimeFormat(gettext.current || 'en', {
    weekday: 'long',
    timeZone: 'UTC'
  })
  // 2026-09-20 is a Sunday.
  return Array.from({ length: 7 }, (_, day) => name.format(new Date(Date.UTC(2026, 8, 20 + day))))
})

onMounted(() => wizard.start())
onBeforeUnmount(() => wizard.dispose())
</script>

<template>
  <main class="ext:p-4 ext:flex ext:flex-col ext:gap-4 ext:max-w-2xl">
    <router-link
      :to="{ name: 'backup-vault-space', params: { spaceId } }"
      class="ext:text-sm"
      data-testid="back"
    >
      {{ $gettext('Back to the space') }}
    </router-link>

    <h1 class="ext:text-xl ext:font-semibold">
      <template v-if="wizard.spaceName">
        {{ $gettext('Set up backup for %{space}', { space: wizard.spaceName }) }}
      </template>
      <template v-else>{{ $gettext('Set up backup') }}</template>
    </h1>

    <RequestState :loading="state.step === 'loading'" :error="loadError" @retry="wizard.start()">
      <!-- Viewer -->
      <section v-if="state.step === 'not_allowed'" data-step="not_allowed">
        <p>{{ $gettext('Only editors and managers of this space can set up backup.') }}</p>
      </section>

      <!-- No destination granted -->
      <section v-else-if="state.step === 'no_targets'" data-step="no_targets">
        <p>
          {{
            $gettext(
              'No backup destination has been shared with you yet. Ask your administrator to grant you one.'
            )
          }}
        </p>
      </section>

      <!-- Step 1: destination -->
      <section v-else-if="state.step === 'pick_target'" data-step="pick_target">
        <template v-if="state.targets.length > 1">
          <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Where should backups go?') }}</h2>
          <fieldset class="ext:mt-2 ext:flex ext:flex-col ext:gap-1" data-testid="target-picker">
            <legend class="ext:sr-only">{{ $gettext('Backup destination') }}</legend>
            <label v-for="target in state.targets" :key="target.id" class="ext:flex ext:gap-2">
              <input
                type="radio"
                name="target"
                :value="target.id"
                :checked="state.selected === target.id"
                :disabled="state.saving"
                @change="wizard.selectTarget(target.id)"
              />
              {{ target.name }}
            </label>
          </fieldset>
        </template>
        <p v-else-if="state.saving" class="ext:flex ext:items-center ext:gap-2">
          <oc-spinner />
          {{ $gettext('Saving…') }}
        </p>
        <ActionError v-if="state.error" class="ext:mt-2" :error="state.error" />
        <oc-button
          v-if="state.targets.length > 1 || state.error"
          class="ext:mt-3"
          appearance="filled"
          :disabled="state.saving || state.selected === undefined"
          :show-spinner="state.saving"
          data-testid="target-continue"
          @click="wizard.confirmTarget()"
        >
          {{ state.error ? $gettext('Try again') : $gettext('Continue') }}
        </oc-button>
      </section>

      <!-- Step 2, editor: a manager has to continue -->
      <section v-else-if="state.step === 'needs_manager'" data-step="needs_manager">
        <p class="ext:font-medium">
          {{ $gettext('A manager of this space has to finish setup.') }}
        </p>
        <p>
          {{
            $gettext(
              'The backup destination is chosen. Creating the Recovery Key needs a manager of this space.'
            )
          }}
        </p>
      </section>

      <!-- Step 2: Recovery Key, introduction -->
      <section v-else-if="state.step === 'key_intro'" data-step="key_intro">
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Your Recovery Key') }}</h2>
        <p v-if="state.discardPrevious" role="alert" data-testid="discard-previous">
          {{
            $gettext(
              'The Recovery Key shown before was not taken into use. Throw away any copy of it; a new one will be created.'
            )
          }}
        </p>
        <p v-if="state.failure?.kind === 'ceremony'" role="alert" data-testid="ceremony-failed">
          {{
            $gettext('Creating the Recovery Key did not work. Nothing was saved. Please try again.')
          }}
        </p>
        <ActionError v-if="state.failure?.kind === 'refused'" :error="state.failure.error" />
        <p>
          {{
            $gettext(
              'Your backups are locked with a key. Next, a Recovery Key is created for you. You need it to read your backups if this server is ever lost, and nobody else can give it back to you.'
            )
          }}
        </p>
        <p class="ext:text-sm ext:text-role-on-surface-variant">
          {{
            $gettext('Creating it takes a few seconds, and the page may not respond while it does.')
          }}
        </p>
        <oc-button
          class="ext:mt-3"
          appearance="filled"
          data-testid="create-key"
          @click="wizard.createKey()"
        >
          {{ $gettext('Create my Recovery Key') }}
        </oc-button>
      </section>

      <section
        v-else-if="state.step === 'generating'"
        data-step="generating"
        aria-live="polite"
        class="ext:flex ext:items-center ext:gap-2"
      >
        <oc-spinner />
        {{ $gettext('Creating your Recovery Key. This takes a moment…') }}
      </section>

      <!-- Step 2: show the key once -->
      <section v-else-if="state.step === 'show_key'" data-step="show_key">
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Save your Recovery Key now') }}</h2>
        <p>
          {{
            $gettext(
              'This is the only time it is shown. Save it in your password manager, or write it down and keep it somewhere safe.'
            )
          }}
        </p>
        <p>
          {{
            $gettext(
              'Without it, nobody — not even your administrator — can read your backups if this server is lost.'
            )
          }}
        </p>
        <RecoveryKeyDisplay :recovery-key="state.recoveryKey">
          <oc-button appearance="filled" data-testid="key-saved" @click="wizard.keySaved()">
            {{ $gettext('I have saved it') }}
          </oc-button>
        </RecoveryKeyDisplay>
      </section>

      <!-- Step 2: the confirmation gate -->
      <RecoveryKeyGate
        v-else-if="state.step === 'confirm'"
        data-step="confirm"
        :groups="state.groups"
        :mismatch="state.mismatch"
        :submitting="state.submitting"
        :submit-label="$gettext('Protect this space')"
        @submit="(answers) => wizard.confirmKey(answers)"
        @show-again="wizard.showKeyAgain()"
      />

      <!-- Setup may or may not have landed -->
      <section v-else-if="state.step === 'setup_uncertain'" data-step="setup_uncertain">
        <p class="ext:font-medium">{{ $gettext('It is not clear whether setup finished.') }}</p>
        <ActionError :error="state.error" />
        <p>
          {{
            $gettext(
              'Keep the Recovery Key you saved. Checking again finds out whether this space now uses it.'
            )
          }}
        </p>
        <oc-button
          class="ext:mt-3"
          appearance="filled"
          :disabled="state.checking"
          :show-spinner="state.checking"
          data-testid="check-again"
          @click="wizard.retryUncertain()"
        >
          {{ $gettext('Check again') }}
        </oc-button>
      </section>

      <!-- Terminal: keys already exist. There is deliberately no way back. -->
      <section v-else-if="state.step === 'already_protected'" data-step="already_protected">
        <h2 class="ext:text-lg ext:font-semibold">
          {{ $gettext('This space is already protected') }}
        </h2>
        <p>
          {{
            $gettext(
              'Its backups are already locked with a Recovery Key. Setting it up again would make every existing backup unreadable, so that is not possible.'
            )
          }}
        </p>
      </section>

      <!-- Step 3: schedule -->
      <form
        v-else-if="state.step === 'schedule'"
        data-step="schedule"
        class="ext:flex ext:flex-col ext:gap-3"
        @submit.prevent="wizard.saveSchedule()"
      >
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('When should backups run?') }}</h2>
        <fieldset class="ext:flex ext:gap-4">
          <legend class="ext:sr-only">{{ $gettext('How often') }}</legend>
          <label class="ext:flex ext:gap-2">
            <input
              type="radio"
              name="kind"
              value="daily"
              :checked="state.choice.kind === 'daily'"
              :disabled="state.saving"
              data-testid="kind-daily"
              @change="wizard.chooseSchedule({ ...state.choice, kind: 'daily' })"
            />
            {{ $gettext('Every day') }}
          </label>
          <label class="ext:flex ext:gap-2">
            <input
              type="radio"
              name="kind"
              value="weekly"
              :checked="state.choice.kind === 'weekly'"
              :disabled="state.saving"
              data-testid="kind-weekly"
              @change="wizard.chooseSchedule({ ...state.choice, kind: 'weekly' })"
            />
            {{ $gettext('Once a week') }}
          </label>
        </fieldset>
        <label v-if="state.choice.kind === 'weekly'" class="ext:flex ext:flex-col">
          {{ $gettext('Day') }}
          <select
            :value="state.choice.weekday"
            :disabled="state.saving"
            data-testid="weekday"
            @change="
              wizard.chooseSchedule({
                ...state.choice,
                weekday: Number(($event.target as HTMLSelectElement).value)
              })
            "
          >
            <option v-for="(day, index) in weekdays" :key="index" :value="index">{{ day }}</option>
          </select>
        </label>
        <label class="ext:flex ext:flex-col">
          {{ $gettext('Time') }}
          <input
            type="time"
            :value="timeOf(state.choice)"
            :disabled="state.saving"
            data-testid="time"
            @change="onTimeChange(state.choice, ($event.target as HTMLInputElement).value)"
          />
        </label>
        <p class="ext:text-sm ext:text-role-on-surface-variant" data-testid="timezone">
          <template v-if="state.timezone">
            {{
              $gettext('Times are in the server’s time zone (%{zone}).', { zone: state.timezone })
            }}
          </template>
          <template v-else>{{ $gettext('Times are in the server’s time zone.') }}</template>
        </p>
        <ActionError v-if="state.error" :error="state.error" />
        <div>
          <oc-button
            submit="submit"
            appearance="filled"
            :disabled="state.saving"
            :show-spinner="state.saving"
            data-testid="schedule-save"
          >
            {{ $gettext('Turn on backups') }}
          </oc-button>
        </div>
      </form>

      <!-- Done -->
      <section v-else-if="state.step === 'done'" data-step="done">
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Backup is set up') }}</h2>
        <p data-testid="first-run">
          <template v-if="state.status.last_successful_run">
            {{ $gettext('Backups run automatically. You can check on them at any time.') }}
          </template>
          <template v-else-if="state.status.next_run">
            {{ $gettext('First backup: %{when}', { when: format.when(state.status.next_run) }) }}
          </template>
          <template v-else>
            {{ $gettext('The first backup runs at the next scheduled time.') }}
          </template>
        </p>
        <p>
          {{
            $gettext(
              'Keep your Recovery Key safe. It is the only way to read these backups if this server is lost.'
            )
          }}
        </p>
      </section>
    </RequestState>
  </main>
</template>
