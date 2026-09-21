<script setup>
import {computed,onMounted,onBeforeUnmount,ref} from 'vue'
import {api} from '../api'
import {formatTime} from '../utils'
import ProAutoStages from './ProAutoStages.vue'
import ProAccountLogDialog from './ProAccountLogDialog.vue'
import Pagination from './Pagination.vue'
const props=defineProps({compact:Boolean,defaultPageSize:{type:Number,default:10}})
const status=ref({}),rows=ref([]),total=ref(0),page=ref(1),pageSize=ref(props.defaultPageSize),busy=ref(false),error=ref(''),logs=ref(null),clock=ref(0)
let timer,ticker,disposed=false,base=0,received=performance.now(),loading=false
const countdown=computed(()=>Math.max(0,Math.ceil(base-(clock.value-received)/1000)))
const labels={running:'执行中',completed:'已完成',failed:'失败',skipped:'无需执行',queued:'待启动'}
async function load(){
  if(loading)return;loading=true
  try{
    const v=await api('/api/pro-schedule');if(disposed)return;status.value=v
    received=performance.now();clock.value=received;base=v.next_check_at?Math.max(0,(Date.parse(v.next_check_at)-Date.parse(v.server_time))/1000):0
    if(!props.compact){const result=await api(`/api/pro-schedule/runs?page=${page.value}&page_size=${pageSize.value}`);if(disposed)return;rows.value=result.items||[];total.value=result.total||0}
    error.value=''
  }catch(e){if(!disposed)error.value=e.message}
  finally{loading=false;if(!disposed)timer=setTimeout(load,3000)}
}
async function run(){if(busy.value)return;busy.value=true;error.value='';try{await api('/api/pro-schedule/run',{method:'POST',body:{}});clearTimeout(timer);await load()}catch(e){error.value=e.message}finally{busy.value=false}}
function changePage(v){page.value=v;clearTimeout(timer);load()}
function changeSize(v){pageSize.value=v;changePage(1)}
onMounted(()=>{load();ticker=setInterval(()=>clock.value=performance.now(),500)})
onBeforeUnmount(()=>{disposed=true;clearTimeout(timer);clearInterval(ticker)})
</script>
<template>
<section class="panel pro-schedule-panel">
  <div class="panel-title"><div><h2>{{compact?'定时开通概况':'Pro 执行记录'}}</h2><p>{{status.active?'本批执行中，等待全部账号推送及首次额度刷新完成':status.enabled?`下次检查 ${countdown}s`:'定时开通未启用'}}</p></div><button class="btn primary" type="button" :disabled="busy||!!status.active" @click="run">{{busy?'检查中…':'立即检查并开通'}}</button></div>
  <div class="schedule-stats"><div><small>已开通未合并</small><strong>{{status.opened_unmerged||0}} / {{status.maximum||5}}</strong></div><div><small>开通在途</small><strong>{{status.pending||0}}</strong></div><div><small>银行卡剩余名额</small><strong>{{status.card_capacity||0}}</strong></div><div><small>尚需补充</small><strong>{{status.needed||0}}</strong></div></div>
  <p class="muted">每批最多按并发账号数补充。登录、付款在途也占名额；已付费但尚未推送的账号同样计入未合并数。失败账号请查日志后手动处理。</p>
  <p v-if="error" role="alert" class="danger-text">{{error}}</p>
  <template v-if="!compact">
    <article v-for="run in rows" :key="run.id" class="run-record">
      <div class="run-heading"><strong>{{labels[run.status]||run.status}}</strong><span>{{formatTime(run.started_at)}} · {{run.trigger==='manual'?'手动检查':'定时检查'}}</span></div>
      <p>{{run.message||'正在按顺序启动账号'}}</p>
      <div v-for="task in run.tasks" :key="task.email" class="run-task"><div><strong>{{task.email}}</strong><span>{{labels[task.status]||task.status}}</span><button class="btn ghost compact" @click="logs?.open({email:task.email})">完整日志</button></div><ProAutoStages :state="{steps:task.steps||{},stage:task.stage,status:task.status}"/><p v-if="task.error" class="danger-text">{{task.error}}</p></div>
    </article>
    <p v-if="!rows.length" class="muted">暂无执行记录</p>
    <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize"/>
    <ProAccountLogDialog ref="logs"/>
  </template>
</section>
</template>
<style scoped>
.schedule-stats{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin:16px 0}.schedule-stats>div{padding:14px;background:var(--surface-2);border:1px solid var(--line);border-radius:6px}.schedule-stats small,.schedule-stats strong{display:block}.schedule-stats small{color:var(--muted);margin-bottom:8px}.schedule-stats strong{font-size:20px}.run-record{border-top:1px solid var(--line);padding:16px 0}.run-heading,.run-task>div{display:flex;align-items:center;gap:14px;flex-wrap:wrap}.run-heading span{color:var(--muted);font-size:12px}.run-task{padding:12px;margin-top:12px;background:var(--surface-2)}.run-task strong{overflow-wrap:anywhere}.run-task :deep(.pro-auto-stages){margin:12px 0}.pro-schedule-panel p{font-size:12px;line-height:1.7;overflow-wrap:anywhere}@media(max-width:800px){.schedule-stats{grid-template-columns:repeat(2,minmax(0,1fr))}}
</style>
