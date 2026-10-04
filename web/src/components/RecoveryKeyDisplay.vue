<script setup lang="ts">
// A Recovery Key shown once, exactly as it is saved, with a copy button.
//
// Used by setup and by replacement. The key arrives as a prop from the page
// that owns it and is never kept here beyond rendering. It is shown as one
// string, prefix included, so what is on screen, what "Copy" writes and what a
// person selects by hand are the same text (#55): the gate counts groups the
// way that saved text reads. The copy button writes to the clipboard, which is
// local to this device but outlives the page: that is what "copy" means, and
// nothing here reads the clipboard back (8d.2 outcome). The page adds its own
// "I have saved it" through the slot.
import { ref, watch } from 'vue'

const props = defineProps<{ recoveryKey: string }>()

const copied = ref(false)

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
    <div class="ext:my-3 ext:flex ext:items-center ext:gap-2">
      <!-- No whitespace inside <code>: a hand selection must be the key, nothing more. -->
      <code
        class="ext:flex-1 ext:rounded ext:border ext:border-role-outline-variant ext:bg-role-surface-container ext:px-3 ext:py-2 ext:font-mono ext:text-lg ext:break-all ext:select-all"
        data-testid="recovery-key"
        :aria-label="$gettext('Recovery Key')"
        >{{ recoveryKey }}</code
      >
      <oc-button appearance="outline" data-testid="copy-key" @click="copyKey()">
        {{ copied ? $gettext('Copied') : $gettext('Copy') }}
      </oc-button>
    </div>
    <div class="ext:flex ext:gap-2">
      <slot />
    </div>
  </div>
</template>
