<script setup>
import { computed } from 'vue'
const props = defineProps({ page: { type: Number, default: 1 }, pageSize: { type: Number, default: 10 }, total: { type: Number, default: 0 } })
const emit = defineEmits(['update:page', 'update:pageSize'])
const pages = computed(() => Math.max(1, Math.ceil(props.total / props.pageSize)))
function setSize(event) { emit('update:pageSize', Number(event.target.value)); emit('update:page', 1) }
</script>
<template>
  <footer class="pagination pager"><span>共 {{ total }} 条</span><label>每页 <select :value="pageSize" @change="setSize"><option :value="10">10</option><option :value="50">50</option><option :value="100">100</option><option :value="500">500</option></select> 条</label><button class="icon-button" type="button" title="上一页" :disabled="page <= 1" @click="emit('update:page', page - 1)">‹</button><strong>{{ page }} / {{ pages }}</strong><button class="icon-button" type="button" title="下一页" :disabled="page >= pages" @click="emit('update:page', page + 1)">›</button></footer>
</template>
