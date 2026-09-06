<script setup>
defineProps({ label: { type: String, required: true }, danger: Boolean, disabled: Boolean })
defineEmits(['click'])
</script>

<template>
  <button class="icon-button" :class="{ danger }" type="button" :title="label" :aria-label="label" :disabled="disabled" @click="$emit('click')">
    <slot />
  </button>
</template>
