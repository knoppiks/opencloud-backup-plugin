<script setup lang="ts">
// "Nothing here", said the way OpenCloud says it: a large muted icon, one
// line of what is (not) there, and what to do next if anything.
//
// Modelled on web-pkg's NoContentMessage rather than using it: that would put
// web-pkg itself into every component test that renders an empty list, and
// the look is three classes.
//
// `oc-*` elements are host globals: see components/RequestState.vue.
withDefaults(defineProps<{ icon?: string; message: string }>(), { icon: 'information' })
</script>

<template>
  <div
    class="ext:flex ext:flex-col ext:items-center ext:justify-center ext:gap-3 ext:py-8 ext:text-center ext:text-role-on-surface-variant"
    data-testid="empty-state"
  >
    <oc-icon :name="icon" fill-type="line" size="xlarge" />
    <p class="ext:text-lg">{{ message }}</p>
    <slot />
  </div>
</template>
