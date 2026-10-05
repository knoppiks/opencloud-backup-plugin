<script setup lang="ts">
// The confirmation gate's form: type the asked-for groups of the key just
// shown. Whether the answers match is the owning machine's decision
// (recoverykey/gate.ts); this form only collects them and says so when the
// machine reports a mismatch.
//
// Groups are counted after the `ocbk1-` prefix, and each field draws where its
// group sits in the saved key, so the count cannot start one off (#55).
//
// The answers are fragments of a Recovery Key, so they live in this
// component's state only and are cleared when new groups are asked for and on
// unmount.
import { onBeforeUnmount, ref, watch } from 'vue'
import { RK_PREFIX } from '../crypto'
import { gateHint } from '../recoverykey/gate'
import NoticeBanner from './NoticeBanner.vue'

const props = defineProps<{
  /** groups are 0-based indices into the key's seven groups. */
  groups: number[]
  mismatch: boolean
  submitting: boolean
  submitLabel: string
}>()

const emit = defineEmits<{
  submit: [answers: string[]]
  showAgain: []
}>()

const answers = ref<string[]>(props.groups.map(() => ''))

watch(
  () => props.groups.join(),
  () => {
    answers.value = props.groups.map(() => '')
  }
)

onBeforeUnmount(() => {
  answers.value = []
})
</script>

<template>
  <form class="ext:flex ext:flex-col ext:gap-3" @submit.prevent="emit('submit', [...answers])">
    <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Check that you saved it') }}</h2>
    <p>
      {{
        $gettext(
          'Type two groups from your saved Recovery Key. Groups are counted after %{prefix}-. This makes sure you can find it when you need it.',
          { prefix: RK_PREFIX }
        )
      }}
    </p>
    <div v-for="(group, index) in groups" :key="group" class="ext:flex ext:flex-col ext:gap-1">
      <code
        class="ext:font-mono ext:break-all"
        aria-hidden="true"
        :data-testid="`gate-hint-${group + 1}`"
        ><template v-for="(part, i) in gateHint(group)" :key="i"
          ><template v-if="i > 0">-</template
          ><mark
            v-if="part.asked"
            class="ext:rounded ext:bg-role-primary-container ext:px-0.5 ext:font-bold ext:text-role-on-primary-container"
            >{{ part.text }}</mark
          ><template v-else>{{ part.text }}</template></template
        ></code
      >
      <oc-text-input
        v-model="answers[index]"
        :label="
          $gettext('Group %{number} after %{prefix}-', {
            number: String(group + 1),
            prefix: RK_PREFIX
          })
        "
        :disabled="submitting"
        :data-testid="`gate-${group + 1}`"
      />
    </div>
    <NoticeBanner
      v-if="mismatch"
      tone="danger"
      :message="$gettext('That does not match. Look at your saved copy and try again.')"
      data-testid="gate-mismatch"
    />
    <div class="ext:flex ext:gap-2">
      <oc-button
        appearance="outline"
        :disabled="submitting"
        data-testid="show-again"
        @click="emit('showAgain')"
      >
        {{ $gettext('Show the key again') }}
      </oc-button>
      <oc-button
        submit="submit"
        appearance="filled"
        :disabled="submitting"
        :show-spinner="submitting"
        data-testid="gate-submit"
      >
        {{ submitLabel }}
      </oc-button>
    </div>
  </form>
</template>
