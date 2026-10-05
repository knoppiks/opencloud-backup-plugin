<script setup lang="ts">
// "Yes, back up my data": the setup wizard for one Space.
//
// All decisions live in wizard/machine.ts; this file renders its state and
// forwards clicks. The machine is created per mounted wizard and disposed on
// unmount, which is what keeps the Recovery Key in component state only: it is
// never in a store, a route, the URL or browser storage (8d decision 3; lint
// bans storage here), and it is gone when the page is left.
//
// Every control is the host's own (`oc-radio`, `oc-select`; Phase 8g). The
// design system has no time field, so the time is a dropdown of half hours
// (wizard/timeoptions.ts).
import { computed, onBeforeUnmount, onMounted, shallowRef } from 'vue'
import { useGettext } from 'vue3-gettext'
import ActionError from '../components/ActionError.vue'
import NoticeBanner from '../components/NoticeBanner.vue'
import RecoveryKeyDisplay from '../components/RecoveryKeyDisplay.vue'
import RecoveryKeyGate from '../components/RecoveryKeyGate.vue'
import PageLayout from '../components/PageLayout.vue'
import StepIndicator from '../components/StepIndicator.vue'
import { spaceCrumbs } from '../layout/breadcrumbs'
import RequestState from '../components/RequestState.vue'
import { useBackupApi } from '../composables/useBackupApi'
import { useFormat } from '../composables/useFormat'
import { useIsAdmin } from '../composables/useIsAdmin'
import { performSetupCeremony, recoveryKeyOpens } from '../crypto'
import {
  cryptoRandomInt,
  nextFrame,
  SetupWizard,
  type ScheduleChoice,
  type WizardState
} from '../wizard/machine'
import { wizardProgress, wizardStepNames } from '../wizard/progress'
import { selectedTime, timeOptions, type TimeOption } from '../wizard/timeoptions'

const props = defineProps<{ spaceId: string }>()

const gettext = useGettext()
const { $gettext } = gettext
const format = useFormat()
/** isAdmin only decides what is offered, never what is allowed (8e decision 4). */
const isAdmin = useIsAdmin()

const state = shallowRef<WizardState>({ step: 'loading' })
/**
 * crumbs is the page's breadcrumb trail. It names the Space once wizard has
 * read it, which happens with a state change: reading `state` is what makes
 * this follow, since wizard.spaceName itself is not reactive.
 */
const crumbs = computed(() => {
  void state.value
  return spaceCrumbs($gettext, props.spaceId, wizard.spaceName, { text: $gettext('Set up backup') })
})
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

/** progress is the step the indicator marks, or undefined to show none. */
const progress = computed(() => wizardProgress(state.value))
const stepNames = computed(() => wizardStepNames($gettext))

/** WeekdayOption is one entry of the weekday dropdown; value 0 is Sunday. */
interface WeekdayOption {
  value: number
  label: string
}

/** weekdays are named by the browser in the user's language; 0 is Sunday. */
const weekdays = computed<WeekdayOption[]>(() => {
  const name = new Intl.DateTimeFormat(gettext.current || 'en', {
    weekday: 'long',
    timeZone: 'UTC'
  })
  // 2026-09-20 is a Sunday.
  return Array.from({ length: 7 }, (_, day) => ({
    value: day,
    label: name.format(new Date(Date.UTC(2026, 8, 20 + day)))
  }))
})

/** times are the offered times; the stored one is always among them. */
const times = computed(() =>
  state.value.step === 'schedule' ? timeOptions(state.value.choice) : []
)

function chooseKind(choice: ScheduleChoice, kind: ScheduleChoice['kind']): void {
  wizard.chooseSchedule({ ...choice, kind })
}

function chooseWeekday(choice: ScheduleChoice, day: WeekdayOption | null): void {
  if (day) {
    wizard.chooseSchedule({ ...choice, weekday: day.value })
  }
}

function chooseTime(choice: ScheduleChoice, time: TimeOption | null): void {
  if (time) {
    wizard.chooseSchedule({ ...choice, hour: time.hour, minute: time.minute })
  }
}

onMounted(() => wizard.start())
onBeforeUnmount(() => wizard.dispose())
</script>

