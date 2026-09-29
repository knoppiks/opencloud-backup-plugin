<script setup lang="ts">
// The admin views' client-side gate: renders its content for an OpenCloud
// administrator and a plain notice for anyone else, without calling the API.
//
// This decides what is *offered*. The backend's admin middleware decides what
// is allowed, and a 403 from it is shown as "not an administrator" by each
// view regardless of what this said (8e decision 1).
import { useIsAdmin } from '../../composables/useIsAdmin'

const isAdmin = useIsAdmin()
</script>

<template>
  <slot v-if="isAdmin" />
  <p v-else role="alert" data-testid="not-admin">
    {{ $gettext('Only administrators can manage backup destinations') }}
  </p>
</template>
