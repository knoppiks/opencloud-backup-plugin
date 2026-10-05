<script setup lang="ts">
// Where a multi-step flow is: numbered steps, the current one marked, the
// finished ones ticked (Phase 8g). The design system has no stepper, so this
// is drawn from theme roles like the status tags.
//
// `current` is 0-based; `steps.length` means every step is done. Each step
// also says its state in words for screen readers, so the marks are never
// the only cue.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
const props = defineProps<{ steps: string[]; current: number }>()

type StepState = 'done' | 'current' | 'upcoming'

function stateOf(index: number): StepState {
  if (index < props.current) {
    return 'done'
  }
  return index === props.current ? 'current' : 'upcoming'
}

function badgeClasses(index: number): string {
  switch (stateOf(index)) {
    case 'done':
      return 'ext:bg-role-primary ext:text-role-on-primary'
    case 'current':
      return 'ext:bg-role-secondary ext:text-role-on-secondary'
    case 'upcoming':
      return 'ext:bg-role-surface-container-high ext:text-role-on-surface-variant'
  }
}
</script>

<template>
  <nav :aria-label="$gettext('Setup steps')" data-testid="steps">
    <ol
      class="ext:m-0 ext:flex ext:list-none ext:flex-wrap ext:items-center ext:gap-x-6 ext:gap-y-2 ext:p-0"
    >
      <li
        v-for="(name, index) in steps"
        :key="index"
        class="ext:flex ext:items-center ext:gap-2"
        :data-state="stateOf(index)"
        :aria-current="stateOf(index) === 'current' ? 'step' : undefined"
      >
        <span
          class="ext:flex ext:size-6 ext:shrink-0 ext:items-center ext:justify-center ext:rounded-full ext:text-sm ext:font-semibold"
          :class="badgeClasses(index)"
          aria-hidden="true"
        >
          <oc-icon v-if="stateOf(index) === 'done'" name="check" size="small" fill-type="line" />
          <template v-else>{{ index + 1 }}</template>
        </span>
        <span
          :class="{
            'ext:font-semibold': stateOf(index) === 'current',
            'ext:text-role-on-surface-variant': stateOf(index) === 'upcoming'
          }"
        >
          {{ name }}
        </span>
        <span v-if="stateOf(index) === 'done'" class="ext:sr-only">{{ $gettext('(done)') }}</span>
        <span v-else-if="stateOf(index) === 'current'" class="ext:sr-only">
          {{
            $gettext('(step %{number} of %{total})', {
              number: String(index + 1),
              total: String(steps.length)
            })
          }}
        </span>
      </li>
    </ol>
  </nav>
</template>
