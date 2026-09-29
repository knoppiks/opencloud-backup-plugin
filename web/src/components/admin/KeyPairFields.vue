<script setup lang="ts">
// One S3 key pair, typed in. Write-only: the parent starts it empty, and
// nothing ever fills it from the server (decisions.md #14).
//
// `autocomplete="off"` on both: a browser that offers to save or refill an S3
// secret keeps it somewhere this project does not control.
import type { KeyPairInput } from '../../admin/targetform'

const props = defineProps<{
  modelValue: KeyPairInput
  legend: string
  description?: string
  idError?: string
  secretError?: string
  disabled?: boolean
}>()

const emit = defineEmits<{ 'update:modelValue': [value: KeyPairInput] }>()

function update(field: keyof KeyPairInput, value: string): void {
  emit('update:modelValue', { ...props.modelValue, [field]: value })
}
</script>

<template>
  <fieldset class="ext:flex ext:flex-col ext:gap-2">
    <legend class="ext:font-medium">{{ legend }}</legend>
    <p v-if="description" class="ext:text-sm ext:text-role-on-surface-variant">
      {{ description }}
    </p>
    <oc-text-input
      :model-value="modelValue.accessKeyId"
      :label="$gettext('Access key ID')"
      :error-message="idError"
      :disabled="disabled"
      autocomplete="off"
      data-testid="access-key-id"
      @update:model-value="update('accessKeyId', $event)"
    />
    <oc-text-input
      :model-value="modelValue.secretAccessKey"
      type="password"
      :label="$gettext('Secret access key')"
      :error-message="secretError"
      :disabled="disabled"
      autocomplete="off"
      data-testid="secret-access-key"
      @update:model-value="update('secretAccessKey', $event)"
    />
  </fieldset>
</template>
