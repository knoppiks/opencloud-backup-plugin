<script setup lang="ts">
// The frame of every Backup Vault page, modelled on the Files app: a header
// bar with the breadcrumb trail as the title, an optional status beside it
// and the page's actions on the right; below it the content, which scrolls on
// its own so the header stays put (Phase 8g).
//
// The trail's last crumb is the page title. It is repeated as an `h1` for
// assistive technology, hidden visually because the breadcrumb already shows
// it, which is what Files does too.
//
// `narrow` caps the content at a readable form width; tables and status
// boards use the full width.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
import { computed } from 'vue'
import { pageTitle, type Crumb } from '../layout/breadcrumbs'

const props = withDefaults(defineProps<{ crumbs: Crumb[]; narrow?: boolean }>(), {
  narrow: false
})

const title = computed(() => pageTitle(props.crumbs))
</script>

<template>
  <main class="ext:flex ext:flex-col ext:w-full ext:h-full ext:min-w-0">
    <header
      class="ext:flex ext:flex-wrap ext:items-center ext:justify-between ext:gap-x-4 ext:gap-y-2 ext:px-4 ext:py-3 ext:min-h-16"
      data-testid="page-header"
    >
      <!-- The host's breadcrumb only bolds a linked current crumb; ours is
           not a link, so the title is bolded here, as Files shows it. -->
      <div
        class="ext:flex ext:flex-wrap ext:items-center ext:gap-3 ext:min-w-0 ext:[&_[aria-current=page]]:font-bold"
      >
        <h1 class="ext:sr-only">{{ title }}</h1>
        <!-- Trails here are at most four short crumbs: never fold them into
             "…", which the host does from the third crumb on otherwise. -->
        <oc-breadcrumb :items="crumbs" :truncation-offset="crumbs.length" />
        <slot name="status" />
      </div>
      <div
        v-if="$slots.actions"
        class="ext:flex ext:flex-wrap ext:items-center ext:gap-2"
        data-testid="page-actions"
      >
        <slot name="actions" />
      </div>
    </header>
    <div class="ext:flex-1 ext:overflow-y-auto ext:px-4 ext:pb-6">
      <div class="ext:flex ext:flex-col ext:gap-4" :class="{ 'ext:max-w-2xl': narrow }">
        <slot />
      </div>
    </div>
  </main>
</template>
