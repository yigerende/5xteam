<script setup>
import { computed } from 'vue'
import CompactErrorDetails from './CompactErrorDetails.vue'
const props=defineProps({account:{type:Object,default:()=>({})},disabled:Boolean})
defineEmits(['retry'])
const merchant=computed(()=>props.account.pro_merchant)
const stages=computed(()=>{
  const m=merchant.value
  return [
    {label:'提交申请',state:m?.accepted_at?'completed':m?.submitted_at?(m.status==='failed'?'failed':'waiting'):'pending'},
    {label:'查询进度',state:m?.completed_at?'completed':m?.job_id?(m.status==='failed'?'failed':'waiting'):'pending'},
    {label:'处理完成',state:m?.completed_at?'completed':m?.status==='failed'&&m?.job_id?'failed':'pending'},
  ]
})
const names={completed:'成功',waiting:'等待',failed:'失败',pending:'未开始'}
function time(value){return value?new Date(value).toLocaleString('zh-CN',{hour12:false}):''}
</script>
<template>
  <div class="merchant-stages">
    <div class="merchant-strip"><span v-for="step in stages" :key="step.label" :class="step.state">{{step.label}}<small>{{names[step.state]}}</small></span></div>
    <template v-if="merchant">
      <small>供应商：{{merchant.provider === 'starbridge' ? '星桥 StarBridge' : '原 API 商户'}}</small>
      <small>{{merchant.message || '等待商户处理'}}</small>
      <small v-if="merchant.job_id" :title="merchant.job_id">申请编号：{{merchant.job_id}}</small>
      <small v-if="merchant.accepted_at">受理：{{time(merchant.accepted_at)}}</small>
      <small v-if="merchant.checked_at">查询：{{time(merchant.checked_at)}}</small>
      <small v-if="merchant.next_check_at && !account.pro_auto?.stop_requested && !merchant.completed_at">下次查询：{{time(merchant.next_check_at)}}</small>
      <small v-if="merchant.completed_at">完成：{{time(merchant.completed_at)}}</small>
      <small v-if="account.pro_auto?.stop_requested && !merchant.completed_at">已停止本地查询，商户已受理的申请无法撤回</small>
      <CompactErrorDetails :messages="[merchant.error,account.pro_post_merge_groups?.error,account.vm2api?.error]" />
      <button v-if="!merchant.completed_at || account.pro_post_merge_groups?.status==='failed' || account.vm2api?.stage==='delete_failed'" type="button" class="btn ghost compact" :disabled="disabled" @click="$emit('retry')">{{merchant.job_id?'查询 / 续跑进度':'执行 / 续跑申请'}}</button>
    </template>
    <small v-else>达到所选条件后提交商户，每 15 秒查询；最终成功才完成。</small>
  </div>
</template>
<style scoped>
.merchant-stages{min-width:260px;max-width:340px;display:grid;gap:5px}.merchant-strip{display:flex;gap:4px}.merchant-strip>span{display:flex;flex-direction:column;gap:2px;border:1px solid var(--line);border-radius:5px;padding:4px 6px;font-size:11px}.merchant-stages small{font-size:11px;color:var(--muted);overflow-wrap:anywhere}.merchant-strip .completed{background:var(--green-bg);color:var(--green-strong)}.merchant-strip .waiting{background:var(--amber-bg);color:var(--amber)}.merchant-strip .failed{background:var(--red-bg);color:var(--red)}.merchant-stages button{justify-self:start}
</style>
