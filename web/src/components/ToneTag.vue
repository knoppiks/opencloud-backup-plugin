<script setup lang="ts">
// A small tag: icon and words on a tone's colours, shaped like the host's own
// tag (layout/tone.ts explains why it is not `oc-tag`). The words always
// carry the meaning; colour and icon only support them.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { toneClasses, type Tone } from '../layout/tone'

defineProps<{ tone: Tone; icon: string; label: string }>()
</script>

<template>
  <span
    class="ext:inline-flex ext:items-center ext:gap-1 ext:rounded-full ext:px-2 ext:py-0.5 ext:min-h-6 ext:text-sm ext:font-medium ext:whitespace-nowrap"
    :class="toneClasses(tone)"
    :data-tone="tone"
  >
    <oc-icon :name="icon" fill-type="line" size="small" />
    {{ label }}
  </span>
</template>
