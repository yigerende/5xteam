<script setup>
import {ref,reactive,computed,watch,onMounted,onUnmounted,nextTick} from 'vue'
import {api} from '../api'
import ProTakeoverPanel from './ProTakeoverPanel.vue'
import {createLatestRequest} from '../latestRequest'
import Pagination from './Pagination.vue'
import ProForecastDetails from './ProForecastDetails.vue'
import ProRestockDecisionDetails from './ProRestockDecisionDetails.vue'
import ProRestockTimetable from './ProRestockTimetable.vue'
import ProForceMergeStatus from './ProForceMergeStatus.vue'
import ProExitStatus from './ProExitStatus.vue'
import ProFallbackPanel from './ProFallbackPanel.vue'
import {forecastDetails,concurrencyDetails} from '../restockForecast'
import {restockMonitorStatus} from '../restockMonitor'
import {restockDisplay} from '../restockDisplay'
import {restockTaskActive,restockTaskProgress,restockTaskRows,restockTaskTimeline} from '../restockTasks'
const props=defineProps({defaultPageSize:{type:Number,default:10}})
const pageSize=ref(props.defaultPageSize || 10),recordTotal=ref(0)
const state=ref(null),tab=ref('dashboard'),busy=ref(false),error=ref(''),notice=ref(''),groups=ref([]),search=ref(''),records=ref([]),page=ref(1),detail=ref(null),dialog=ref(null),dirty=ref(false),form=reactive({})
const clock=ref(Date.now()),clockOffset=ref(0),forecastOpen=ref(false)
const windowDraft=ref(3),windowEdited=ref(false),savingWindow=ref(false)
let clockTimer
let timer,disposed=false,returnFocus,stateRevision=0
const stateRequest=createLatestRequest(),recordsRequest=createLatestRequest()
const collectionError=ref(''),recordsError=ref('')
let recordsLoadedAt=0
const detailLoading=ref(false),detailError=ref('')
let detailRequest,detailRevision=0
const d=computed(()=>state.value?.decision||{}),snap=computed(()=>state.value?.snapshot||{}),inherited=computed(()=>state.value?.inherited||{}),tasks=computed(()=>state.value?.tasks||[])
const active=computed(()=>tasks.value.filter(restockTaskActive)),filteredGroups=computed(()=>groups.value.filter(g=>g.name.toLowerCase().includes(search.value.toLowerCase())))
const stoppingTask=ref('')
const canStopTask=t=>!!t?.decision_id&&!t.managed_externally&&restockTaskActive(t)
async function stopTask(t){
 if(stoppingTask.value||!canStopTask(t))return
 if(!window.confirm(`停止 ${t.email} 本次补货？自动补货仍按当前配置判断；已提交的开卡、付款记录保留核验。`))return
 stoppingTask.value=t.id;error.value=''
 try{
  const stopped=await api('/api/pro-restock/tasks/'+encodeURIComponent(t.id)+'/stop',{method:'POST',body:{confirm:true}})
  if(disposed)return
  stateRevision++
  state.value={...state.value,tasks:tasks.value.map(v=>v.id===stopped.id?stopped:v)}
  notice.value=stopped.message
  void refresh()
 }catch(e){if(!disposed)error.value=e.message}
 finally{stoppingTask.value=''}
}
const taskFilter=ref('all'),taskSearch=ref(''),taskPage=ref(1),taskPageSize=ref(props.defaultPageSize||10)
const taskRows=computed(()=>restockTaskRows(tasks.value,taskFilter.value,taskSearch.value))
const visibleTasks=computed(()=>taskRows.value.slice((taskPage.value-1)*taskPageSize.value,taskPage.value*taskPageSize.value))
const taskDetail=computed(()=>restockTaskProgress(detail.value||{}))
watch([taskFilter,taskSearch,taskPageSize],()=>taskPage.value=1)
watch(()=>taskRows.value.length,()=>taskPage.value=Math.min(taskPage.value,Math.max(1,Math.ceil(taskRows.value.length/taskPageSize.value))))
watch(()=>props.defaultPageSize,value=>{taskPageSize.value=value||10;pageSize.value=value||10;page.value=1})
watch(tasks,rows=>{if(detail.value?.email){const task=rows.find(t=>t.id===detail.value.id);if(task)detail.value={...task}}})
const monitorStatus=computed(()=>restockMonitorStatus(state.value||{},clock.value+clockOffset.value))
const monitorUnavailable=computed(()=>monitorStatus.value.unavailable)
const noRestockActive=computed(()=>!!state.value?.no_restock_active)
// A newly collected sample must not make the previous forecast look current.
const liveDecision=computed(()=>({...d.value,snapshot:d.value.snapshot?.id&&d.value.snapshot.id!==snap.value.id?d.value.snapshot:snap.value,config:state.value?.config||d.value.config}))
const displayReport=computed(()=>restockDisplay(state.value||{},clock.value+clockOffset.value))
const concurrencyReport=computed(()=>displayReport.value.concurrency)
const forecastReport=computed(()=>displayReport.value.forecast)
function noRestockWindows(config={}){
 const windows=Array.isArray(config.no_restock_windows)?config.no_restock_windows:[{start:config.no_restock_start??'00:00',end:config.no_restock_end??'07:00'}]
 return windows.map(w=>({start:w?.start??'',end:w?.end??'',allowed_provider:w?.allowed_provider??'',takeover_enabled:!w?.allowed_provider&&!!w?.takeover_enabled,...(w?.high_demand_enabled?{high_demand_enabled:true,high_demand_threshold:w.high_demand_threshold??20,high_demand_confirmations:w.high_demand_confirmations??3}:{})}))
}
function applyConfig(config){Object.assign(form,{capacity_percent:60,daily_ejh_merge_enabled:false,daily_ejh_merge_time:"17:30"},config,{no_restock_windows:noRestockWindows(config)})}
const cardProviderName=p=>({ejh:'EJH',nexovcc:'环球'}[p]||'默认卡台')
function scheduleSummary(config){return noRestockWindows(config).map(w=>`${w.start||'待设置'}～${w.start>w.end?'次日 ':''}${w.end||'待设置'} ${w.allowed_provider?'仅'+cardProviderName(w.allowed_provider):(w.high_demand_enabled?`禁补 · ≥${w.high_demand_threshold} 并发连续 ${w.high_demand_confirmations} 次可放行`:w.takeover_enabled?'完全禁补 · 环球接管':'完全禁补')}`).join('、')||'待设置'}
function addNoRestockWindow(){form.no_restock_windows??=noRestockWindows(form);form.no_restock_windows.push({start:'',end:'',allowed_provider:'',takeover_enabled:false});dirty.value=true}
function removeNoRestockWindow(index){if(form.no_restock_windows?.length>1){form.no_restock_windows.splice(index,1);dirty.value=true}}
function acceptState(data){state.value=data;if(!windowEdited.value)windowDraft.value=data.config?.money_window_minutes??3;clock.value=Date.now();const at=Date.parse(data.server_time);if(Number.isFinite(at))clockOffset.value=at-clock.value}
const fmt=t=>t&&!String(t).startsWith('0001')?new Date(t).toLocaleString('zh-CN',{hour12:false,timeZone:'Asia/Shanghai'}):'—'
const mins=n=>Number.isFinite(n)?Number(n).toFixed(1)+' 分钟':'暂不可估'
const statuses={reserved:'已预留 / 排队',submitting:'启动确认中',waiting_card:'等待原卡 · 自动续跑',running:'全自动执行中',unknown:'原订单待确认',verifying:'已推送待核验',ready:'已就绪',failed:'确认失败',attention:'需要人工核验',cancelled:'未提交已取消',external:'其他来源在途'}
const stages={queued:'等待工作槽位',login:'登录',recharge:'开通 / 付款核验',oauth:'获取新授权',push:'推送 Sub2',quota:'额度巡检',merge:'合并 / 移出',deactivate:'删除账户'}
const sections=[
 {title:'预计供给不足',text:'按剩余美元额度和所选时间窗口的消耗估算；样本不足时显示采样中。',fields:[['time_enabled','启用时间规则','bool'],['money_window_minutes','消耗计算窗口（分钟，默认 3）',1,60],['trigger_minutes','触发水位（分钟）',1,1439],['target_minutes','补后目标（分钟）',2,1440],['early','按实际就绪耗时提前补货','bool']]},
 {title:'强制合并前提前补货',forceMerge:true,text:'独立判断 EJH / 环球已开通、已推送且可调度账号的平均剩余时限。未开启时限和历史未知不参与；与其他规则净缺口取最大值。',fields:[['force_merge_enabled','启用强制合并前提前补货','bool'],['force_merge_minutes','平均剩余时间低于（分钟）',1,525600]]},
 {title:'账号退出前补货',text:'独立触发：逐号预测耗完、合并阈值或推送截止时间，退出后并发容量不足才补。沿用并发触发比例、补后目标和连续确认次数；提前时间严格采用手动设置。',fields:[['exit_enabled','启用账号退出前补货','bool'],['exit_minutes','预计退出前提前补货（分钟）',1,1440],['exit_warning_percent','无可靠速度时额度预警（百分点，0 关闭）',0,20],['exit_refresh_seconds','临期／采样中费用刷新周期（秒）',5,60],['exit_demand_hold_seconds','容量下降后近期需求最多保留（秒）',5,600]]},
 {title:'持续并发压力',tiers:true,text:'关注账号实际占用和账号侧等待，连续新快照确认。',fields:[['concurrency_enabled','启用并发规则','bool'],['trigger_percent','触发利用率（%）',1,100],['target_percent','补后目标利用率（%）',1,99],['confirmations','连续确认次数',1,100],['emergency_queue','紧急账号排队阈值',1,100000]]},
 {title:'补货数量',text:'时间、并发、退出前补货、强制合并接替、最低备用和空分组的净缺口取最大值，不相加；各规则扣除适用在途。同批全部推送并刷新监控后，才评估下一批；本批排队任务按全自动并发继续执行。最低备用用于持续维护可用账号，空分组启动仅在实际空组时新增。',fields:[['min_batch','每批最少',1,100],['max_batch','每批最多',1,100],['allow_small','缺口不足最小批量时允许少补','bool'],['max_inflight','最大在途账号数',1,1000],['reserve','最低备用账号数',0,10000],['empty_group_count','空分组启动补货数量（个，0 关闭）',0,1000]]},
 {title:'运行节奏',text:'采集失败时暂停新增，按并发检查周期自动重试，也可点击“立即重试”。采集成功后恢复规则判断，仍遵守连续确认、冷却和数量限制。',fields:[['check_seconds','并发检查周期（秒）',2,300],['quota_seconds','额度刷新周期（秒）',30,3600],['cooldown_seconds','正常冷却（秒）',1,86400],['emergency_cooldown_seconds','紧急冷却（秒）',1,86400]]}
]
const advanced=[
 {title:'成本与数量保护',text:'预算按每号保守成本预留记账，单位须一致，不直接换算供应商 Credits；不包含其他策略支出。金额限制为 0 时关闭。',fields:[['hour_limit','每小时最多补充',1,10000],['day_limit','每天最多补充',1,10000],['unit_cost','每号成本预留（记账单位）',0,100000000],['hour_budget','每小时预留预算',0,100000000000],['day_budget','每日预留预算',0,100000000000],['pool_limit','监控池硬上限',1,10000],['failure_limit','连续异常熔断阈值',1,100],['fault_percent','账号异常暂停比例（%）',1,100]]},
 {title:'数据与到货保护',text:'不足历史样本时使用保守回退耗时；付款未知保留原订单、预算和在途。',fields:[['lead_minutes','就绪耗时回退（分钟）',1,1440],['lead_samples','采用历史耗时所需样本',2,1000],['margin_minutes','到货安全余量（分钟）',0,120],['forecast_minutes','时间补货规划上限（分钟）',2,1440],['fresh_seconds','并发快照有效期（秒）',2,3600],['retention_days','明细保留天数',1,90]]}
]
async function refresh(initial=false,background=false){
 if(disposed||(background&&busy.value))return
 const revision=stateRevision
 try{await stateRequest.run(signal=>api('/api/pro-restock',{signal,timeoutMs:15000}),async data=>{
  if(disposed||revision!==stateRevision)return
  const initialize=initial||!state.value
  acceptState(data)
  error.value=''
  if(!monitorUnavailable.value)collectionError.value=''
  if(initialize)applyConfig(data.config)
  // History never delays status rendering or keeps a saved action busy.
  if(!background||Date.now()-recordsLoadedAt>=30000)void loadRecords(true).catch(e=>{if(!disposed)recordsError.value=e.message})
 },{background})}catch(e){if(!disposed&&revision===stateRevision)error.value=e.message}
}
async function loadRecords(background=false){
 const requestedPage=page.value
 await recordsRequest.run(signal=>api('/api/pro-restock/records?page='+requestedPage+'&page_size='+pageSize.value,{signal,timeoutMs:15000}),async data=>{recordsLoadedAt=Date.now();recordsError.value='';records.value=data.items||[];recordTotal.value=data.total||0;const lastPage=Math.max(1,Math.ceil(recordTotal.value/pageSize.value));if(page.value>lastPage){page.value=lastPage;await loadRecords()}},{background})
}
function readRecords(){return loadRecords().catch(e=>{if(!disposed)recordsError.value=e.message})}
function setRecordPage(value){page.value=value;void readRecords()}
function setRecordPageSize(value){pageSize.value=value;setRecordPage(1)}
async function action(fn){if(busy.value||disposed)return;busy.value=true;stateRevision++;error.value='';collectionError.value='';notice.value='';try{await fn()}catch(e){if(!disposed)error.value=e.message}finally{busy.value=false}}
function validateWindow(value){if(!Number.isInteger(value)||value<1||value>60)throw new Error('消耗计算窗口应为 1～60 分钟的整数。')}
async function saveWindow(){await action(async()=>{
 validateWindow(windowDraft.value)
 if(!state.value?.config)return
 savingWindow.value=true
 try{
  const c=await api('/api/pro-restock/config',{method:'PUT',body:{...state.value.config,money_window_minutes:windowDraft.value}})
  if(disposed)return
  state.value={...state.value,config:c};windowEdited.value=false;windowDraft.value=c.money_window_minutes??3
  // A dashboard-only save must not overwrite unrelated unsaved settings.
  if(!dirty.value)applyConfig(c)
  void refresh()
  notice.value=`计算窗口已保存为 ${c.money_window_minutes} 分钟；样本不足时显示采样中。`
 }finally{savingWindow.value=false}
})}
async function evaluate(){await action(async()=>{
 const data=await api('/api/pro-restock/evaluate',{method:'POST'})
 if(disposed)return
 acceptState(data)
 if(monitorUnavailable.value){
  collectionError.value='本次采集未恢复，新增补货继续暂停：'+(data.snapshot?.error||'监控数据缺失或过期。')
 }else if(!data.config.enabled){
  notice.value='采集成功，已更新评估；新增仍处于手动暂停状态。'
 }else if(data.no_restock_active){
  notice.value='采集成功；'+data.no_restock_reason
 }else if(data.config.mode==='auto'){
  notice.value='采集成功，已恢复规则判断；满足补货条件和限制时才启动全自动 Pro。'
 }else{
  notice.value='采集成功，已恢复观察评估；观察模式不会启动全自动 Pro。'
 }
 void readRecords()
})}
async function save(){await action(async()=>{
 if(form.daily_ejh_merge_enabled&&!/^([01]\d|2[0-3]):[0-5]\d$/.test(form.daily_ejh_merge_time||''))throw new Error('每日 EJH 定点合并时间必须为 HH:mm（00:00～23:59）。')
 const windows=noRestockWindows(form)
 if(form.no_restock_enabled){
  const validTime=/^([01]\d|2[0-3]):[0-5]\d$/
  if(!windows.length)throw new Error('启用每日禁补时段时至少设置一个时间段。')
  for(const [index,w] of windows.entries()){
   if(!validTime.test(w.start)||!validTime.test(w.end))throw new Error(`每日禁补时段 ${index+1}：开始和结束时间必须为 HH:mm（00:00～23:59）。`)
   if(w.high_demand_enabled&&(w.allowed_provider||!Number.isInteger(w.high_demand_threshold)||w.high_demand_threshold<1||w.high_demand_threshold>1000000||!Number.isInteger(w.high_demand_confirmations)||w.high_demand_confirmations<1||w.high_demand_confirmations>1000))throw new Error(`每日禁补时段 ${index+1}：高并发放行阈值应为 1～1000000，连续次数应为 1～1000，且只能用于完全禁补。`)
   if(w.start===w.end)throw new Error(`每日禁补时段 ${index+1}：开始和结束时间不能相同；全天停止请关闭自动补货总开关。`)
   if(!['','ejh','nexovcc'].includes(w.allowed_provider))throw new Error(`每日禁补时段 ${index+1}：允许卡台无效。`)
  }
  const inside=(w,t)=>w.start<w.end?t>=w.start&&t<w.end:t>=w.start||t<w.end
  for(let i=0;i<windows.length;i++)for(let j=i+1;j<windows.length;j++){
   const a=windows[i],b=windows[j]
   if(a.allowed_provider&&b.allowed_provider&&a.allowed_provider!==b.allowed_provider&&(inside(a,b.start)||inside(b,a.start)))throw new Error(`每日禁补时段 ${i+1} 和 ${j+1} 重叠，不能允许不同卡台。`)
  }
 }
 if(!Number.isInteger(form.capacity_percent??60)||(form.capacity_percent??60)<1||(form.capacity_percent??60)>100)throw new Error('总并发容量使用比例应为 1～100% 的整数。')
 validateWindow(form.money_window_minutes??3)
 const c=await api('/api/pro-restock/config',{method:'PUT',body:{...form,no_restock_windows:windows,no_restock_start:windows[0]?.start??'',no_restock_end:windows[0]?.end??''}});if(disposed)return;state.value={...state.value,config:c};applyConfig(c);dirty.value=false;windowEdited.value=false;windowDraft.value=c.money_window_minutes??3;void refresh();notice.value='配置 v'+c.version+' 已保存。'
})}
async function pause(){await action(async()=>{const c=await api('/api/pro-restock/config',{method:'PUT',body:{...state.value.config,enabled:!state.value.config.enabled}});if(disposed)return;state.value={...state.value,config:c};applyConfig(c);dirty.value=false;void refresh(true);notice.value=c.enabled?'后台监控已启用':'新增已暂停，已提交任务继续跟踪。'})}
async function loadGroups(){await action(async()=>{groups.value=await api('/api/pro-restock/groups');notice.value='已读取 Pro 推送连接的 Sub2 分组。'})}
async function resetBreaker(){await action(async()=>{const data=await api('/api/pro-restock/reset-breaker',{method:'POST'});if(disposed)return;acceptState(data);notice.value='熔断已重置，原订单和在途保留。'})}
async function changeTab(value){tab.value=value;if(value==='records')await readRecords()}
async function showDetail(item){
 if(disposed)return
 detailRequest?.dispose()
 const revision=++detailRevision
 detailRequest=createLatestRequest(loading=>{detailLoading.value=loading})
 detailError.value=''
 returnFocus=document.activeElement
 detail.value={...item,related_tasks:[]}
 await nextTick()
 if(disposed||revision!==detailRevision)return
 if(!dialog.value?.open)dialog.value?.showModal()
 dialog.value?.querySelector?.('.detail-body')?.scrollTo({top:0})
 await loadDetailTasks()
}
async function loadDetailTasks(){
 const item=detail.value,revision=detailRevision,request=detailRequest
 if(disposed||!item?.snapshot||!item.id||!request)return
 detailError.value=''
 try{await request.run(signal=>api('/api/pro-restock/records/'+encodeURIComponent(item.id)+'/tasks',{signal,timeoutMs:15000}),data=>{
  if(!disposed&&revision===detailRevision)detail.value.related_tasks=data
 })}catch(e){if(!disposed&&revision===detailRevision)detailError.value=e.message}
}
function closeDetail(){detailRevision++;detailRequest?.dispose();dialog.value?.close();detail.value=null;detailError.value='';returnFocus?.focus()}
const chart=computed(()=>{const points=[...records.value].reverse().slice(-30);const top=Math.max(1,...points.map(r=>Math.max(r.capacity,r.used+r.waiting)));function path(key){let value='',connected=false;points.forEach((r,i)=>{if(!r.snapshot?.valid||!r.snapshot?.load_valid){connected=false;return}const n=key==='capacity'?r.capacity:r.used+r.waiting;value+=(connected?' L':' M')+(20+i*560/Math.max(1,points.length-1))+','+(155-n*125/top);connected=true});return value}return {empty:!points.some(r=>r.snapshot?.valid&&r.snapshot?.load_valid),demand:path('demand'),capacity:path('capacity')}})
onMounted(async()=>{clockTimer=setInterval(()=>{clock.value=Date.now()},1000);await refresh(true);if(!disposed)timer=setInterval(()=>{if(!document.hidden)void refresh(false,true)},5000)})
onUnmounted(()=>{disposed=true;stateRevision++;clearInterval(timer);clearInterval(clockTimer);stateRequest.dispose();recordsRequest.dispose();detailRevision++;detailRequest?.dispose()})
</script>
<template>
<div class="restock">
 <header class="page-head"><div><p class="eyebrow">SPACE / PRO</p><h1>Pro 自动补货</h1><p>按供给时间与并发压力判断，分批启动现有全自动 Pro 流程。</p></div><div class="actions"><button :disabled="busy||!state" @click="evaluate">{{monitorUnavailable?'立即重试':'立即评估'}}</button><button class="primary" :disabled="busy||!state" @click="pause">{{state?.config?.enabled?'暂停新增':'启用监控'}}</button></div></header>
 <nav class="tabs" aria-label="Pro 自动补货页面"><button v-for="(name,key) in {dashboard:'监控大屏',settings:'补货配置',fallback:'兜底配置',records:'决策与记录'}" :key="key" :class="{selected:tab===key}" :aria-current="tab===key?'page':undefined" @click="changeTab(key)">{{name}}</button></nav>
 <p v-if="error||collectionError" role="alert" class="notice danger">{{error||collectionError}}</p><p v-if="notice" role="status" class="notice">{{notice}}</p>
 <p v-if="!state" class="empty">{{error?'补货状态暂未读取成功':'正在读取补货状态…'}} <button v-if="error" @click="refresh(true)">重新读取</button></p>
 <p v-if="recordsError" class="notice warn">历史记录读取失败：{{recordsError}} <button @click="readRecords">重试记录</button></p>
 <template v-if="state">
 <ProTakeoverPanel :data="state.takeover" :push-settings="state.inherited?.sub2" :monitor-group="state.config?.group_id" compact/>
 <div class="runbar"><span><b>{{!state.config.enabled?'新增已手动暂停':noRestockActive?'每日禁补时段 · 暂停新增':monitorUnavailable?monitorStatus.title+' · 暂停新增':monitorStatus.running?'后台监控更新中':'后台监控已启用'}}</b> · {{state.config.mode==='auto'?'自动执行':'观察模式'}} · 配置 v{{state.config.version}}</span><span>监控组 #{{state.config.group_id||'未选择'}} · 快照 {{fmt(snap.at)}}<template v-if="monitorStatus.age!==null"> · {{Math.floor(monitorStatus.age)}} 秒前</template></span></div>
 <ProRestockTimetable :tasks="tasks" :timetable="state.purchase_timetable" :config="state.config" :demand="state.no_restock_demand" :purchase="state.current_purchase" :decision="state.decision" :now="clock+clockOffset"/>
 <p v-if="inherited.scheduled_enabled" class="notice warn">原定时任务仍已启用，会按原固定数量策略开通。同池同时使用时，新补货不能阻止原任务补号。</p>
 <div id="pro-fallback-config-slot" v-show="tab==='fallback'"></div>
 <section v-show="tab==='dashboard'">
  <div v-if="monitorUnavailable" class="notice warn"><b>{{monitorStatus.title}}</b> · {{monitorStatus.message}}<p v-if="monitorStatus.lastSuccess">最近成功快照：{{fmt(monitorStatus.lastSuccess)}}<template v-if="monitorStatus.totalSeconds"> · 上轮用时 {{monitorStatus.totalSeconds.toFixed(1)}} 秒</template></p><p v-if="state.config.enabled">新增补货暂时停止，已提交的全自动 Pro 流程继续执行。<template v-if="monitorStatus.running">本轮正在更新，完成后自动重新判断。</template><template v-else>后台自动重试<template v-if="monitorStatus.retry!==null">（{{monitorStatus.retry>0?monitorStatus.retry+' 秒后':'等待调度'}}）</template>，也可点击“立即重试”。</template></p><p v-else>可点击“立即重试”更新数据；成功后仍保留手动暂停状态。</p></div>
  <p v-else-if="monitorStatus.running" class="notice" role="status">{{monitorStatus.message}}</p>
  <p v-if="displayReport.historical" class="notice warn">以下保留上次有效数据供查看，不作为当前补货依据。并发采样：{{fmt(displayReport.sampleAt)}}；预测采样：{{fmt(displayReport.decisionAt)}}。后台继续更新。</p>
  <div class="kpis"><article><span>当前可用账号</span><strong>{{displayReport.usable??'—'}}<small> / {{snap.accounts?.length??0}}</small></strong><p>按 Sub2 实际组归属与可调度状态</p></article><article><span>并发容量</span><strong>{{concurrencyReport.percent}}</strong><p>{{concurrencyReport.used??'—'}} 已用 / {{concurrencyReport.capacity??'—'}} 容量</p><p class="capacity-basis">原始 {{concurrencyReport.reportedCapacity??'—'}} × {{concurrencyReport.capacityPercent}}%</p><p class="kpi-rule" :class="{inactive:!state.config.concurrency_enabled}" title="有效容量 × 触发比例向下取整；已用达到该数量或账号侧出现等待，连续确认后评估补货">{{state.config.concurrency_enabled ? `≥ ${concurrencyReport.triggerCount??'—'} 并发（${concurrencyReport.triggerPercent}%）· 连续 ${state.config.confirmations} 次` : '规则已关闭'}}</p><p v-if="d.waiting>0">等待 {{d.waiting}}</p></article><article><span>预计支撑时间</span><strong class="medium">{{forecastReport.supply}}</strong><p class="kpi-rule" :class="{inactive:!state.config.time_enabled}" :title="state.config.early ? '已开启按就绪耗时提前补货，实际提前量见预测详情' : '按已保存的时间阈值评估补货'">{{state.config.time_enabled ? `≤ ${state.config.trigger_minutes} 分钟触发${state.config.early ? ' · 可提前' : ''}` : '规则已关闭'}}</p><p class="money-summary">{{forecastReport.summary}}</p><button @click="forecastOpen=!forecastOpen" :aria-expanded="forecastOpen">{{forecastOpen?'收起预测详情':'查看预测详情'}}</button></article><article><ProForceMergeStatus :decision="d" :config="state.config" :unavailable="displayReport.decisionUnavailable" :now="clock+clockOffset" compact/></article><article><ProExitStatus :decision="d" :config="state.config" :unavailable="displayReport.decisionUnavailable" compact @details="showDetail(d)"/></article><article><span>正在补充</span><strong>{{active.length}}<small> / {{state.config.max_inflight}}</small></strong><p>{{d.batch_wait?.message || "整批推送并刷新监控后，再评估下一批"}}</p></article><div id="pro-fallback-card-slot"></div></div>
  <article class="panel rate-panel"><div><h2>当前每分钟消耗</h2><strong>{{forecastReport.rateText}}</strong><p>最近 {{forecastReport.windowMinutes}} 分钟 · 账号费用</p><form class="rate-window-form" @submit.prevent="saveWindow"><label>计算窗口<input v-model.number="windowDraft" type="number" min="1" max="60" step="1" required aria-label="消耗计算窗口（分钟）" title="1～60 分钟，默认 3 分钟" :disabled="busy" @input="windowEdited=true"/>分钟</label><button type="submit" :disabled="busy||windowDraft===(state.config.money_window_minutes??3)">{{savingWindow?'保存中…':'保存'}}</button></form></div><button @click="forecastOpen=!forecastOpen" :aria-expanded="forecastOpen">{{forecastOpen?'收起消耗明细':'查看消耗明细'}}</button></article>
  <ProForecastDetails v-if="forecastOpen" :decision="displayReport.forecastDecision" :now="displayReport.forecastNow"/>
  <div class="two-grid"><article class="panel recommendation"><div class="panel-head"><h2>本轮补货建议</h2><span class="badge">{{state.config.mode==='auto'?'自动执行':'观察模式'}}</span></div><div class="recommend">{{d.suggested||0}}<span> 个</span></div><p>{{d.reason||'等待第一轮评估'}}</p><div class="line"><span>时间 / 并发 / 退出 / 备用 / 空组 / 接替净缺口</span><b>{{d.time_need||0}} / {{d.concurrency_need||0}} / {{d.exit_need||0}} / {{d.reserve_need||0}} / {{d.empty_group_need||0}} / {{d.force_merge_need||0}}</b></div><div class="line"><span>本轮已预留</span><b>{{d.started||0}} 个</b></div><div class="line"><span>连续压力确认</span><b>{{d.confirmations||0}} / {{state.config.confirmations}}</b></div><button @click="showDetail(d)">查看本轮详情</button></article>
  <article class="panel"><h2>补货触发规则</h2><div class="rule"><h3>01 / 预计供给不足</h3><p>配置水位 {{state.config.trigger_minutes}} 分钟；实际提前量 {{mins(d.trigger_minutes)}}；补后目标 {{mins(d.target_minutes)}}。</p></div><div class="rule"><h3>02 / 持续并发压力</h3><p>按原始容量的 {{state.config.capacity_percent??60}}% 计算；占用达到 {{concurrencyReport.triggerCount??'—'}}（{{concurrencyReport.triggerPercent}}% 向下取整），连续 {{state.config.confirmations}} 次确认；补后希望降到 {{concurrencyReport.targetPercent}}%。</p></div><div class="rule"><h3>03 / 账号退出前补货</h3><p>{{state.config.exit_enabled?'已开启':'未开启'}} · 提前 {{state.config.exit_minutes??5}} 分钟检查；沿用 {{concurrencyReport.triggerPercent}}% 触发、{{concurrencyReport.targetPercent}}% 目标，逐号预测退出后的容量。</p></div><div class="rule"><h3>04 / 空分组启动</h3><p>实际组成员为 0 时，启动数量 {{state.config.empty_group_count||0}} 个（0 关闭），扣除同组全部在途。已预留本批继续执行。</p></div><div class="rule"><h3>05 / 最低备用</h3><p>持续维护 {{state.config.reserve}} 个可用账号（0 关闭），独立于空分组启动。</p></div><div class="rule"><h3>06 / 强制合并前提前补货</h3><p>有效 EJH / 环球账号平均剩余低于 {{state.config.force_merge_minutes||15}} 分钟时，按临期账号净接替数补货；未开启时限和历史未知排除。</p></div></article></div>
  <div class="quality-grid"><article class="panel"><span>新任务预计就绪（含排队）</span><strong>{{mins(d.lead_minutes)}}</strong><p>登录 → 开通 → 新授权 → 推送 → 核验</p></article><article class="panel"><span>开通工作并发 / 单号请求并发</span><strong>{{inherited.concurrency}} / {{inherited.sub2?.account_concurrency}}</strong><p>继承 Pro 管理，两个并发含义不同</p></article><article class="panel"><span>本小时 / 今日补货数量</span><strong>{{state.limits.hour_count}} / {{state.limits.day_count}}</strong><p>成本预留 {{state.limits.day_cost}}；未知结果继续占用</p></article></div>
  <p v-for="warning in d.warnings||[]" :key="warning" class="notice warn">{{warning}}</p>
  <div class="two-grid"><article class="panel"><div class="panel-head"><h2>需求与承载趋势</h2><span class="muted">最近保存快照</span></div><p v-if="chart.empty" class="empty">积累有效快照后显示趋势</p><svg v-else viewBox="0 0 600 180" role="img" aria-label="需求与有效容量趋势"><path d="M20 155H580" stroke="#d8e5e5"/><path :d="chart.capacity" fill="none" stroke="#97afb2" stroke-width="2" stroke-dasharray="5 4"/><path :d="chart.demand" fill="none" stroke="#008786" stroke-width="3"/></svg><p class="muted">绿色：占用＋账号等待 · 灰色：有效容量 · 数据异常处断线</p></article><article class="panel"><h2>接入现有全自动 Pro</h2><div class="flow"><span v-for="step in ['领取账号 / 卡片','登录','开通 / 确认付款','获取新授权','推送 Sub2','核验就绪']" :key="step">{{step}}</span></div><p>单号就绪后立即更新容量；额度巡检、原推送等待时限、合并与移出继续由原流程执行。</p><p>新增由新规则决定，卡片占用、付款防重和工作并发与手动及原定时任务共用。</p></article></div>
  <article class="panel task-panel"><div class="panel-head"><h2>逐账号执行进度</h2><span class="muted">{{active.length}} 个在途 · {{tasks.length}} 条近期记录</span></div>
   <div class="task-toolbar"><div class="task-filters" aria-label="任务状态筛选"><button v-for="(name,key) in {all:'全部',active:'在途',ready:'成功',ended:'失败 / 取消'}" :key="key" :class="{selected:taskFilter===key}" :aria-pressed="taskFilter===key" @click="taskFilter=key">{{name}}</button></div><input v-model="taskSearch" type="search" aria-label="搜索执行账号或订单" placeholder="搜索账号 / 订单"/></div>
   <p class="muted task-hint">在途优先，其余按任务创建时间从新到旧。成功时间为推送后监控核验完成时间；均为北京时间。</p>
   <div class="tablewrap"><table class="task-table"><thead><tr><th>账号 / 订单</th><th>补货状态</th><th>执行时间</th><th>成功时间</th><th>说明</th><th>明细</th></tr></thead><tbody><tr v-for="t in visibleTasks" :key="t.id"><td class="task-account">{{t.email}}<small v-if="t.card_provider">{{cardProviderName(t.card_provider)}}</small><small class="task-order" :title="t.order_id">{{t.order_id||'尚未生成订单'}}</small></td><td><span class="task-status" :class="t.status">{{t.progress.status}}</span><small>{{t.progress.stage}}</small></td><td class="task-time">{{t.progress.started?fmt(t.progress.started):t.progress.startNote}}<small v-if="!t.progress.started&&t.progress.submitted">提交 {{fmt(t.progress.submitted)}}</small><small>创建 {{fmt(t.created_at)}}</small></td><td class="task-time">{{t.progress.success?fmt(t.progress.success):t.progress.successNote}}<small v-if="t.progress.eta">预计 {{fmt(t.progress.eta)}}</small><small v-else-if="!t.progress.success&&t.progress.pushed">推送 {{fmt(t.progress.pushed)}}</small></td><td class="task-message">{{t.progress.message}}<small v-if="t.next_card_check_at">下次检查：{{fmt(t.next_card_check_at)}}</small></td><td><div class="task-actions"><button @click="showDetail(t)">查看</button><button v-if="canStopTask(t)" :disabled="!!stoppingTask" @click="stopTask(t)">{{stoppingTask===t.id?'停止中…':'停止本次'}}</button></div></td></tr><tr v-if="!visibleTasks.length"><td colspan="6" class="empty">{{taskFilter==='active'?'当前没有在途任务':'暂无匹配的补货任务'}}</td></tr></tbody></table></div>
   <Pagination :page="taskPage" :page-size="taskPageSize" :total="taskRows.length" @update:page="taskPage=$event" @update:page-size="taskPageSize=$event"/>
  </article>
 </section>
 <Teleport :to="tab==='dashboard'?'#pro-fallback-card-slot':'#pro-fallback-config-slot'"><ProFallbackPanel :compact="tab==='dashboard'" :settings="tab==='fallback'"/></Teleport>
 <section v-if="tab==='settings'"><form @submit.prevent="save" @input="dirty=true"><div class="two-grid"><article class="panel"><h2>基本设置</h2><label class="check"><input v-model="form.enabled" type="checkbox">启用 Pro 自动补货</label><label>运行模式<select v-model="form.mode"><option value="observe">观察模式 — 只记录建议</option><option value="auto">自动执行 — 达标后启动全自动 Pro</option></select></label><label>唯一监控分组<input v-model="search" type="search" placeholder="搜索 Sub2 分组"><select aria-label="唯一监控分组" v-model.number="form.group_id"><option :value="0">请选择分组</option><option v-if="form.group_id&&!groups.some(g=>g.id===form.group_id)" :value="form.group_id">已保存分组 #{{form.group_id}}</option><option v-for="g in filteredGroups" :key="g.id" :value="g.id">{{g.name}} · #{{g.id}}</option></select></label><button type="button" :disabled="busy" @click="loadGroups">读取 / 刷新分组</button><p v-if="form.group_id&&!(inherited.sub2?.group_ids||[]).includes(form.group_id)" class="danger-text">该监控组不在当前推送目标中，无法启用。</p><label>总并发容量使用比例（%）<input v-model.number="form.capacity_percent" aria-label="总并发容量使用比例（%）" type="number" min="1" max="100" step="1" required></label><p class="muted">默认 60%；有效容量＝Sub2 总容量 × 此比例。并发补货与退出前补货统一按此容量判断。</p><p class="muted">保存启用配置前检查连接、分组和监控数据。仅选择一个监控组。</p></article>
 <article class="panel"><h2>继承 Pro 推送设置</h2><div class="line"><span>当前下游</span><b>{{inherited.provider}}</b></div><div class="line"><span>Sub2 地址</span><b>{{inherited.sub2?.url||'未配置'}}</b></div><div class="line"><span>认证</span><b>{{inherited.sub2?.password_present?'已保存':'未配置'}}</b></div><div class="line"><span>目标分组</span><b>{{inherited.sub2?.group_names?.join('、')||inherited.sub2?.group_ids?.join('、')||'未配置'}}</b></div><div class="line"><span>模型 / 代理 / 优先级</span><b>{{inherited.sub2?.models?.join('、')||'默认模型'}} / {{inherited.sub2?.proxy_ids?.join('、')||'默认'}} / {{inherited.sub2?.priority}}</b></div><div class="line"><span>单账号请求并发</span><b>{{inherited.sub2?.account_concurrency}}</b></div><div class="line"><span>开通工作并发</span><b>{{inherited.concurrency}}</b></div><div class="line"><span>原推送等待时限</span><b>{{inherited.force_merge_enabled?inherited.force_merge_minutes+' 分钟':'未启用强制时限'}}</b></div><p class="muted">以上值在 Pro 管理中设置，本页不保存第二套推送配置。尚未推送的账号使用最新保存的推送配置；已提交创建请求的重试和已推送账号保留实际连接。</p></article>
 <article v-for="section in sections" :key="section.title" class="panel"><h2>{{section.title}}</h2><p class="muted">{{section.text}}</p><ProForceMergeStatus v-if="section.forceMerge" :decision="d" :config="state.config" :unavailable="monitorUnavailable" :now="clock+clockOffset"/><div class="fields"><label v-for="field in section.fields" :key="field[0]" :class="{check:field[2]==='bool'}"><input v-if="field[2]==='bool'" v-model="form[field[0]]" type="checkbox">{{field[1]}}<input v-if="field[2]!=='bool'" v-model.number="form[field[0]]" type="number" :min="field[2]" :max="field[3]" step="1" required></label></div><div v-if="section.tiers" class="panel"><label class="check"><input v-model="form.capacity_tiers_enabled" type="checkbox" @change="form.capacity_tiers?.length||(form.capacity_tiers=[{min_capacity:0,trigger_percent:80,target_percent:75},{min_capacity:120,trigger_percent:85,target_percent:80}])">按总并发容量分档</label><p class="muted">按有效可用容量（原始容量 × 使用比例）选择最大匹配下限；账号退出前按退出后容量选档。未启用沿用上方固定阈值。</p><div v-for="(tier,i) in form.capacity_tiers||[]" :key="i" class="fields"><label>容量 ≥<input v-model.number="tier.min_capacity" type="number" min="0" max="10000000" step="1" required></label><label>触发利用率 %<input v-model.number="tier.trigger_percent" type="number" min="1" max="100" step="1" required></label><label>补后目标 %<input v-model.number="tier.target_percent" type="number" min="1" :max="tier.trigger_percent-1" step="1" required></label><button type="button" @click="form.capacity_tiers.splice(i,1)">删除此档</button></div><button type="button" @click="(form.capacity_tiers??=[]).push({min_capacity:240,trigger_percent:90,target_percent:85})">增加容量档位</button></div></article>
 <article class="panel schedule-panel"><div class="panel-head"><h2>每日禁补时段</h2><button type="button" :disabled="busy" @click="addNoRestockWindow">＋ 添加时段</button></div><p class="muted">每天按北京时间生效，可设置多个时段，支持跨午夜；每段可选择允许补货的卡台；完全禁补时可单独勾选“环球接管”。包含开始，不包含结束。</p><label class="check"><input v-model="form.no_restock_enabled" type="checkbox">启用每日禁补时段</label>
  <div v-for="(window,index) in form.no_restock_windows" :key="index" class="schedule-row">
   <span class="schedule-number">{{index+1}}</span><label>开始时间<input v-model="window.start" :aria-label="`禁补时段 ${index+1} 开始时间`" type="time" step="60" :disabled="!form.no_restock_enabled||busy" :required="form.no_restock_enabled"></label><label>结束时间<input v-model="window.end" :aria-label="`禁补时段 ${index+1} 结束时间`" type="time" step="60" :disabled="!form.no_restock_enabled||busy" :required="form.no_restock_enabled"></label><label>允许卡台<select v-model="window.allowed_provider" :aria-label="`禁补时段 ${index+1} 允许卡台`" @change="window.takeover_enabled=false;window.high_demand_enabled=false" :disabled="!form.no_restock_enabled||busy"><option value="">完全禁补</option><option value="ejh">EJH</option><option value="nexovcc">环球</option></select></label><label class="check schedule-takeover"><input v-model="window.takeover_enabled" type="checkbox" :aria-label="`禁补时段 ${index+1} 环球接管`" :disabled="!form.no_restock_enabled||busy||!!window.allowed_provider">环球接管</label><button type="button" :aria-label="`删除禁补时段 ${index+1}`" :disabled="busy||form.no_restock_windows.length<=1" @click="removeNoRestockWindow(index)">删除</button>
   <div v-if="!window.allowed_provider" class="demand-config"><label class="check"><input v-model="window.high_demand_enabled" type="checkbox" :disabled="!form.no_restock_enabled||busy" @change="window.high_demand_threshold??=20;window.high_demand_confirmations??=3">高并发允许补货</label><template v-if="window.high_demand_enabled"><label>总在线并发 ≥<input v-model.number="window.high_demand_threshold" type="number" min="1" max="1000000" required :disabled="!form.no_restock_enabled||busy"></label><label>连续达到次数<input v-model.number="window.high_demand_confirmations" type="number" min="1" max="1000" required :disabled="!form.no_restock_enabled||busy"></label><small>每 {{form.check_seconds||5}} 秒检查 · 监控账号＋已勾选兜底账号</small></template></div>
  </div>
  <p class="muted">高并发放行在禁补期间持续检测，沿用“运行节奏”的并发检查周期。统计监控分组账号＋已勾选兜底账号，按账号去重；连续达标后按正常配置补货，低于阈值重新禁补。重叠时段须全部允许，未开启此功能的完全禁补优先。兜底始终按主组空组开启、有号关闭。</p>
  <p v-if="form.no_restock_enabled" class="notice">每天 {{scheduleSummary(form)}}。</p><p class="muted">允许卡台的时段仍需达到补货条件，仅用所选卡台及其补卡配置；时段外使用 Pro 管理的默认卡台。“环球接管”需配置下方跨项目连接；未勾选不接管，结束后撤回临时分组。重叠时未勾选接管的完全禁补优先，不同卡台的允许时段不能重叠。已启动流程继续使用原卡，仅影响自动补货，不影响每日 EJH 定点合并。</p></article>
 <article class="panel daily-merge-panel"><h2>每日 EJH 定点合并</h2><label class="check"><input v-model="form.daily_ejh_merge_enabled" type="checkbox">启用每日定点强制合并</label><label>执行时间（北京时间）<input v-model="form.daily_ejh_merge_time" aria-label="每日 EJH 定点合并时间" type="time" step="60" :disabled="!form.daily_ejh_merge_enabled||busy" :required="form.daily_ejh_merge_enabled"></label><p class="muted">每天到点只登记一次当时已开通、仍在运行的 EJH Pro（含未开启推送时限的账号）；之后新开的不加入当天任务。独立于补货总开关与观察模式。</p><p class="muted">跳过额度等待，按 Pro 全自动配置选择执行空间合并、删除账户或 API 商户；沿用并发限制，合并同母号串行；安全失败按原重试配置处理，结果待确认或人工停止的不自动重试。</p><p v-if="state.daily_ejh_merge_run" class="notice">最近登记：{{fmt(state.daily_ejh_merge_run.at)}} · {{state.daily_ejh_merge_run.count}} 个账号<br><small>逐账号进度见 Pro 管理。当天改时间不会再次登记。</small></p></article>
 </div><article class="panel"><h2>死号后的补货策略</h2><label><input v-model="form.dead_account_pause" type="checkbox">检测到明确死号后暂停自动补货</label><p class="muted">默认关闭：移出供给后按正常规则补货。开启：发现新死号时关闭自动补货，须手动恢复；代理错误、普通 401 不算死号。</p></article><details class="panel advanced"><summary>高级配置 · 成本、数据与到货保护</summary><div class="two-grid"><article v-for="section in advanced" :key="section.title"><h3>{{section.title}}</h3><p class="muted">{{section.text}}</p><div class="fields"><label v-for="field in section.fields" :key="field[0]">{{field[1]}}<input v-model.number="form[field[0]]" type="number" :min="field[2]" :max="field[3]" step="1" required></label></div></article></div><button type="button" :disabled="busy" @click="resetBreaker">处理故障后重置熔断</button></details><div class="savebar"><span>{{dirty?'有未保存修改':'参数由后台持久化保存'}}</span><button class="primary" :disabled="busy" type="submit">{{busy?'检查并保存…':'保存配置'}}</button></div></form><ProTakeoverPanel :data="state.takeover" :push-settings="state.inherited?.sub2" :monitor-group="state.config?.group_id" @saved="refresh"/></section>
 <section v-if="tab==='records'"><article class="panel"><div class="panel-head"><h2>补货决策与执行记录</h2><span class="badge">持久化记录 · 第 {{page}} 页</span></div><p class="muted">点击“查看”打开本轮详情，查看当时为什么触发、为什么补这个数量，以及关联任务的执行进度。记录按配置保留 {{state.config.retention_days||7}} 天。</p><div class="tablewrap"><table><thead><tr><th>时间 / 配置</th><th>事件</th><th>结果</th><th>在途</th><th>原因</th><th>明细</th></tr></thead><tbody><tr v-for="r in records" :key="r.id"><td>{{fmt(r.at)}}<small>v{{r.version}}</small></td><td>{{r.snapshot?.id||r.trace?'补货决策':'配置 / 操作'}}</td><td>{{r.started?'已预留 '+r.started+' 个':r.snapshot?.id||r.trace?'建议 '+(r.suggested||0)+' 个':'未执行'}}</td><td>{{r.inflight??'—'}}</td><td>{{r.reason}}<small v-if="r.card_provider">{{cardProviderName(r.card_provider)}}<template v-if="r.card_schedule?.active"> · {{r.card_schedule.summary}}</template></small></td><td><button @click="showDetail(r)">查看</button></td></tr><tr v-if="!records.length"><td colspan="6" class="empty">暂无记录，启用观察或立即评估后显示</td></tr></tbody></table></div><Pagination :page="page" :page-size="pageSize" :total="recordTotal" :disabled="busy" @update:page="setRecordPage" @update:page-size="setRecordPageSize"/></article></section>
 </template>
 <dialog ref="dialog" class="detail-modal" aria-labelledby="restock-detail-title" @cancel.prevent="closeDetail" @click="$event.target===dialog&&closeDetail()"><div v-if="detail"><header><div><p class="eyebrow">PRO / {{detail.email?'TASK':'DECISION'}}</p><h2 id="restock-detail-title">{{detail.email?'账号执行详情':'补货决策详情'}}</h2></div><button aria-label="关闭详情" @click="closeDetail">✕</button></header><div class="detail-body"><ProRestockDecisionDetails v-if="detail.snapshot" :decision="detail" :loading="detailLoading" :error="detailError" :statuses="statuses" :stages="stages" @retry="loadDetailTasks"/><template v-else><p><b>{{taskDetail.message}}</b></p><div class="line"><span>时间 / 配置</span><b>{{fmt(detail.created_at)}} · v{{detail.version}}</b></div><div v-if="detail.next_card_check_at" class="line"><span>下次卡片检查（后台调度时执行）</span><b>{{fmt(detail.next_card_check_at)}}</b></div><div class="line"><span>账号 / 状态</span><b>{{detail.email}} · {{taskDetail.status}}</b></div><div class="line"><span>卡台 / 原订单 / 卡片</span><b>{{cardProviderName(detail.card_provider)}} / {{detail.order_id||'—'}} / {{detail.card_id||'—'}}</b></div><div class="line"><span>执行时间</span><b>{{taskDetail.started?fmt(taskDetail.started):taskDetail.startNote}}</b></div><div v-if="taskDetail.submitted" class="line"><span>提交时间</span><b>{{fmt(taskDetail.submitted)}}</b></div><div class="line"><span>成功时间（核验完成）</span><b>{{taskDetail.success?fmt(taskDetail.success):taskDetail.successNote}}</b></div><div v-if="taskDetail.pushed" class="line"><span>推送完成时间</span><b>{{fmt(taskDetail.pushed)}}</b></div><div class="line"><span>Sub2 ID</span><b>{{detail.account_id||'—'}}</b></div><div v-for="event in restockTaskTimeline(detail)" :key="event.key" class="line"><span>{{event.label}}</span><b>{{fmt(event.at)}}</b></div></template><details><summary>完整脱敏快照与配置</summary><pre>{{JSON.stringify(detail,null,2)}}</pre></details></div><footer><button v-if="canStopTask(detail)" :disabled="!!stoppingTask" @click="stopTask(detail)">{{stoppingTask===detail.id?'停止中…':'停止本次'}}</button><button class="primary" @click="closeDetail">关闭详情</button></footer></div></dialog>
