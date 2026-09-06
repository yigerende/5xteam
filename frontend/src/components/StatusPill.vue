<script setup>
defineProps({ tone: { type: String, default: 'pending' }, dot: { type: Boolean, default: true } })
</script>

<template>
  <span class="status-pill" :class="`tone-${tone}`"><i v-if="dot"></i><slot /></span>
</template>
