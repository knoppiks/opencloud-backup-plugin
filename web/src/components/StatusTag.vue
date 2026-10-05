<script setup lang="ts">
// A Space's state as a tag (layout/tone.ts maps state to tone and icon).
import { computed } from 'vue'
import { useGettext } from 'vue3-gettext'
import { stateLook } from '../layout/tone'
import type { SpaceState } from '../status/spacestate'
import { stateLabel } from '../status/statetext'
import ToneTag from './ToneTag.vue'

const props = defineProps<{ state: SpaceState }>()

const { $gettext } = useGettext()
const look = computed(() => stateLook(props.state))
</script>

<template>
  <ToneTag
    :tone="look.tone"
    :icon="look.icon"
    :label="stateLabel(state, $gettext)"
    :data-state="state"
    data-testid="state-label"
  />
</template>