<template>
  <PageLayout :crumbs="crumbs" narrow>
    <StepIndicator v-if="progress !== undefined" :steps="stepNames" :current="progress" />
    <RequestState :loading="state.step === 'loading'" :error="loadError" @retry="wizard.start()">
      <!-- Viewer -->
      <NoticeBanner
        v-if="state.step === 'not_allowed'"
        tone="info"
        :message="$gettext('Only editors and managers of this space can set up backup.')"
        data-step="not_allowed"
      />

      <!-- No destination granted. An admin is the one who can change that,
           so they are offered the way (8g decision 7). -->
      <NoticeBanner
        v-else-if="state.step === 'no_targets'"
        tone="warning"
        :title="$gettext('No backup destination is available to you yet')"
        :message="
          isAdmin
            ? $gettext('Add a backup destination, or share an existing one with yourself.')
            : $gettext(
                'Ask your administrator to share one with you. Until then, no space can be set up.'
              )
        "
        data-step="no_targets"
      >
        <template v-if="isAdmin" #actions>
          <oc-button
            type="router-link"
            :to="{ name: 'backup-vault-admin-targets' }"
            appearance="outline"
            data-testid="no-targets-admin"
          >
            {{ $gettext('Add or share a destination') }}
          </oc-button>
        </template>
      </NoticeBanner>

      <!-- Step 1: destination -->
      <section
        v-else-if="state.step === 'pick_target'"
        class="ext:flex ext:flex-col ext:gap-3"
        data-step="pick_target"
      >
        <template v-if="state.targets.length > 1">
          <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Where should backups go?') }}</h2>
          <fieldset class="ext:flex ext:flex-col ext:gap-2" data-testid="target-picker">
            <legend class="ext:sr-only">{{ $gettext('Backup destination') }}</legend>
            <oc-radio
              v-for="target in state.targets"
              :key="target.id"
              :model-value="state.selected"
              :option="target.id"
              :label="target.name"
              :disabled="state.saving"
              :data-testid="`target-${target.id}`"
              @update:model-value="wizard.selectTarget(target.id)"
            />
          </fieldset>
        </template>
        <p v-else-if="state.saving" class="ext:flex ext:items-center ext:gap-2">
          <oc-spinner />
          {{ $gettext('Saving…') }}
        </p>
        <ActionError v-if="state.error" :error="state.error" />
        <div v-if="state.targets.length > 1 || state.error">
          <oc-button
            appearance="filled"
            :disabled="state.saving || state.selected === undefined"
            :show-spinner="state.saving"
            data-testid="target-continue"
            @click="wizard.confirmTarget()"
          >
            {{ state.error ? $gettext('Try again') : $gettext('Continue') }}
          </oc-button>
        </div>
      </section>

      <!-- Step 2, editor: a manager has to continue -->
      <NoticeBanner
        v-else-if="state.step === 'needs_manager'"
        tone="info"
        :title="$gettext('A manager of this space has to finish setup.')"
        :message="
          $gettext(
            'The backup destination is chosen. Creating the Recovery Key needs a manager of this space.'
          )
        "
        data-step="needs_manager"
      />

      <!-- Step 2: Recovery Key, introduction -->
      <section
        v-else-if="state.step === 'key_intro'"
        class="ext:flex ext:flex-col ext:gap-3"
        data-step="key_intro"
      >
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Your Recovery Key') }}</h2>
        <NoticeBanner
          v-if="state.discardPrevious"
          tone="warning"
          role="alert"
          :message="
            $gettext(
              'The Recovery Key shown before was not taken into use. Throw away any copy of it; a new one will be created.'
            )
          "
          data-testid="discard-previous"
        />
        <NoticeBanner
          v-if="state.failure?.kind === 'ceremony'"
          tone="danger"
          :message="
            $gettext('Creating the Recovery Key did not work. Nothing was saved. Please try again.')
          "
          data-testid="ceremony-failed"
        />
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
        <div>
          <oc-button appearance="filled" data-testid="create-key" @click="wizard.createKey()">
            {{ $gettext('Create my Recovery Key') }}
          </oc-button>
        </div>
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
      <section
        v-else-if="state.step === 'show_key'"
        class="ext:flex ext:flex-col ext:gap-2"
        data-step="show_key"
      >
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
      <section
        v-else-if="state.step === 'setup_uncertain'"
        class="ext:flex ext:flex-col ext:gap-3"
        data-step="setup_uncertain"
      >
        <NoticeBanner
          tone="warning"
          :title="$gettext('It is not clear whether setup finished.')"
          :message="
            $gettext(
              'Keep the Recovery Key you saved. Checking again finds out whether this space now uses it.'
            )
          "
        />
        <ActionError :error="state.error" />
        <div>
          <oc-button
            appearance="filled"
            :disabled="state.checking"
            :show-spinner="state.checking"
            data-testid="check-again"
            @click="wizard.retryUncertain()"
          >
            {{ $gettext('Check again') }}
          </oc-button>
        </div>
      </section>

      <!-- Terminal: keys already exist. There is deliberately no way back. -->
      <NoticeBanner
        v-else-if="state.step === 'already_protected'"
        tone="info"
        :title="$gettext('This space is already protected')"
        :message="
          $gettext(
            'Its backups are already locked with a Recovery Key. Setting it up again would make every existing backup unreadable, so that is not possible.'
          )
        "
        data-step="already_protected"
      />

      <!-- Step 3: schedule -->
      <form
        v-else-if="state.step === 'schedule'"
        data-step="schedule"
        class="ext:flex ext:flex-col ext:gap-4"
        @submit.prevent="wizard.saveSchedule()"
      >
        <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('When should backups run?') }}</h2>
        <fieldset class="ext:flex ext:flex-wrap ext:gap-x-6 ext:gap-y-2">
          <legend class="ext:sr-only">{{ $gettext('How often') }}</legend>
          <oc-radio
            :model-value="state.choice.kind"
            option="daily"
            :label="$gettext('Every day')"
            :disabled="state.saving"
            data-testid="kind-daily"
            @update:model-value="chooseKind(state.choice, 'daily')"
          />
          <oc-radio
            :model-value="state.choice.kind"
            option="weekly"
            :label="$gettext('Once a week')"
            :disabled="state.saving"
            data-testid="kind-weekly"
            @update:model-value="chooseKind(state.choice, 'weekly')"
          />
        </fieldset>
        <div class="ext:flex ext:flex-wrap ext:gap-4">
          <oc-select
            v-if="state.choice.kind === 'weekly'"
            class="ext:min-w-48"
            :model-value="weekdays[state.choice.weekday]"
            :options="weekdays"
            :label="$gettext('Day')"
            :searchable="false"
            :disabled="state.saving"
            data-testid="weekday"
            @update:model-value="chooseWeekday(state.choice, $event)"
          />
          <oc-select
            class="ext:min-w-32"
            :model-value="selectedTime(times, state.choice)"
            :options="times"
            :label="$gettext('Time')"
            :disabled="state.saving"
            data-testid="time"
            @update:model-value="chooseTime(state.choice, $event)"
          />
        </div>
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
      <NoticeBanner
        v-else-if="state.step === 'done'"
        tone="success"
        :title="$gettext('Backup is set up')"
        data-step="done"
      >
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
      </NoticeBanner>
    </RequestState>
  </PageLayout>
</template>
