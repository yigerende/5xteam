<script setup>
defineProps({ message: { type: Object, default: () => ({ text: '', type: '' }) } })
</script>

<template>
  <p class="message-bar" :class="message.type" role="status">{{ message.text }}</p>
</template>
