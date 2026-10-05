<script setup lang="ts">
// An inline notice: a title, an optional message, and room for an action.
// It stays until what it reports changes (layout/tone.ts explains why this is
// not the design system's toast). A notice that is one sentence leaves the
// title out and says it as the message, so it is not set in bold.
//
// A danger notice is an alert, read out at once; the others are a status,
// read out when the reader is idle.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { computed } from 'vue'
import { toneClasses, toneIcon, type Tone } from '../layout/tone'

const props = withDefaults(defineProps<{ tone?: Tone; title?: string; message?: string }>(), {
  tone: 'neutral',
  title: '',
  message: ''
})

const role = computed(() => (props.tone === 'danger' ? 'alert' : 'status'))
</script>

<template>
  <div
    class="ext:flex ext:items-start ext:gap-3 ext:rounded-xl ext:px-4 ext:py-3"
    :class="toneClasses(tone)"
    :role="role"
    :data-tone="tone"
  >
    <oc-icon :name="toneIcon(tone)" fill-type="line" class="ext:shrink-0 ext:mt-0.5" />
    <div class="ext:flex ext:flex-col ext:gap-1 ext:min-w-0 ext:flex-1">
      <p v-if="title" class="ext:font-semibold" data-testid="notice-title">{{ title }}</p>
      <p v-if="message" data-testid="notice-message">{{ message }}</p>
      <slot />
    </div>
    <div v-if="$slots.actions" class="ext:shrink-0 ext:self-center">
      <slot name="actions" />
    </div>
  </div>
</template>
