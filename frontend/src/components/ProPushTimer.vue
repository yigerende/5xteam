<script setup>
import { computed,onMounted,onBeforeUnmount,ref } from 'vue'
import { formatTime } from '../utils'
import { proPushDuration } from '../proPushDuration'
const props=defineProps({account:Object}),now=ref(Date.now());let timer
onMounted(()=>{timer=setInterval(()=>now.value=Date.now(),1000)})
onBeforeUnmount(()=>clearInterval(timer))
const daily=computed(()=>{const r=props.account.pro_daily_merge,a=props.account.pro_auto||{};return r&&r.automation_id===(a.id||'')&&(!a.id||r.order_id===a.order_id)?r:null})
const duration=computed(()=>proPushDuration(props.account,now.value))
const remaining=computed(()=>Math.ceil((Date.parse(props.account.force_merge_deadline)-now.value)/60000))
</script>
<template><span v-if="!account.pro_push_started_at">未开始</span><template v-else><strong>{{duration.minutes===null?'时长未记录':duration.minutes+' 分钟'}}</strong><small :title="formatTime(account.pro_push_started_at)">{{account.pro_push_started_source==='activation'?'开通时间起算':'首次推送起算（历史/外部账号）'}}</small><small v-if="duration.frozen" :title="duration.end ? formatTime(duration.end) : '历史停止时间未记录'">已停止计时</small><small v-else-if="account.push_status!=='completed'">计时中，等待推送成功</small><small v-else-if="account.force_merge_deadline" :title="formatTime(account.force_merge_deadline)">{{remaining>0?'距超时 '+remaining+' 分钟':'已到阈值，等待调度'}}</small><small v-if="account.pro_auto?.status==='stopped'||account.pro_migration?.paused">已停止 / 暂停</small><small v-if="account.pro_merge_trigger==='push_timeout'">触发：推送超时</small></template><small v-if="daily" :title="formatTime(daily.at)">定点合并：{{daily.completed?'已完成':account.pro_auto?.status==='stopped'||account.pro_migration?.paused?'已停止 / 暂停':daily.attempts?'已触发，见流程进度':'已登记，等待执行'}}</small></template>
<style scoped>small{display:block;font-size:11px;color:var(--muted);margin-top:4px}</style>