</div>
</template>
<style scoped>
.task-actions{display:flex;gap:6px;flex-wrap:wrap}
.demand-config{grid-column:1/-1;display:flex;align-items:center;flex-wrap:wrap;gap:12px;padding:10px;background:var(--surface,#f6faf9);border-radius:6px}.demand-config label{display:flex;align-items:center;gap:8px}.demand-config input[type=number]{width:90px}.demand-config small{color:var(--muted)}

.task-toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap;margin:14px 0}.task-filters{display:flex;gap:6px;flex-wrap:wrap}.task-filters .selected{background:var(--teal);color:white;border-color:var(--teal)}.task-toolbar input{max-width:280px}.task-hint{font-size:12px}.task-table{min-width:1000px}.task-account{max-width:260px;overflow-wrap:anywhere}.task-order{max-width:250px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.task-time{white-space:nowrap;font-variant-numeric:tabular-nums}.task-time small{white-space:normal}.task-message{max-width:310px;line-height:1.7}.task-status{font-weight:600}.task-status.ready{color:var(--teal)}.task-status.failed,.task-status.attention,.task-status.unknown{color:#b65b27}

.schedule-panel .panel-head h2{margin:0}.schedule-row{display:grid;grid-template-columns:20px minmax(0,1fr) minmax(0,1fr) minmax(90px,1fr) auto auto;gap:12px;align-items:end;margin:14px 0}.schedule-row label{margin:0}.schedule-row .schedule-takeover{align-self:center;white-space:nowrap}.schedule-number{align-self:center;color:var(--muted)}.schedule-row button{margin-bottom:1px}.schedule-panel .notice{overflow-wrap:anywhere}@media(max-width:600px){.schedule-row{gap:8px;grid-template-columns:16px minmax(0,1fr) minmax(0,1fr)}.schedule-row label:nth-of-type(3){grid-column:2}.schedule-row .schedule-takeover{grid-column:3}.schedule-row button{grid-column:3;justify-self:end}.schedule-row input{padding:9px 6px}}
.kpis .money-summary{overflow-wrap:anywhere}.kpis .kpi-rule{color:var(--teal);font-weight:500}.kpis .kpi-rule.inactive{color:var(--muted);font-weight:400}
.rate-window-form{display:flex;align-items:center;flex-wrap:wrap;gap:10px}.rate-window-form label{display:flex;align-items:center;gap:8px;margin:0}.rate-window-form input{width:70px;margin:0;padding:7px 8px}.rate-window-form button{padding:7px 12px}.rate-panel{display:flex;align-items:center;justify-content:space-between;gap:20px}.rate-panel strong{font-size:22px;color:var(--teal)}@media(max-width:600px){.rate-panel{align-items:flex-start;flex-direction:column}}.restock{--teal:#008786;--ink:#234752;--muted:#708a94;--border:#dce7e9;color:var(--ink);font-size:13px;max-width:1500px;margin:auto}.page-head,.panel-head,.actions,.runbar,.line,.savebar,.detail-modal header,.detail-modal footer{display:flex;align-items:center;justify-content:space-between;gap:16px}.page-head{margin-bottom:24px}.eyebrow{font-size:10px;letter-spacing:2px;color:var(--muted);margin-bottom:6px}h1{font-size:26px;font-weight:600;margin:0 0 9px}h2{font-size:15px;font-weight:600;margin:0 0 16px}h3{font-size:13px;font-weight:600;margin:0 0 10px}p{line-height:1.8;margin:8px 0;color:var(--muted)}button{border:1px solid var(--border);border-radius:7px;background:white;padding:9px 14px;color:var(--ink);font:inherit;cursor:pointer;white-space:nowrap}button:hover{border-color:var(--teal);color:var(--teal)}button:disabled{opacity:.5;cursor:default}button.primary{background:var(--teal);border-color:var(--teal);color:white}.tabs{display:flex;overflow-x:auto;gap:26px;border-bottom:1px solid var(--border);margin-bottom:22px}.tabs button{border:0;border-radius:0;background:none;padding:12px 0;color:var(--muted)}.tabs .selected{border-bottom:2px solid var(--teal);color:var(--teal);font-weight:600}.runbar{padding:13px 16px;background:#fff;border:1px dashed #c6d9d9;border-radius:9px;margin-bottom:18px;color:var(--muted);font-size:12px;flex-wrap:wrap}.kpis{display:grid;grid-template-columns:repeat(7,minmax(0,1fr));gap:16px;margin-bottom:20px}.kpis article,.panel{background:#fff;border:1px solid var(--border);border-radius:11px;padding:21px;min-width:0}.kpis span,.quality-grid span{color:var(--muted);font-size:12px}.kpis strong{display:block;font-size:34px;font-weight:550;margin:12px 0 5px}.kpis strong.medium{font-size:24px;margin-top:21px}.kpis small{font-size:16px;color:var(--muted);font-weight:400}.kpis p{font-size:11px}.two-grid{display:grid;grid-template-columns:1.1fr 1fr;gap:18px;margin-bottom:18px}.panel{margin-bottom:18px}.two-grid .panel{margin-bottom:0}.panel-head h2{margin:0}.panel-head{margin-bottom:18px}.badge{background:#eef5f5;border-radius:4px;padding:5px 9px;color:var(--teal);font-size:11px}.recommendation{border-top:3px solid var(--teal)}.recommend{font-size:48px;color:var(--teal);line-height:1.1;margin:22px 0}.recommend span{font-size:16px}.line{padding:11px 0;border-bottom:1px solid var(--border);font-size:12px;align-items:flex-start}.line span{color:var(--muted)}.line b{font-weight:500;text-align:right;overflow-wrap:anywhere;max-width:70%}.recommendation button{margin-top:15px}.rule{padding:12px 0;border-bottom:1px solid var(--border)}.rule:last-child{border:0}.quality-grid{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:18px}.quality-grid strong{display:block;font-size:20px;font-weight:500;margin:12px 0}.quality-grid p{font-size:11px}.notice{padding:12px 16px;border:1px solid #cce2df;background:#edf7f5;color:#31736e;border-radius:8px;margin:14px 0}.notice.warn{background:#fff8ec;border-color:#eedcc0;color:#976f31}.notice.danger{background:#fff1f0;border-color:#f2c9c5;color:#a45148}.danger-text{color:#b34843}.muted{font-size:12px;color:var(--muted)}.flow{display:flex;flex-wrap:wrap;gap:8px;margin:20px 0}.flow span{background:#eef4f4;padding:7px 10px;border-radius:6px;font-size:11px}.tablewrap{overflow:auto}table{width:100%;border-collapse:collapse;min-width:680px}th{background:#f5f8f9;text-align:left;color:var(--muted);font-size:11px;font-weight:400}td,th{padding:13px 12px;border-bottom:1px solid var(--border)}td{font-size:12px;max-width:410px;overflow-wrap:anywhere}td small{display:block;color:var(--muted);font-size:10px;margin-top:7px}.empty{text-align:center;padding:35px;color:var(--muted)}.fields{display:grid;grid-template-columns:1fr 1fr;gap:17px 20px}label{display:block;font-size:12px;margin:15px 0}input,select{width:100%;border:1px solid #d3e0e3;border-radius:6px;padding:9px 10px;background:white;color:var(--ink);font:inherit;margin-top:8px;min-width:0;box-sizing:border-box}.fields label{margin:4px 0}label.check{display:flex;align-items:center;gap:8px;grid-column:1/-1}input[type=checkbox]{width:16px;height:16px;margin:0;accent-color:var(--teal)}summary{cursor:pointer;font-weight:500}.advanced .two-grid{margin-top:25px}.savebar{position:sticky;bottom:0;background:#f3f7f7ed;backdrop-filter:blur(8px);padding:16px 0;color:var(--muted)}.pagination{display:flex;justify-content:flex-end;gap:10px;margin-top:18px}svg{width:100%;height:auto}.detail-modal{border:1px solid var(--border);border-radius:13px;max-width:850px;width:calc(100% - 32px);max-height:85vh;padding:0;color:var(--ink);background:white;box-shadow:0 24px 100px #12383d33}.detail-modal::backdrop{background:#102e3c77;backdrop-filter:blur(3px)}.detail-modal header,.detail-modal footer{padding:18px 24px;border-bottom:1px solid var(--border)}.detail-modal h2{margin:0}.detail-modal footer{justify-content:flex-end;border-bottom:0;border-top:1px solid var(--border)}.detail-body{padding:18px 24px;overflow:auto;max-height:58vh}.detail-body pre{white-space:pre-wrap;overflow-wrap:anywhere;background:#f3f7f7;padding:16px;border-radius:8px;font-size:11px}.detail-body details{margin-top:20px}:focus-visible{outline:2px solid var(--teal);outline-offset:3px}
:global([data-theme=dark] .restock){--ink:#d4e5e8;--muted:#a8bac3;--border:#324b55;--teal:#5bd4c7}:global([data-theme=dark] .restock :is(.panel,.kpis article,.runbar,button,input,select,.detail-modal)){background:#192e38}:global([data-theme=dark] .restock :is(th,.badge,.flow span,.savebar,.detail-body pre)){background:#203b46}:global([data-theme=dark] .restock button.primary){background:#007b78;border-color:#159d96;color:#fff}:global([data-theme=dark] .restock .notice){background:#193b38;border-color:#32615b;color:#b4e4d9}:global([data-theme=dark] .restock .notice.warn){background:#3c3222;border-color:#6f5733;color:#efd3a1}:global([data-theme=dark] .restock .notice.danger){background:#412a2b;border-color:#784346;color:#f3b4af}
@media(min-width:1001px) and (max-width:1200px){.kpis{grid-template-columns:repeat(3,minmax(0,1fr))}}@media(max-width:1000px){.kpis{grid-template-columns:repeat(2,minmax(0,1fr))}.two-grid{grid-template-columns:1fr}.quality-grid{grid-template-columns:1fr;margin-bottom:18px}.quality-grid .panel{margin-bottom:0}}@media(max-width:600px){.page-head{align-items:flex-start;flex-direction:column}.actions{width:100%}.kpis{gap:10px}.kpis article,.panel{padding:15px}.kpis strong{font-size:28px}.kpis strong.medium{font-size:18px}.fields{grid-template-columns:1fr}.tabs{gap:22px}.detail-body,.detail-modal header,.detail-modal footer{padding:15px}.runbar{font-size:11px}.line{gap:10px}.line b{max-width:60%}}
.restock{container-type:inline-size}
@container(max-width:1250px){.kpis{grid-template-columns:repeat(3,minmax(0,1fr))}}
@container(max-width:900px){.kpis{grid-template-columns:repeat(2,minmax(0,1fr))}}
</style>
