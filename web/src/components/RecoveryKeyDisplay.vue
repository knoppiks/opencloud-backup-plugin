<script setup lang="ts">
// A Recovery Key shown once, in numbered groups, with a copy button.
//
// Used by setup and by replacement. The key arrives as a prop from the page
// that owns it and is never kept here beyond rendering. The copy button writes
// to the clipboard, which is local to this device but outlives the page: that
// is what "copy" means, and nothing here reads the clipboard back (8d.2
// outcome). The page adds its own "I have saved it" through the slot.
import { computed, ref, watch } from 'vue'
import { recoveryKeyGroups } from '../crypto'

const props = defineProps<{ recoveryKey: string }>()

const copied = ref(false)
const groups = computed(() => recoveryKeyGroups(props.recoveryKey))

watch(
  () => props.recoveryKey,
  () => {
    copied.value = false
  }
)

async function copyKey(): Promise<void> {
  try {
    await globalThis.navigator.clipboard.writeText(props.recoveryKey)
    copied.value = true
  } catch {
    copied.value = false
  }
}
</script>

<template>
  <div>
    <ol
      class="ext:my-3 ext:grid ext:grid-cols-4 ext:gap-2 ext:font-mono ext:text-lg"
      data-testid="recovery-key"
      :aria-label="$gettext('Recovery Key')"
    >
      <li
        v-for="(group, index) in groups"
        :key="index"
        class="ext:flex ext:flex-col ext:items-center"
      >
        <span class="ext:text-xs ext:text-role-on-surface-variant">{{ index + 1 }}</span>
        <span>{{ group }}</span>
      </li>
    </ol>
    <div class="ext:flex ext:gap-2">
      <oc-button appearance="outline" data-testid="copy-key" @click="copyKey()">
        {{ copied ? $gettext('Copied') : $gettext('Copy') }}
      </oc-button>
      <slot />
    </div>
  </div>
</template>
