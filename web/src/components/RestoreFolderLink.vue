<script setup lang="ts">
// A restore's folder: a link into the Files app when the host knows the
// Space, and the folder's path as plain text when it does not. The text is
// the same either way, so a person can find the folder by hand.
import { computed } from 'vue'
import { useRestoreFolderLink } from '../composables/useRestoreFolderLink'
import { isRestoreFolder } from '../restore/folderlink'

const props = defineProps<{ spaceId: string; folder: string }>()

const linkTo = useRestoreFolderLink()
const location = computed(() => linkTo(props.spaceId, props.folder))
</script>

<template>
  <template v-if="isRestoreFolder(folder)">
    <router-link v-if="location" :to="location" class="ext:font-medium" data-testid="folder-link">
      {{ folder }}
    </router-link>
    <span v-else class="ext:font-mono" data-testid="folder-path">{{ folder }}</span>
  </template>
</template>
