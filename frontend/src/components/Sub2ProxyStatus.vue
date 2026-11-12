<script setup>
import {computed} from 'vue'
import {proxyReleaseText} from '../sub2ProxyRelease'
import {formatTime} from '../utils'
const props = defineProps({id:String, binding:Object, now:Number, error:String, retrying:String})
const emit = defineEmits(['retry'])
const detail = computed(() => props.binding ? [
  `代理：${props.binding.proxy_name || '#' + props.binding.proxy_id}`,
  `绑定时间：${formatTime(props.binding.bound_at)}`,
  props.binding.due_at ? `预计解绑：${formatTime(props.binding.due_at)}` : '一直绑定',
  props.binding.error, props.error,
].filter(Boolean).join('\n') : props.error || '正在读取已保存的代理绑定状态')
</script>
<template>
  <small v-if="id" class="proxy-binding-state" :class="{failed: binding?.state === 'retry' || error}" :title="detail">
    <span>{{ error ? '代理状态读取失败，自动重试中' : proxyReleaseText(binding, now) }}</span>
    <button v-if="binding?.state === 'retry'" type="button" class="proxy-retry" :disabled="!!retrying" @click.stop="emit('retry', id)">{{retrying === id ? '提交中…' : '重试解绑'}}</button>
  </small>
</template>
<style scoped>
.proxy-binding-state{display:flex;gap:6px;flex-wrap:wrap;font-size:11px;line-height:1.6;margin-top:4px;color:var(--muted,#64748b);white-space:normal}.failed{color:#b45309}.proxy-retry{background:none;border:0;padding:0;color:var(--primary,#2563eb);font-size:inherit;cursor:pointer;text-decoration:underline}.proxy-retry:disabled{opacity:.5;cursor:wait}
</style>
