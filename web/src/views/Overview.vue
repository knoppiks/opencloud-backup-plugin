<script setup lang="ts">
// Placeholder. The per-Space overview cards land in sub-phase 8d; this view
// exists so the extension builds, loads, and shows a nav entry while the
// crypto foundation (src/crypto) is reviewed on its own.
import { useGettext } from 'vue3-gettext'

const { $gettext } = useGettext()
</script>

<template>
  <div class="ext:p-4">
    <h1>{{ $gettext('Backup Vault') }}</h1>
    <p>{{ $gettext('Not available yet.') }}</p>
  </div>
</template>
