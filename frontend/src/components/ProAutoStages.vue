<script setup>
import { LoaderCircle } from 'lucide-vue-next'
defineProps({ state: { type: Object, default: () => ({}) } })
const steps = [['login','登录'],['recharge','开通'],['oauth','RT/AT'],['push','推送'],['quota','额度'],['merge','合并']]
const names = {pending:'待处理',running:'执行中',waiting:'等待额度',completed:'已完成',skipped:'已跳过',failed:'失败',interrupted:'已中断',unknown:'待确认'}
</script>
<template><div class="pro-auto-stages"><span v-for="[key,label] in steps" :key="key" :data-stage="key" :class="state.steps?.[key] || 'pending'" :title="label + '：' + (state.errors?.[key] || names[state.steps?.[key]] || '未开始')"><LoaderCircle v-if="state.steps?.[key] === 'running'" :size="11" class="spin" />{{ label }}<small>{{ names[state.steps?.[key]] || '未开始' }}</small></span></div></template>
<style scoped>
.pro-auto-stages { display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:4px;min-width:260px; }
.pro-auto-stages>span {display:flex;align-items:center;gap:4px;padding:4px 6px;font-size:11px;border:1px solid var(--line);border-radius:5px;color:var(--muted);white-space:nowrap;}
small {font-size:10px;}
.completed {color:var(--green-strong)!important;background:var(--green-bg);border-color:var(--green)!important;}
.running,.waiting,.unknown {color:var(--amber)!important;background:var(--amber-bg);}
.failed,.interrupted {color:var(--red)!important;background:var(--red-bg);}
.spin {animation:pro-stage-spin 1s linear infinite;}
@keyframes pro-stage-spin {to {transform:rotate(360deg);}}
</style>
