<script setup lang="ts">
// The confirmation gate's form: type the asked-for groups of the key just
// shown. Whether the answers match is the owning machine's decision
// (recoverykey/gate.ts); this form only collects them and says so when the
// machine reports a mismatch.
//
// The answers are fragments of a Recovery Key, so they live in this
// component's state only and are cleared when new groups are asked for and on
// unmount.
import { onBeforeUnmount, ref, watch } from 'vue'

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
  <form class="ext:flex ext:flex-col ext:gap-2" @submit.prevent="emit('submit', [...answers])">
    <h2 class="ext:text-lg ext:font-semibold">{{ $gettext('Check that you saved it') }}</h2>
    <p>
      {{
        $gettext(
          'Type two groups from your saved Recovery Key. This makes sure you can find it when you need it.'
        )
      }}
    </p>
    <oc-text-input
      v-for="(group, index) in groups"
      :key="group"
      v-model="answers[index]"
      :label="$gettext('Group %{number}', { number: String(group + 1) })"
      :disabled="submitting"
      :data-testid="`gate-${group + 1}`"
    />
    <p v-if="mismatch" role="alert" data-testid="gate-mismatch">
      {{ $gettext('That does not match. Look at your saved copy and try again.') }}
    </p>
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
