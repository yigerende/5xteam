<script setup>
import { sortCardsByReturnState } from '../card-workflow-sort'
import CardPaymentPolicy from './CardPaymentPolicy.vue'
import { applyPaymentPolicies } from '../payment-only'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { Globe, RefreshCw, Plus, Settings2, X, CreditCard, CheckCircle2, Clock3, OctagonX, Play } from 'lucide-vue-next'
import { api } from '../api'
import { formatTime } from '../utils'
import { operationNames, operationStates, returnStates, terminal, cardState, cancelReason, requestID, nexovccSettingsPayload, nexovccOperationPayload, templateProductIDs } from '../nexovcc'
import Pagination from './Pagination.vue'
import NexovccProductPicker from './NexovccProductPicker.vue'
import NexovccProgress from './NexovccProgress.vue'
import MessageBar from './MessageBar.vue'
import CardCopyButton from './CardCopyButton.vue'
import CardTemplateBadge from './CardTemplateBadge.vue'
import { cardOrigin } from '../card-platform'
import PaymentCardStatus from './PaymentCardStatus.vue'
import CardOccupancy from './CardOccupancy.vue'
import { financeTotals, money, rmb, tradeStates } from '../nexovcc-finance'
import NexovccFinanceSummary from './NexovccFinanceSummary.vue'
import NexovccCaseTable from './NexovccCaseTable.vue'
import NexovccLedgerDialog from './NexovccLedgerDialog.vue'
import NexovccReconciliationBar from './NexovccReconciliationBar.vue'
import NexovccCardFunding from './NexovccCardFunding.vue'
import NexovccCardReturns from './NexovccCardReturns.vue'
import NexovccCardAmounts from './NexovccCardAmounts.vue'
import { sortReturnCases } from '../nexovcc-case-details'
import { sortClosedCards } from '../nexovcc-card-list'
import { cardBalanceDisplay, frozenBalanceDisplay } from '../nexovcc-reconciliation'

const props=defineProps({defaultPageSize:{type:Number,default:10},entry:{type:Object,default:null}})
const emit=defineEmits(['entry-applied'])
const targetCardID=ref('')
const data=reactive({cards:[],operations:[],cases:[],products:[],balance:{},finance:{},groups:[],finance_error:'',sync:{},withdraw:{},control:{paused:false}})
const returnCases=computed(()=>data.cases.filter(c=>!c.removed_at))
const unopenedCount=computed(()=>returnCases.value.filter(c=>c.state==='unopened').length)
const deletingUnopened=ref(false)
const categorizingCard=ref('')
const cfg=reactive({enabled:false,default_max_accounts:1,single_active_account:true,page_refresh_seconds:30,balance_sync_seconds:60,card_sync_seconds:60,transaction_sync_seconds:60,account_sync_seconds:1800,product_sync_seconds:1800,http_522_retry_seconds:5,http_522_retries:2,request_interval_seconds:3,max_online_cards:0,auto_recycle_at:0,auto_withdraw_enabled:false,auto_withdraw_interval_seconds:60,batch_interval_seconds:3,templates:[],login:'',password:'',fund_password:'',totp_secret:'',clear_totp:false})
const platformFilter=ref('oai')
const origin=row=>cardOrigin(row,data.operations,cfg.templates)
function setPlatform(value){platformFilter.value=value;targetCardID.value='';page.value=1;selection.value=[]}
const savedEnabled=ref(false)
const controlBusy=ref(false),apiPaused=ref(false)
const apiBlocked=computed(()=>apiPaused.value||controlBusy.value)
const ready=ref(false),busy=ref(false),saving=ref(false),tab=ref('cards'),filter=ref('normal'),search=ref(''),page=ref(1),pageSize=ref(props.defaultPageSize||10),selection=ref([])
const message=reactive({text:'',type:'success'}),job=ref({}),batch=ref(null),dialog=ref(null),privateInfo=ref(null)
const view=ref('list'),returnCategory=ref('normal'),returnFilter=ref(''),detail=ref(null),ledger=ref(null),detailState=ref(''),detailSearch=ref(''),detailPage=ref(1),detailSize=ref(props.defaultPageSize||10)
const ledgerSyncSerial=ref('')
const withdrawAttentionOnly=ref(false)
const withdrawAttention=computed(()=>data.operations.filter(op=>op.auto_withdraw&&op.status==='unknown'))
function showWithdrawAttention(){tab.value='operations';search.value='';page.value=1;withdrawAttentionOnly.value=true}
watch(tab,value=>{if(value!=='operations')withdrawAttentionOnly.value=false})
const detailRow=computed(()=>data.cards.find(r=>r.card.id===detail.value?.cardID))
const detailCases=computed(()=>sortReturnCases(returnCases.value.filter(c=>(!detail.value?.cardID||c.card_id===detail.value.cardID)&&(!detailState.value||c.state===detailState.value)&&(!detailSearch.value||[c.email,c.id,c.last4,c.card_name].some(v=>String(v||'').toLowerCase().includes(detailSearch.value.toLowerCase()))))))
const detailVisible=computed(()=>detailCases.value.slice((detailPage.value-1)*detailSize.value,detailPage.value*detailSize.value))
watch([detail,detailState,detailSearch],()=>detailPage.value=1)
watch(()=>props.defaultPageSize,v=>{detailSize.value=v||10;detailPage.value=1})
watch(view,v=>{try{localStorage.setItem('nexovcc-view',v)}catch{}})
let disposed=false,pollTimer,privateTimer,updateTimer,runGeneration=0,stopGeneration=0
const show=(text,type='success')=>Object.assign(message,{text,type})
const tabs=[{id:'cards',name:'卡片管理'},{id:'returns',name:'账号回款'},{id:'operations',name:'操作记录'},{id:'settings',name:'配置与模板'}]
const running=computed(()=>busy.value||deletingUnopened.value||job.value.status==='running'||!!batch.value?.running)
const openingBlocked=computed(()=>apiBlocked.value||deletingUnopened.value||!!batch.value?.running||(running.value&&!job.value.read_only))
const dialogBlocked=computed(()=>saving.value||(dialog.value?.type==='open'?openingBlocked.value:running.value))
const activeCards=computed(()=>data.cards.filter(r=>r.card.status!=='4'))
const pendingCardCount=computed(()=>activeCards.value.filter(r=>r.card.normal_pending).length)
const cardTotal=computed(()=>{const rows=activeCards.value;if(!rows.length)return '0.00';if(rows.some(r=>r.card.balance===''))return '待同步';return rows.reduce((s,r)=>s+Math.round(Number(r.card.balance)*100),0)/100})
const selectable=computed(()=>activeCards.value.filter(r=>r.local?.available).length)
const frozenBalance=computed(()=>frozenBalanceDisplay(data))
function cardCategory(row){return row.card.status==='4'?'closed':row.card.normal_pending?'normal_pending':'normal'}
const filtered=computed(()=>{
 const rows=tab.value==='cards'?data.cards.filter(r=>targetCardID.value?r.card.id===targetCardID.value:cardCategory(r)===filter.value&&origin(r).platform===platformFilter.value):tab.value==='returns'?sortReturnCases(returnCases.value.filter(c=>returnCategory.value==='unopened'?c.state==='unopened':c.state!=='unopened'&&(!returnFilter.value||c.state===returnFilter.value))):(withdrawAttentionOnly.value?withdrawAttention.value:data.operations)
 const ordered=tab.value==='cards'?(filter.value==='closed'?sortClosedCards(rows,data.operations):sortCardsByReturnState(rows,returnCases.value)):rows
 const q=search.value.trim().toLowerCase();return ordered.filter(r=>!q||[r.card?.serial_no,r.card?.last4,r.card?.remark,r.email,r.id,r.last4,r.serial_no,r.card_name].some(v=>String(v||'').toLowerCase().includes(q)))
})
const visible=computed(()=>filtered.value.slice((page.value-1)*pageSize.value,page.value*pageSize.value))
const totalsByCard=computed(()=>new Map((tab.value==='cards'?visible.value:[]).map(row=>[row.card.id,financeTotals(data,row.card.serial_no)])))
watch([tab,filter,platformFilter,search,returnFilter,returnCategory],()=>{page.value=1;selection.value=[]})
watch(()=>filtered.value.length,()=>{page.value=Math.min(page.value,Math.max(1,Math.ceil(filtered.value.length/pageSize.value)))})
watch(()=>props.defaultPageSize,v=>{pageSize.value=v||10;page.value=1})
watch(()=>props.entry,entry=>{
 if(!entry?.cardID)return
 targetCardID.value=entry.cardID;tab.value='cards';search.value='';page.value=1
 const row=data.cards.find(r=>r.card.id===entry.cardID);if(row){filter.value=cardCategory(row);platformFilter.value=origin(row).platform}
 if(entry.email){detailState.value='';detailSearch.value=entry.email;detail.value={type:'accounts',cardID:entry.cardID}}
 emit('entry-applied')
},{immediate:true})
watch(()=>data.cards,()=>{const row=data.cards.find(r=>r.card.id===targetCardID.value);if(row){filter.value=cardCategory(row);platformFilter.value=origin(row).platform}})
function setFilter(value){targetCardID.value='';filter.value=value}
async function changeCardCategory(row){
 if(categorizingCard.value||row.card.status==='4')return
 const category=row.card.normal_pending?'normal':'normal_pending'
 categorizingCard.value=row.card.id
 try{
  const card=await api(`/api/nexovcc/cards/${encodeURIComponent(row.card.id)}/category`,{method:'PUT',body:{category}})
  const current=data.cards.find(r=>r.card.id===card.id)
  if(current)current.card=card
  if(targetCardID.value===card.id)targetCardID.value=''
  selection.value=selection.value.filter(id=>id!==card.id)
  show(category==='normal_pending'?'已移到正常待定':'已恢复正常')
 }catch(e){show(e.message,'error')}finally{categorizingCard.value=''}
}
function at(value){return value&&!String(value).startsWith('0001')?formatTime(value):'—'}
function amount(value){return value!==undefined&&value!==null&&value!==''?Number(value).toFixed(2):'—'}
function balanceDisplay(row){return cardBalanceDisplay(data,row)}
function associated(row){return returnCases.value.filter(c=>c.card_id===row.card.id)}
function cardTotals(row){return totalsByCard.value.get(row.card.id)||financeTotals(data,row.card.serial_no)}
function inspect(options,serial=''){if(options.type==='accounts'){detailState.value=options.state||'';detailSearch.value=options.search||'';detail.value={type:'accounts',cardID:data.cards.find(r=>r.card.serial_no===serial)?.card.id||''}}else ledger.value={...options,serial}}
function manage(row){detail.value={type:'manage',cardID:row.card.id}}
function caseTrades({row,options={type:'trades'}}){const serial=data.cards.find(r=>r.card.id===row.card_id)?.card.serial_no||row.match?.group?.serial_no;if(!serial){show('卡片资料未同步，无法定位流水','error');return}ledger.value={...options,serial,group:options.scope==='card'?null:row.match?.group}}
function manageCase(row){const card=data.cards.find(r=>r.card.id===row.card_id);if(card)manage(card)}
function editMatch(row){dialog.value={type:'match',row,group_id:row.match?.group?.id||'',reason:'',confirm:false}}
function matchOptions(row){const serial=data.cards.find(r=>r.card.id===row.card_id)?.card.serial_no;return data.groups.filter(g=>!g.non_pro&&!g.related_group_id&&g.valid&&Number(g.payment)>0&&g.serial_no===serial&&!data.cases.some(c=>c.id!==row.id&&c.match?.group?.id===g.id))}
function selectPage(){const ids=visible.value.filter(r=>!cancelReason(r,data.operations)).map(r=>r.card.id);selection.value=ids.every(id=>selection.value.includes(id))?selection.value.filter(id=>!ids.includes(id)):[...new Set([...selection.value,...ids])]}
function batchAction(action){dialog.value={type:'batch',action,ids:[...selection.value],confirm:false}}
async function load(){const result=await api('/api/nexovcc/dashboard');if(!disposed){Object.assign(data,applyPaymentPolicies(result));if(result.control)apiPaused.value=result.control.paused===true}}
async function deleteUnopenedCases(){
 if(running.value||saving.value||!unopenedCount.value)return
 if(!window.confirm(`确认清理全部 ${unopenedCount.value} 条未开通记录？包含其他分页和搜索范围外的记录。\n删除表示你已人工核实未开通，将结束原订单并解除该笔占用。正在执行或已有付款记录的会保留；原账号、订单证据和资金流水不删除。`))return
 deletingUnopened.value=true;show('正在清理未开通记录…','info')
 try{
  const result=await api('/api/nexovcc/cases/unopened',{method:'DELETE',body:{confirm:true}})
  if(disposed)return
  const ids=new Set(result.deleted_ids)
  data.cases.forEach(c=>{if(ids.has(c.id))c.removed_at=new Date().toISOString()})
  const clampPage=()=>{page.value=Math.min(page.value,Math.max(1,Math.ceil(filtered.value.length/pageSize.value)))}
  clampPage()
  const text=`已删除 ${result.deleted} 条未开通记录`+(result.skipped?`，${result.skipped} 条正在执行或有付款记录的已保留`:'')
  try{await load();clampPage();show(text,result.skipped?'info':'success')}catch(e){show(text+'；刷新失败：'+e.message,'error')}
 }catch(e){show('清理失败：'+e.message,'error')}finally{deletingUnopened.value=false}
}
async function loadJob(){const generation=runGeneration;const result=await api('/api/nexovcc/job');if(!disposed&&generation===runGeneration){job.value=result;if(typeof result.paused==='boolean')apiPaused.value=result.paused};return result}
async function poll(){clearTimeout(pollTimer);const generation=runGeneration;try{const j=await loadJob();if(disposed||generation!==runGeneration)return;if(j.status==='running'){await load();pollTimer=setTimeout(poll,1200)}else{busy.value=false;await load();if(j.message)show(j.message,j.status==='completed'?'success':j.status==='attention'?'info':'error')}}catch(e){if(!disposed&&generation===runGeneration){show('进度查询暂时失败，正在重新连接：'+e.message,'error');pollTimer=setTimeout(poll,3000)}}}
async function controlInterfaces(){
 if(controlBusy.value)return
 const stopping=!apiPaused.value
 controlBusy.value=true
 if(stopping){stopGeneration++;runGeneration++;clearTimeout(pollTimer);busy.value=false;if(batch.value)batch.value.running=false;dialog.value=null}
 try{
  const state=await api('/api/nexovcc/control',{method:'POST',body:{action:stopping?'stop':'resume'}})
  apiPaused.value=state.paused;data.control=state
  await loadJob();await load()
  show(stopping?'环球所有接口已停止；已提交操作保留，请恢复后核实原记录':'环球接口已恢复，后台按配置继续运行','info')
 }catch(e){show(e.message,'error');await recoverProgress()}finally{controlBusy.value=false}
}
async function recoverProgress(){if(disposed)return;try{const j=await loadJob();if(j.status==='running')pollTimer=setTimeout(poll,1200)}catch{pollTimer=setTimeout(poll,3000)}}
async function watchUpdates(){
 if(disposed)return
 try{if(!running.value){const previous=job.value;const current=await loadJob();if(current.status==='running')poll();else if(current.id!==previous.id||current.status!==previous.status||data.operations.some(op=>op.action==='open'&&op.status==='import_pending'&&!data.cards.some(row=>row.card.id===op.card_id&&(row.card.normal_pending||row.card.status==='4'))))await load()}}catch{}
 if(!disposed)updateTimer=setTimeout(watchUpdates,5000)
}
async function run(input){
 if(apiBlocked.value)throw new Error('环球接口已停止，请先恢复')
 const generation=++runGeneration
 const accepted=await api('/api/nexovcc/run',{method:'POST',body:{...nexovccOperationPayload(input),confirm:true}})
 if(disposed||generation!==runGeneration)return null
 job.value=accepted
 // Follow the durable server job; changing pages never cancels a submitted operation.
 let current=accepted
 while(current.status==='running'){
  if(disposed)return null
  await new Promise(resolve=>setTimeout(resolve,1000));if(disposed||generation!==runGeneration)return null;current=await loadJob();if(generation!==runGeneration)return null
  if(current.id!==accepted.id)throw new Error('后台任务已变化，请查看操作记录核实原请求')
 }
 await load();if(current.status==='failed')throw new Error(current.message)
 return current
}
async function perform(input){if(apiBlocked.value){show('环球接口已停止，请先恢复','info');return}if(running.value)return;const generation=stopGeneration;busy.value=true;job.value={status:'running',message:'正在'+(operationNames[input.action]||'查询数据')+'…'};show(job.value.message,'info');try{const j=await run(input);if(j)show(j.message,j.status==='completed'?'success':'info')}catch(e){if(generation===stopGeneration){show(e.message+'；请查看操作记录确认结果','error');await recoverProgress()}}finally{if(generation===stopGeneration)busy.value=false}}
async function syncCategoryTransactions(){
 if(filter.value==='closed')return
 await perform({action:'card_finance',card_category:filter.value})
}
async function refreshPendingCards(){
 if(filter.value!=='normal_pending'||!pendingCardCount.value)return
 await perform({action:'pending_sync'})
}
async function syncCardTransactions(serial){
 if(running.value)return
 ledgerSyncSerial.value=serial
 const row=data.cards.find(r=>r.card.serial_no===serial)
 if(!serial||!row){show('未找到本卡资料，未同步其他卡片','error');return}
 await perform({action:'card_finance',card_id:row.card.id})
}
async function save(){saving.value=true;try{const result=await api('/api/nexovcc/settings',{method:'PUT',body:nexovccSettingsPayload(cfg)});Object.assign(cfg,result,{login:'',password:'',fund_password:'',totp_secret:'',clear_totp:false});savedEnabled.value=result.enabled===true;show('环球配置已保存')}catch(e){show(e.message,'error')}finally{saving.value=false}}
function addTemplate(){cfg.templates.push({id:crypto.randomUUID(),platform:'oai',name:'',product_id:'',product_ids:[],amount:'',fee_buffer:'0.00',holder_id:''})}
function openCards(){dialog.value={type:'open',template_id:cfg.templates[0]?.id||'',count:1}}
function cardAction(row,action){dialog.value={type:'action',action,card:row,amount:'',remark:row.card.remark||'',request_id:requestID(),confirm:false}}
function editReturn(row){if(row.payment_only){show('请在卡片的回款方式中调整只付款分类','info');return;}dialog.value={type:'return',row,state:row.state,reason:'',confirm:false}}
function editLimit(row){dialog.value={type:'limit',row,maximum:row.local?.max_accounts||0}}
function resolve(op){
 if(op.status!=='unknown')return perform({action:'resolve',request_id:op.id,outcome:'query'})
 dialog.value={type:'resolve',op,outcome:'query',serial_no:op.serial_no||'',evidence:'',confirm:false}
}
function closeDialog(){if(!dialogBlocked.value)dialog.value=null}
async function submit(){
 const d=dialog.value;if(!d||dialogBlocked.value)return
 if(d.type==='open'){await batchOpen(d);return}
 if(d.type==='batch'){await batchCards(d);return}
 if(d.type==='match'){
  saving.value=true;try{await api('/api/nexovcc/cases/'+encodeURIComponent(d.row.id)+'/match',{method:'PUT',body:{group_id:d.group_id,reason:d.reason,confirm:d.confirm}});dialog.value=null;await load();show('已关联流水，回款状态已重新计算')}catch(e){show(e.message,'error')}finally{saving.value=false};return
 }
 if(d.type==='return'){
  saving.value=true;try{await api('/api/nexovcc/cases/'+encodeURIComponent(d.row.id),{method:'PUT',body:{state:d.state,reason:d.reason,revision:d.row.revision,confirm:d.confirm}});dialog.value=null;await load();show('回款状态已保存')}catch(e){show(e.message,'error')}finally{saving.value=false};return
 }
 if(d.type==='limit'){
  saving.value=true;try{await api('/api/gptpay/cards/'+encodeURIComponent(d.row.card.id),{method:'PUT',body:{max_accounts:Number(d.maximum)}});dialog.value=null;await load();show('单卡开通上限已保存')}catch(e){show(e.message,'error')}finally{saving.value=false};return
 }
 if(!d.confirm){show('请勾选确认后再提交','error');return}
 dialog.value=null
 if(d.type==='resolve')await perform({action:'resolve',request_id:d.op.id,outcome:d.outcome,serial_no:d.serial_no,evidence:d.evidence})
 else await perform({action:d.action,card_id:d.card.card.id,amount:d.amount,remark:d.remark,request_id:d.request_id})
}
async function batchOpen(d){
 if(apiBlocked.value)return
 const batchGeneration=stopGeneration
 const count=Number(d.count);if(!Number.isInteger(count)||count<1||count>100||!d.template_id){show('请选择模板，数量须为 1～100','error');return}
 if(!d.confirm){show('请确认开卡将扣除账户资金','error');return}
 dialog.value=null;busy.value=true
 try{const j=await run({action:'open',template_id:d.template_id,count,request_id:requestID()});if(j)show(j.message,j.status==='completed'?'success':'info')}
 catch(e){if(batchGeneration===stopGeneration){show(e.message,'error');await recoverProgress()}}finally{if(batchGeneration===stopGeneration){busy.value=false;if(batch.value)batch.value.running=false}}
}
async function batchCards(d){
 if(!d.confirm||!d.ids.length||running.value)return
 dialog.value=null
 await perform({action:d.action,card_ids:[...d.ids],request_id:requestID()})
 selection.value=[]
}
async function reveal(row){if(apiBlocked.value)return;try{privateInfo.value=await api('/api/nexovcc/cards/'+encodeURIComponent(row.card.id)+'/private?include_holder=true',{method:'POST',body:{}});clearTimeout(privateTimer);privateTimer=setTimeout(()=>{privateInfo.value=null},30000)}catch(e){show(e.message,'error')}}
async function toggle(row){if(running.value)return;saving.value=true;try{await api('/api/gptpay/cards/'+encodeURIComponent(row.card.id),{method:'PUT',body:{enabled:!row.local.enabled}});await load()}catch(e){show(e.message,'error')}finally{saving.value=false}}
onMounted(async()=>{updateTimer=setTimeout(watchUpdates,5000);try{view.value=localStorage.getItem('nexovcc-view')==='cards'?'cards':'list'}catch{};try{const settings=await api('/api/nexovcc/settings');if(disposed)return;Object.assign(cfg,settings);savedEnabled.value=settings.enabled===true;await load();await loadJob();ready.value=true;if(job.value.status==='running')poll()}catch(e){show(e.message,'error')}})
onBeforeUnmount(()=>{disposed=true;clearTimeout(pollTimer);clearTimeout(privateTimer);clearTimeout(updateTimer);privateInfo.value=null;cfg.login=cfg.password=cfg.fund_password=cfg.totp_secret=''})
async function tradePurposeUpdated(group){
 const index=data.groups.findIndex(g=>g.id===group.id)
 if(index>=0)data.groups[index]=group
 try{await load()}catch(e){show('处理已保存，刷新失败：'+e.message,'error')}
}
</script>

<template>
 <div class="nex-page">
  <header class="heading"><div><span class="eyebrow">GLOBAL CARD PLATFORM</span><h1><Globe :size="26"/>环球卡台</h1><p>资金去向、账号回款、收益与成本</p></div><div class="actions"><button class="btn ghost" :class="{'danger-text':!apiPaused}" :disabled="controlBusy||!ready" @click="controlInterfaces"><Play v-if="apiPaused" :size="16"/><OctagonX v-else :size="16"/>{{controlBusy?'处理中…':apiPaused?'恢复接口':'立即停止'}}</button><button class="btn ghost" :disabled="running||!ready" @click="tab='settings'"><Settings2 :size="16"/>配置与模板</button><button class="btn ghost" :disabled="running||apiBlocked" @click="perform({action:'sync'})"><RefreshCw :size="16"/>{{running?'同步处理中…':'同步全部'}}</button><button class="btn primary" :disabled="openingBlocked||!cfg.templates.length" @click="openCards"><Plus :size="16"/>开卡</button></div></header>
  <MessageBar :message="message"/>
  <p v-if="apiPaused" class="note danger-text" role="status">环球接口已全部停止，自动与手动上游请求均暂停。已提交的操作可能已生效，请恢复后查询原记录。</p>
  <NexovccProgress v-if="!apiPaused" :job="job" :batch="batch" :busy="busy"/>
  <p v-if="!savedEnabled&&ready" class="note">卡台已关闭，可手动查询和操作。</p>
  <div v-show="tab!=='settings'" class="overview"><article><span>账户余额</span><strong>{{savedEnabled?amount(data.balance.amount):'---'}} <small>USD</small></strong><button class="text-link" :disabled="running||apiBlocked" @click="perform({action:'balance'})">查询余额</button></article><article><span>卡片冻结金额</span><strong>{{savedEnabled?(frozenBalance.current===null?'待核对':money(frozenBalance.current)):'---'}} <small>USD</small></strong><small>正常卡未回款 · 已回到卡余额的不重复计入</small>
   <details v-if="savedEnabled" class="frozen-breakdown"><summary>查看冻结明细</summary><p>按正常卡未回款流水核算，扣除余额已恢复部分；已销卡不计入。</p><p>平台冻结总额 {{money(frozenBalance.platform)}} USD（仅供核对）</p><p v-if="frozenBalance.current===null">本卡流水或匹配信息不完整，暂不能计算。</p><div v-for="row in frozenBalance.pendingRows" :key="row.serial"><p>尾号 {{row.last4}} · 当前冻结 {{money(row.amount)}} USD</p><button class="text-link" @click="inspect({type:'trades'},row.serial)">查看交易</button></div><template v-if="frozenBalance.recovered"><p>余额已恢复、流水待确认 {{money(frozenBalance.recovered)}} USD<br/>已从冻结金额剔除，回款状态仍等流水确认。</p><div v-for="row in frozenBalance.recoveredRows" :key="row.serial"><button class="text-link" @click="inspect({type:'trades'},row.serial)">尾号 {{row.last4}} · {{money(row.amount)}} USD · 查看交易</button></div></template><template v-if="frozenBalance.historical"><p>已销卡历史授权 {{money(frozenBalance.historical)}} USD / {{rmb(frozenBalance.historical)}} R<br/>不作为正常卡冻结款或可继续回款余额。</p><div v-for="row in frozenBalance.rows" :key="row.serial"><p>尾号 {{row.last4}} · 历史授权 {{money(row.amount)}} USD<br/>销卡已退回主账户 {{money(row.returned)}} USD</p><button class="text-link" @click="inspect({type:'trades'},row.serial)">查看交易</button> · <button class="text-link" @click="inspect({type:'funds',kind:'card-transfers'},row.serial)">查看退回流水</button></div></template><p>仅分开展示，不改原始流水、不重复计成本；冻结金额不再叠加到卡片余额。</p></details></article><article><span>卡片余额</span><strong>{{savedEnabled?(typeof cardTotal==='number'?cardTotal.toFixed(2):cardTotal):'---'}} <small>USD</small></strong><small>正常及销卡处理中 {{savedEnabled?activeCards.length:'---'}} 张</small></article><article><span>可用于开通</span><strong>{{savedEnabled?selectable:'---'}} <small>张卡</small></strong><small>满足开通上限与占用要求</small></article></div>
  <details v-if="savedEnabled&&frozenBalance.pendingRows.some(item=>item.refundPending)" v-show="tab!=='settings'" class="refund-notices"><summary>{{frozenBalance.pendingRows.filter(item=>item.refundPending).length}} 张卡清算后待退款 · 已计入冻结金额<span>查看明细</span></summary><p v-for="row in frozenBalance.pendingRows.filter(item=>item.refundPending)" :key="'refund-pending-'+row.serial" class="note">尾号 {{row.last4}} 有已清算、等待退款的交易 {{money(row.refundPending)}} USD，已计入冻结金额；余额恢复或退款流水确认后会自动移出。</p></details>
  <p v-if="savedEnabled&&frozenBalance.unlinkedSettledRefund" class="note">有已清算的待回款账号未匹配到交易，冻结金额暂显示待核对。</p>
  <NexovccReconciliationBar v-show="tab!=='settings'" :masked="!savedEnabled" :syncing="running" :data="data" :refresh-seconds="cfg.transaction_sync_seconds" @details="inspect($event)"/>
  <NexovccFinanceSummary v-show="tab!=='settings'" collapsible :data="data" :masked="!savedEnabled" @details="inspect($event)"/>
  <div class="sync-line"><span>财务更新 {{at(data.finance?.at)}}</span><span v-if="data.sync?.error||data.finance_error" class="danger-text">部分同步未完成：{{data.sync?.error||data.finance_error}}</span><button class="text-link" @click="inspect({type:'recharges'})">充值记录</button><button class="text-link" @click="inspect({type:'funds'})">资金明细</button><button class="text-link" @click="inspect({type:'days'})">平台日汇总</button><button class="text-link" @click="inspect({type:'reviews'})">交易核实</button></div>
  <div v-if="savedEnabled&&cfg.auto_withdraw_enabled" class="sync-line"><span>自动提现 · 上次检查 {{at(data.withdraw?.last_attempt_at)}}</span><span>下次 {{at(data.withdraw?.next_check_at)}}</span><span v-if="data.withdraw?.error" class="danger-text">{{data.withdraw.error}}</span><button v-if="withdrawAttention.length" class="text-link" @click="showWithdrawAttention">{{withdrawAttention.length}} 笔自动提现待核实 · 查看</button></div>
  <nav class="tabs"><button v-for="item in tabs" :key="item.id" :class="{selected:tab===item.id}" @click="tab=item.id">{{item.name}}</button></nav>
  <div v-if="!ready" class="panel empty">正在读取环球卡台…</div>
  <template v-else-if="tab!=='settings'">
   <p v-if="targetCardID" class="sync-line">已定位 Pro 开通银行卡 <button class="text-link" @click="targetCardID=''">查看全部卡片</button></p>
   <div class="toolbar"><div v-if="tab==='cards'" class="filters"><button :class="{selected:filter==='normal'}" @click="setFilter('normal')">正常</button><button :class="{selected:filter==='normal_pending'}" @click="setFilter('normal_pending')">正常待定</button><button :class="{selected:filter==='closed'}" @click="setFilter('closed')">销卡</button></div><div v-else-if="tab==='returns'" class="filters return-filters"><button :class="{selected:returnCategory==='normal'}" @click="returnCategory='normal'">正常 {{returnCases.filter(c=>c.state!=='unopened').length}}</button><button :class="{selected:returnCategory==='unopened'}" @click="returnCategory='unopened'">未开通 {{returnCases.filter(c=>c.state==='unopened').length}}</button><select v-if="returnCategory==='normal'" v-model="returnFilter" aria-label="回款状态筛选"><option value="">全部状态</option><option v-for="state in ['opened','waiting','returned','no_return']" :key="state" :value="state">{{returnStates[state]}} {{returnCases.filter(c=>c.state===state).length}}</option></select></div><input v-model="search" type="search" placeholder="搜索账号、卡片尾号或操作编号"/><div v-if="tab==='cards'" class="filters"><button :class="{selected:view==='list'}" @click="view='list'">列表</button><button :class="{selected:view==='cards'}" @click="view='cards'">卡片</button></div></div>
   <div v-if="tab==='cards'" class="filters platform-filters" role="group" aria-label="开卡平台筛选"><button v-for="p in ['oai','cc']" :key="p" :class="{selected:platformFilter===p}" @click="setPlatform(p)">{{p.toUpperCase()}}</button></div>
   <div v-if="tab==='cards'&&filter!=='closed'" class="batch-toolbar"><button class="btn ghost" :disabled="running||apiBlocked||!ready" :title="filter==='normal_pending'?'仅同步全部正常待定卡的余额、交易与资金明细':'优先已申请待回款卡，逐卡同步正常卡的余额、交易与资金明细'" @click="syncCategoryTransactions"><RefreshCw :size="14"/>同步余额与流水</button><button v-if="filter==='normal_pending'" class="btn ghost" :disabled="running||apiBlocked||!ready||!pendingCardCount" title="刷新全部正常待定卡的资料、状态、余额、交易与资金明细，包含所有分页" @click="refreshPendingCards"><RefreshCw :size="14"/>一键刷新待定卡</button><button class="text-link" :disabled="running" @click="selectPage">选择 / 取消本页可操作卡</button><span>已选 {{selection.length}} 张</span><button class="btn ghost" :disabled="running||apiBlocked||!selection.length" @click="batchAction('withdraw_all')">批量提现留 1U</button><button class="btn ghost" :disabled="running||apiBlocked||!selection.length" @click="batchAction('cancel')">批量一键销卡</button><button class="btn ghost" :disabled="running||apiBlocked||!selection.length" @click="batchAction('withdraw_cancel')">批量提现留 1U 并销卡</button></div>
   <p v-if="tab==='cards'&&filter==='normal_pending'" class="sync-line">正常待定卡保留上次数据，不参与页面自动刷新和同步全部；可在此单独刷新。</p>
   <div v-if="tab==='returns'&&returnCategory==='unopened'" class="batch-toolbar"><button class="btn ghost danger-text" :disabled="running||saving||!unopenedCount" @click="deleteUnopenedCases">{{deletingUnopened?'正在删除…':'一键删除未开通'}}</button><span>删除即已核实未开通，解除本单占用；保留正在执行及已付款记录</span></div>
   <template v-if="tab==='cards'">
    <section v-if="view==='list'" class="panel table-wrap"><table><thead><tr><th></th><th>卡片</th><th>开卡模板 / 平台</th><th>状态 / 余额 USD</th><th>额度 / 退回 / 差额</th><th>开通账号名额</th><th>账号回款</th><th>收益 / 成本 / 结余</th><th>操作</th></tr></thead><tbody><tr v-for="row in visible" :key="row.card.id"><td><input v-model="selection" type="checkbox" :value="row.card.id" :disabled="!!cancelReason(row,data.operations)||running" :aria-label="'选择卡片 '+row.card.last4"/></td><td><button class="card-name" @click="manage(row)">•••• {{row.card.last4||'待同步'}}</button><small v-if="row.card.normal_pending&&row.card.status!=='4'">正常待定</small><small>开卡 {{at(row.card.created_at)}}</small><small>{{row.card.bin}} · {{row.card.remark||row.card.serial_no}}</small></td><td><CardTemplateBadge :origin="origin(row)"/></td><td><PaymentCardStatus v-if="row.card.imported" :card="row.local"/><span v-else class="badge">{{cardState(row)}}</span><strong class="balance-line">{{balanceDisplay(row).amount}}</strong><small v-if="balanceDisplay(row).note">{{balanceDisplay(row).note}}</small><small v-if="row.card.error" class="danger-text">{{row.card.error}}</small></td><td><NexovccCardFunding :data="data" :row="row" @details="inspect($event,row.card.serial_no)"/></td><td><button class="slot-link" @click="inspect({type:'accounts'},row.card.serial_no)">{{row.local?.opened_accounts||0}} / {{row.local?.account_limit||cfg.default_max_accounts}}</button><small>在途 {{row.local?.pending_accounts||0}} · 关联 {{associated(row).length}}</small><button class="text-link" :disabled="!row.card.imported||running" @click="editLimit(row)">修改上限</button></td><td><NexovccCardReturns :data="data" :row="row" :cases="associated(row)" @inspect="inspect($event,row.card.serial_no)"/></td><td><NexovccCardAmounts :totals="cardTotals(row)" @details="inspect($event,row.card.serial_no)"/></td><td><div class="row-actions"><button v-if="row.card.status!=='4'" class="text-link" :disabled="running||apiBlocked" @click="perform({action:'refresh',card_id:row.card.id})">查询余额</button><button class="text-link" @click="inspect({type:'trades'},row.card.serial_no)">交易明细</button><CardCopyButton issuer="nexovcc" :card-id="row.card.id" @error="show($event,'error')"/><button class="text-link" @click="manage(row)">管理卡片</button><details class="row-more"><summary>更多操作</summary><div><CardPaymentPolicy issuer="nexovcc" :card-id="row.card.id" :data="data" :disabled="running" @saved="load"/><button v-if="row.card.status!=='4'" class="text-link" :disabled="!!categorizingCard" @click="changeCardCategory(row)">{{categorizingCard===row.card.id?'保存中…':row.card.normal_pending?'恢复正常':'移到正常待定'}}</button><template v-if="row.card.status!=='4'"><button v-for="act in ['withdraw_all','cancel','withdraw_cancel']" :key="act" class="text-link" :disabled="running||apiBlocked||!!cancelReason(row,data.operations)" :title="cancelReason(row,data.operations)" @click="cardAction(row,act)">{{operationNames[act]}}</button></template></div></details></div></td></tr><tr v-if="!visible.length"><td colspan="9" class="empty">{{cfg.enabled?'暂无匹配卡片，可同步已有卡或新建卡片':'先到「配置与模板」连接环球卡台'}}</td></tr></tbody></table></section>
    <section v-else class="card-grid"><article v-for="row in visible" :key="row.card.id" class="visual-card"><header><input v-model="selection" type="checkbox" :value="row.card.id" :disabled="!!cancelReason(row,data.operations)||running" :aria-label="'选择卡片 '+row.card.last4"/><span>环球 · {{row.card.bin}}</span><PaymentCardStatus v-if="row.card.imported" :card="row.local"/><span v-else class="badge">{{cardState(row)}}</span></header><CardTemplateBadge :origin="origin(row)"/><button class="card-number" @click="manage(row)">•••• •••• •••• {{row.card.last4||'待同步'}}</button><small v-if="row.card.normal_pending&&row.card.status!=='4'">正常待定</small><small>开卡 {{at(row.card.created_at)}}</small><small>{{row.card.remark||row.card.serial_no}}</small><div class="card-metrics"><div><span>余额 USD</span><strong>{{balanceDisplay(row).amount}}</strong><small v-if="balanceDisplay(row).note">{{balanceDisplay(row).note}}</small></div><button @click="inspect({type:'accounts'},row.card.serial_no)"><span>开通账号名额 ↗</span><strong>{{row.local?.opened_accounts||0}} / {{row.local?.account_limit||cfg.default_max_accounts}}</strong></button></div><NexovccCardAmounts :totals="cardTotals(row)" @details="inspect($event,row.card.serial_no)"/><div class="card-extra"><section><h3>额度 / 退回 / 差额</h3><NexovccCardFunding :data="data" :row="row" @details="inspect($event,row.card.serial_no)"/></section><section><h3>账号回款</h3><NexovccCardReturns :data="data" :row="row" :cases="associated(row)" @inspect="inspect($event,row.card.serial_no)"/></section></div><footer><button v-if="row.card.status!=='4'" class="text-link" :disabled="running||apiBlocked" @click="perform({action:'refresh',card_id:row.card.id})">查询余额</button><button class="text-link" @click="inspect({type:'trades'},row.card.serial_no)">交易明细</button><CardCopyButton issuer="nexovcc" :card-id="row.card.id" @error="show($event,'error')"/><button class="text-link" @click="manage(row)">管理卡片</button><details class="row-more"><summary>更多操作</summary><div><CardPaymentPolicy issuer="nexovcc" :card-id="row.card.id" :data="data" :disabled="running" @saved="load"/><button v-if="row.card.status!=='4'" class="text-link" :disabled="!!categorizingCard" @click="changeCardCategory(row)">{{categorizingCard===row.card.id?'保存中…':row.card.normal_pending?'恢复正常':'移到正常待定'}}</button><template v-if="row.card.status!=='4'"><button v-for="act in ['withdraw_all','cancel','withdraw_cancel']" :key="act" class="text-link" :disabled="running||apiBlocked||!!cancelReason(row,data.operations)" :title="cancelReason(row,data.operations)" @click="cardAction(row,act)">{{operationNames[act]}}</button></template></div></details></footer></article><div v-if="!visible.length" class="panel empty">暂无匹配卡片</div></section>
   </template>
   <section v-else-if="tab==='returns'" class="panel table-wrap"><NexovccCaseTable @updated="load" :data="data" :rows="visible" :finance="data.finance" :cards="data.cards" :running="running" @state="editReturn" @match="editMatch" @details="caseTrades" @card="manageCase"/></section>
   <section v-else class="panel table-wrap"><div v-if="withdrawAttentionOnly" class="batch-toolbar"><span>仅显示自动提现待核实</span><button class="text-link" @click="withdrawAttentionOnly=false;page=1">查看全部操作记录</button></div><table><thead><tr><th>时间</th><th>操作</th><th>金额 USD</th><th>状态</th><th>详情</th><th></th></tr></thead><tbody><tr v-for="op in visible" :key="op.id"><td>{{at(op.created_at)}}<small>{{op.id}}</small></td><td>{{operationNames[op.action]}}<small>{{op.automatic||op.auto_withdraw||op.capacity_recycle?'自动':'手动'}} · {{op.serial_no||'未返回卡片'}}</small></td><td>{{amount(op.amount)}}</td><td><span class="badge" :class="{good:op.status==='succeeded'}">{{operationStates[op.status]}}</span></td><td class="wrap">{{op.error||(op.evidence?.startsWith('fund:')?'已由同卡资金流水核实到账':op.evidence)||'—'}}<small v-if="op.balance_withdrawal">余额先到账，退款流水待同步；本次仅提现</small></td><td><button v-if="!terminal(op)" class="text-link" :disabled="running" @click="resolve(op)">{{op.status==='unknown'?'核实异常':'查询进度'}}</button></td></tr><tr v-if="!visible.length"><td colspan="6" class="empty">暂无操作记录</td></tr></tbody></table></section>
   <Pagination :page="page" :page-size="pageSize" :total="filtered.length" @update:page="page=$event" @update:page-size="pageSize=$event;page=1"/>
  </template>
  <form v-else class="settings" @submit.prevent="save">
   <section class="panel"><header><h2>连接配置</h2><label class="check"><input v-model="cfg.enabled" type="checkbox"/>启用环球卡台</label></header><p class="note">关闭后停止自动查询，环球卡不能用于新的 Pro 开通；卡台页面仍可手动查询和操作。已保存的凭据留空不修改。</p><div class="fields"><label>登录账号<input v-model="cfg.login" autocomplete="off" :placeholder="cfg.login_present?'已保存，留空不修改':'环球登录账号'"/></label><label>登录密码<input v-model="cfg.password" type="password" autocomplete="new-password" :placeholder="cfg.password_present?'已保存，留空不修改':'登录密码'"/></label><label>资金密码<input v-model="cfg.fund_password" type="password" autocomplete="new-password" :placeholder="cfg.fund_password_present?'已保存，留空不修改':'用于卡密和资金操作'"/></label><label>谷歌验证密钥<input v-model="cfg.totp_secret" type="password" autocomplete="new-password" :placeholder="cfg.totp_present?'已保存，留空不修改':'绑定谷歌验证时填写 Base32 密钥'"/></label><label v-if="cfg.totp_present" class="check"><input v-model="cfg.clear_totp" type="checkbox"/>清除已保存谷歌验证密钥</label></div><div class="actions"><button class="btn primary" :disabled="saving||running">{{saving?'保存中…':'保存配置'}}</button><button type="button" class="btn ghost" :disabled="running||apiBlocked" @click="perform({action:'balance'})">测试连接 / 余额</button><button type="button" class="btn ghost" :disabled="running||apiBlocked" @click="perform({action:'sync'})">同步产品与卡片</button></div></section>
   <section class="panel"><h2>用卡与刷新</h2><div class="fields"><label>默认每卡最大开通数<input v-model.number="cfg.default_max_accounts" type="number" min="1" max="10000" required/></label><label>最多在线卡数<input v-model.number="cfg.max_online_cards" type="number" min="0" max="10000" required/><small>0 不限制；含正常、冻结及销卡中的卡</small></label><label>自动回收触发卡数<input v-model.number="cfg.auto_recycle_at" type="number" min="0" max="10000" required/><small>0 关闭；达到该卡数后自动回收旧卡</small></label><label>500 / 522 重试间隔（秒）<input v-model.number="cfg.http_522_retry_seconds" type="number" min="1" max="300" required/></label><label>500 / 522 重试次数<input v-model.number="cfg.http_522_retries" type="number" min="0" max="10" required/><small>默认重试 2 次；0 不重试，次数用尽停止当次请求</small></label><label>接口请求间隔（秒）<input v-model.number="cfg.request_interval_seconds" type="number" min="1" max="30" required/><small>仅后台自动请求使用；手动操作不等待</small></label><label>批量卡片间隔（秒）<input v-model.number="cfg.batch_interval_seconds" type="number" min="1" max="300" required/><small>默认 3 秒；提现、销卡和批量开卡逐张执行</small></label><label>自动提现检查间隔（秒）<input v-model.number="cfg.auto_withdraw_interval_seconds" type="number" min="30" max="86400" required/><small>独立检查；先更新候选卡余额与流水，再判断提现条件</small></label><label class="check"><input v-model="cfg.auto_withdraw_enabled" type="checkbox"/>自动提现留 1U</label><label class="check"><input v-model="cfg.single_active_account" type="checkbox"/>一卡同时一个号</label></div><p class="note">达到在线上限后，先将最早开卡且已全部回款、无占用的卡提现留 1U 并销卡，到账后间隔 3 秒再开新卡；无可回收卡则等待。成功开通累计计数；自动匹配确认回款后释放同时占用。单张卡可独立调整最大开通数。启用自动回收后，后台从最早已使用、全部回款且无占用的卡开始回收，无需达到开通上限，降到触发卡数以下停止；未使用的新卡保留。自动提现须达到该卡开通上限且余额大于 1 USD；全部回款、无占用时执行。已申请回款且余额先恢复、流水待同步时，无开通或付款任务也可先提现留 1U，仍保留待回款状态与占用。单卡上限优先，不冻结、不销卡；销卡仍须全部回款确认。自动提现按独立间隔检查；同步或其他卡片操作占用时顺延，后台自动请求遵守接口间隔；手动操作立即请求，不受刷新周期、接口冷却和开卡失败冷却影响。HTTP 500 / 522 查询共用设置的间隔和次数重试；开卡、提现等操作则按相同间隔和次数查询原结果，避免重复扣款。结果未知的提现不会重复提交。</p></section>
   <section class="panel"><h2>分项自动同步</h2><p class="note">进入页面只显示已保存的数据，不触发上游查询；以下五项由后台按各自间隔自动同步，“同步全部”可手动立即更新。卡片余额和流水只查正常卡，正常待定与已销卡保留已有数据。</p>
    <div class="fields">
     <label>主账户余额（秒）<input v-model.number="cfg.balance_sync_seconds" type="number" min="60" max="86400" required/><small>主账户可用金额</small></label>
     <label>卡片资料与状态（秒）<input v-model.number="cfg.card_sync_seconds" type="number" min="60" max="86400" required/><small>卡片列表、状态及缺失资料；余额随流水一起同步</small></label>
     <label>卡片余额与交易流水（秒）<input v-model.number="cfg.transaction_sync_seconds" type="number" min="60" max="86400" required/><small>优先已申请待回款卡；逐卡同步余额、交易及资金流水，整卡成功后更新</small></label>
     <label>充值记录与每日汇总（秒）<input v-model.number="cfg.account_sync_seconds" type="number" min="60" max="86400" required/><small>主账户近期充值及平台每日统计</small></label>
     <label>产品 / BIN（秒）<input v-model.number="cfg.product_sync_seconds" type="number" min="60" max="86400" required/><small>开卡产品与可用卡段</small></label>
    </div>
    <p class="note">自动提现使用上面的独立检查间隔，不必等待流水同步周期；仅检查“正常”分类中达到开通上限且无进行中付款、无未确认操作的卡，正常待定和已销卡不查询、不自动提现，再按原有回款条件决定是否提现。最近已更新的数据直接复用，余额不超过 1U 不提现。</p>
    <div class="sync-line" v-for="(name,key) in {balance:'主账户余额',cards:'卡片资料与状态',transactions:'卡片余额与流水',account:'充值与每日汇总',products:'产品 / BIN'}" :key="key"><span>{{name}}</span><span>最近成功 {{at(data.sync?.parts?.[key]?.last_success_at)}}</span><span>下次 {{at(data.sync?.parts?.[key]?.next_refresh_at)}}</span><span v-if="data.sync?.parts?.[key]?.error" class="danger-text">{{data.sync.parts[key].error}}</span></div>
    <button class="btn primary" :disabled="saving||running">保存配置</button>
   </section>
   <section class="panel"><header><h2>开卡模板 <small>{{cfg.templates.length}} 个</small></h2><button type="button" class="btn ghost" @click="addTemplate"><Plus :size="15"/>添加模板</button></header><div v-for="(t,i) in cfg.templates" :key="t.id" class="template-row fields"><label>名称<input v-model="t.name" required maxlength="60" placeholder="例如 Pro 主用卡"/></label><label class="field"><span>平台</span><select :value="t.platform||'oai'" @change="t.platform=$event.target.value"><option value="oai">OAI</option><option value="cc">CC</option></select><small>Pro 仅使用 OAI；CC 用于手动开卡。</small></label><label>适用套餐<select :value="t.plan_code || ''" @change="t.plan_code=$event.target.value"><option value="">未分类（原有模板）</option><option value="pro5">5x</option><option value="pro20">20x</option></select></label><NexovccProductPicker :model-value="templateProductIDs(t)" :products="data.products" :disabled="saving||running" @update:model-value="t.product_ids=$event;t.product_id=$event[0]||''"/><label>开卡充值金额 USD<input v-model="t.amount" type="number" min="0.01" step="0.01" required/></label><label>费用预留 USD<input v-model="t.fee_buffer" type="number" min="0" step="0.01" required/></label><label>持卡人 ID<input v-model="t.holder_id" placeholder="留空自动分配"/></label><button type="button" class="text-link danger-text" @click="cfg.templates.splice(i,1)">删除模板</button></div><p v-if="!cfg.templates.length" class="empty">添加模板后，可在 Pro 配置 → 银行卡与补卡中选择。</p><p class="note">费用预留用于开卡前余额检查，不代表平台实际收费。</p><button class="btn primary" :disabled="saving||running">保存配置与模板</button></section>
  </form>
  <Teleport to="body"><div v-if="detail" class="modal-backdrop" @click.self="detail=null"><section class="modal details-dialog" role="dialog" aria-modal="true" aria-label="环球卡片详情"><div class="modal-heading"><h2>{{detail.type==='accounts'?'账号回款信息':'管理卡片'}} <small v-if="detailRow">· 尾号 {{detailRow.card.last4}}</small></h2><button class="icon-button" aria-label="关闭卡片详情" @click="detail=null"><X :size="18"/></button></div>
   <NexovccProgress v-if="!apiPaused" :job="job" :batch="batch" :busy="busy"/><MessageBar :message="message"/>
   <template v-if="detail.type==='accounts'"><div class="toolbar detail-toolbar"><div class="filters"><button :class="{selected:!detailState}" @click="detailState=''">全部</button><button v-for="(name,state) in returnStates" :key="state" :class="{selected:detailState===state}" @click="detailState=state">{{name}}</button></div><input v-model="detailSearch" placeholder="搜索账号"/></div><NexovccCaseTable @updated="load" :data="data" compact :rows="detailVisible" :finance="data.finance" :cards="data.cards" :running="running" @state="editReturn" @match="editMatch" @details="caseTrades" @card="manageCase"/><Pagination :page="detailPage" :page-size="detailSize" :total="detailCases.length" @update:page="detailPage=$event" @update:page-size="detailSize=$event;detailPage=1"/></template>
   <template v-else-if="detailRow"><div class="manage-header"><div><strong>•••• {{detailRow.card.last4}}</strong><small>开卡 {{at(detailRow.card.created_at)}} · {{detailRow.card.serial_no}}</small></div><div><span>余额</span><strong>{{balanceDisplay(detailRow).amount}} USD</strong><small v-if="balanceDisplay(detailRow).note">{{balanceDisplay(detailRow).note}}</small><small v-else>{{at(detailRow.card.balance_at)}}</small></div><PaymentCardStatus v-if="detailRow.card.imported" :card="detailRow.local"/></div><CardOccupancy :card="detailRow" @updated="load"/><NexovccFinanceSummary :data="data" :serial="detailRow.card.serial_no" @details="inspect($event,detailRow.card.serial_no)"/><div class="card-extra"><section><h3>额度 / 退回 / 差额</h3><NexovccCardFunding :data="data" :row="detailRow" @details="inspect($event,detailRow.card.serial_no)"/></section><section><h3>账号回款</h3><NexovccCardReturns :data="data" :row="detailRow" :cases="associated(detailRow)" @inspect="inspect($event,detailRow.card.serial_no)"/></section></div><div class="actions manage-actions"><button class="btn ghost" @click="inspect({type:'accounts'},detailRow.card.serial_no)">账号回款 {{associated(detailRow).length}}</button><button class="btn ghost" @click="inspect({type:'trades'},detailRow.card.serial_no)">交易明细</button><button class="btn ghost" @click="inspect({type:'funds'},detailRow.card.serial_no)">资金明细</button><button v-if="detailRow.card.status!=='4'" class="btn ghost" :disabled="running||apiBlocked" @click="perform({action:'refresh',card_id:detailRow.card.id})">刷新余额</button><CardPaymentPolicy issuer="nexovcc" :card-id="detailRow.card.id" :data="data" :disabled="running" @saved="load"/><CardCopyButton issuer="nexovcc" :card-id="detailRow.card.id" @error="show($event,'error')"/><button class="btn ghost" :disabled="apiBlocked||!detailRow.card.imported" @click="reveal(detailRow)">查看卡密</button><button class="btn ghost" :disabled="running||!detailRow.card.imported" @click="editLimit(detailRow)">修改开通上限</button><button v-if="detailRow.card.status!=='4'" class="btn ghost" :disabled="!!categorizingCard" @click="changeCardCategory(detailRow)">{{categorizingCard===detailRow.card.id?'保存中…':detailRow.card.normal_pending?'恢复正常':'移到正常待定'}}</button><template v-if="detailRow.card.status!=='4'"><button class="btn ghost" :disabled="running" @click="toggle(detailRow)">{{detailRow.local?.enabled?'禁用':'启用'}}</button><button v-for="act in ['deposit','withdraw','withdraw_all','freeze','unfreeze','remark']" :key="act" class="btn ghost" :disabled="running||apiBlocked||(['withdraw','withdraw_all'].includes(act)&&!!cancelReason(detailRow,data.operations))" @click="cardAction(detailRow,act)">{{operationNames[act]}}</button><button class="btn ghost danger-text" :disabled="running||apiBlocked||!!cancelReason(detailRow,data.operations)" @click="cardAction(detailRow,'cancel')">一键销卡</button><button class="btn primary" :disabled="running||apiBlocked||!!cancelReason(detailRow,data.operations)" @click="cardAction(detailRow,'withdraw_cancel')">提现留 1U 并销卡</button></template></div><p v-if="cancelReason(detailRow,data.operations)" class="note">{{cancelReason(detailRow,data.operations)}}</p></template>
  </section></div></Teleport>
  <Teleport to="body"><div v-if="dialog" class="modal-backdrop" @click.self="closeDialog"><form class="modal nex-dialog" @submit.prevent="submit"><div class="modal-heading"><h2>{{dialog.type==='open'?'开卡':dialog.type==='batch'?'批量'+operationNames[dialog.action]:dialog.type==='match'?'关联交易流水':dialog.type==='return'?'调整回款状态':dialog.type==='limit'?'单卡开通上限':dialog.type==='resolve'?'核实原操作':operationNames[dialog.action]}}</h2><button type="button" class="icon-button" :disabled="dialogBlocked" @click="closeDialog"><X :size="18"/></button></div>
   <template v-if="dialog.type==='open'"><label class="field">开卡模板<select v-model="dialog.template_id" required><option v-for="t in cfg.templates" :key="t.id" :value="t.id">{{t.name}} · {{(t.platform||'oai').toUpperCase()}} · {{t.amount}} USD</option></select></label><label class="field">开卡数量<input v-model.number="dialog.count" type="number" min="1" max="100" required/></label><p>逐张开卡，只有 OAI 卡加入 Pro；遇到失败或未知结果即停止。</p><label class="check"><input v-model="dialog.confirm" type="checkbox" required/>确认从环球账户扣款开卡</label></template>
   <template v-else-if="dialog.type==='batch'"><p>对所选 {{dialog.ids.length}} 张卡执行{{operationNames[dialog.action]}}。</p><p v-if="dialog.action==='withdraw_all'">按每张卡的最新余额提现，保留 1 USD；余额不足的卡自动跳过。</p><p v-else-if="dialog.action==='withdraw_cancel'">自动提现保留 1 USD → 核实到账 → 冻结 → 销卡；操作间隔至少 3 秒。</p><p v-else>卡内余额不超过 1 USD；冻结成功后等待 3 秒自动销卡。</p><p>后台逐张处理，离开页面仍继续。跳过不可操作卡，结果未知时停止后续提交；进度与结果可在这里查看。</p><label class="check"><input v-model="dialog.confirm" type="checkbox" required/>确认执行批量{{operationNames[dialog.action]}}</label></template>
   <template v-else-if="dialog.type==='match'"><p>{{dialog.row.email}} · 尾号 {{dialog.row.last4}}</p><label class="field">同卡交易<select v-model="dialog.group_id" required><option value="">选择核实后的付款流水</option><option v-for="g in matchOptions(dialog.row)" :key="g.id" :value="g.id">{{at(g.at)}} · {{g.payment}} USD · 回款 {{g.returned}} · 授权 {{g.authorization}}</option></select></label><p v-if="!matchOptions(dialog.row).length">没有可关联流水，请先同步；重复或不完整的交易需要核实。</p><label class="field">核实依据<textarea v-model="dialog.reason" minlength="5" maxlength="500" required/></label><label class="check"><input v-model="dialog.confirm" type="checkbox" required/>确认该付款属于此账号</label></template>
   <template v-else-if="dialog.type==='limit'"><label class="field">最大开通数<input v-model.number="dialog.maximum" type="number" min="0" max="10000" required/></label><p>填 0 沿用环球默认上限；已有成功次数和在途占用不会清空。</p></template>
   <template v-else-if="dialog.type==='return'"><p>{{dialog.row.email}} · 尾号 {{dialog.row.last4}}</p><label class="field">回款状态<select v-model="dialog.state"><option v-for="(name,state) in Object.fromEntries(Object.entries(returnStates).filter(([k])=>k!=='no_return'))" :key="state" :value="state">{{name}}</option><option value="auto">恢复自动判断</option></select></label><label class="field">核实依据<textarea v-model="dialog.reason" required minlength="3" maxlength="500" placeholder="例如已在环球后台确认退款到账"/></label><label class="check"><input v-model="dialog.confirm" type="checkbox" required/>已核实该账号实际付款 / 回款状态</label></template>
   <template v-else-if="dialog.type==='resolve'"><p>{{operationNames[dialog.op.action]}} · {{dialog.op.id}}</p><label class="field">处理方式<select v-model="dialog.outcome"><option value="query">查询原操作结果</option><option v-if="dialog.op.action!=='open'" value="succeeded">人工确认已完成并到账</option><option value="failed">人工确认未执行 / 未扣款</option></select></label><label v-if="dialog.op.action==='open'&&!dialog.op.serial_no" class="field">原卡序列号（可留空自动查询）<input v-model="dialog.serial_no" placeholder="留空按原请求编号和产品查找，找到后同步原卡"/></label><label v-if="dialog.outcome!=='query'" class="field">核实依据<textarea v-model="dialog.evidence" minlength="5" maxlength="500" required/></label><label class="check"><input v-model="dialog.confirm" type="checkbox" required/>确认核实原操作，不重复提交资金请求</label></template>
   <template v-else><p>银行卡尾号 {{dialog.card.card.last4}}</p><label v-if="['deposit','withdraw'].includes(dialog.action)" class="field">金额 USD<input v-model="dialog.amount" type="number" min="0.01" step="0.01" required/></label><label v-if="dialog.action==='remark'" class="field">备注<input v-model="dialog.remark" maxlength="40"/></label><p v-if="dialog.action==='withdraw'">提现后须保留 1 USD。核实提现到账后才能继续销卡。</p><p v-if="dialog.action==='withdraw_all'">按最新余额一键提现，保留 1 USD；余额不超过 1 USD 时跳过。</p><p v-if="dialog.action==='cancel'">冻结成功后等待 3 秒自动销卡。卡内余额须不超过 1 USD。</p><p v-if="dialog.action==='withdraw_cancel'">提现保留 1 USD → 核实到账 → 冻结 → 销卡。操作间隔至少 3 秒，剩余余额退回后自动确认完成。</p><label class="check"><input v-model="dialog.confirm" type="checkbox" required/>确认执行{{operationNames[dialog.action]}}</label></template>
   <div class="actions"><button type="button" class="btn ghost" :disabled="dialogBlocked" @click="closeDialog">取消</button><button class="btn primary" :disabled="dialogBlocked">{{dialogBlocked?'处理中…':'确认'}}</button></div>
  </form></div><div v-if="privateInfo" class="modal-backdrop" @click.self="privateInfo=null"><section class="modal nex-dialog"><div class="modal-heading"><h2>卡片信息</h2><button class="icon-button" @click="privateInfo=null"><X :size="18"/></button></div><p class="private-number">{{privateInfo.cardNumber}}</p><p>有效期：{{privateInfo.expMonth}} / {{privateInfo.expYear}}</p><p>CVV：{{privateInfo.cvv}}</p><CardCopyButton issuer="nexovcc" :secret="privateInfo" @error="show($event,'error')"/><small>30 秒后自动隐藏</small></section></div></Teleport>
  <NexovccLedgerDialog v-if="ledger" :data="data" :options="ledger" :page-size="props.defaultPageSize" :syncing="running" :sync-message="ledgerSyncSerial===ledger.serial?(running?job.message:message.text):''" :sync-failed="!running&&message.type==='error'" @sync="syncCardTransactions" @close="ledger=null" @updated="tradePurposeUpdated"/>
 </div>
</template>

<style scoped>
.frozen-breakdown{font-size:11px;line-height:1.7;color:var(--muted);margin-top:8px}.frozen-breakdown summary{cursor:pointer;width:fit-content}.frozen-breakdown p{margin:8px 0}.frozen-breakdown .text-link{font-size:11px;padding:0}

.template-row{align-items:start}
.return-filters{display:flex;align-items:center;gap:8px}.return-filters select{padding:9px 12px;background:var(--surface);color:var(--text);border:1px solid var(--line);border-radius:7px;font-size:12px}

.card-extra{display:grid;grid-template-columns:1fr 1fr;gap:20px;margin:20px 0;border-top:1px solid var(--line);padding-top:16px}.card-extra section{min-width:0}.card-extra h3{font-size:11px;color:var(--muted);font-weight:500;margin:0 0 14px}.visual-card .card-extra{grid-template-columns:1fr}.visual-card .card-extra section+section{border-top:1px solid var(--line);padding-top:14px}@media(max-width:600px){.card-extra{grid-template-columns:1fr}}

.sync-line,.batch-toolbar{display:flex;gap:15px;align-items:center;flex-wrap:wrap;font-size:11px;color:var(--muted)}.batch-toolbar .btn{padding:7px 12px;font-size:12px}.balance-line{display:block;margin-top:10px;font-size:17px;font-variant-numeric:tabular-nums}.card-name,.slot-link{background:none;border:0;color:var(--green-strong);cursor:pointer;padding:0;font-size:15px;font-weight:600}.slot-link{font-size:18px}.card-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(320px,1fr));gap:18px}.visual-card{padding:22px;border:1px solid var(--line);border-radius:14px;background:var(--surface);min-width:0}.visual-card>header{display:flex;gap:10px;align-items:center;font-size:11px;color:var(--muted)}.visual-card>header>span{flex:1}.card-number{display:block;border:0;background:none;color:var(--text);font-size:21px;letter-spacing:1px;padding:24px 0 12px;cursor:pointer}.visual-card small{display:block;line-height:1.8;font-size:11px;color:var(--muted);overflow-wrap:anywhere}.card-metrics{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px;margin:20px 0}.card-metrics>button{background:none;border:0;text-align:left;cursor:pointer;color:var(--green-strong);padding:0}.card-metrics span{display:block;font-size:11px;color:var(--muted);margin-bottom:7px}.card-metrics strong{font-size:21px;font-variant-numeric:tabular-nums}.visual-card>footer{display:flex;flex-wrap:wrap;gap:12px;border-top:1px solid var(--line);padding-top:15px}.details-dialog{width:min(1240px,96vw);max-height:92vh;overflow:auto}.details-dialog h2{font-size:18px}.details-dialog h2 small{font-size:12px;color:var(--muted)}.detail-toolbar{margin:20px 0}.manage-header{display:flex;justify-content:space-between;align-items:center;gap:20px;margin:24px 0}.manage-header strong{display:block;font-size:21px;margin:7px 0}.manage-header small{display:block;font-size:11px;color:var(--muted);line-height:1.7}.manage-header span{font-size:12px;color:var(--muted)}.manage-actions{margin:24px 0}.manage-actions .btn{font-size:12px;padding:9px 13px}@media(max-width:600px){.manage-header{flex-wrap:wrap}.card-grid{grid-template-columns:1fr}}
.nex-page{display:grid;gap:20px}.heading,.heading h1,.actions,.panel>header,.toolbar{display:flex;align-items:center;gap:12px}.heading,.panel>header{justify-content:space-between}.heading h1{font-size:25px;margin:7px 0}.heading p,.note{font-size:12px;color:var(--muted);line-height:1.8}.eyebrow{font-size:10px;letter-spacing:2px;color:var(--green-strong)}.actions{flex-wrap:wrap}.btn{display:inline-flex;align-items:center;gap:7px}.overview{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:16px}.overview article,.panel{background:var(--surface);border:1px solid var(--line);border-radius:12px}.overview article{padding:20px;display:grid;gap:12px}.overview article>span{font-size:12px;color:var(--muted)}.overview strong{font-size:26px;font-variant-numeric:tabular-nums}.overview strong small{font-size:12px;font-weight:400}.overview small,td small{display:block;font-size:11px;color:var(--muted);line-height:1.7}.overview strong small{display:inline}.panel{padding:22px;min-width:0}.panel h2{font-size:15px;margin:0 0 18px}.tabs{display:flex;gap:8px;border-bottom:1px solid var(--line);padding-bottom:10px}.tabs button,.filters button{padding:9px 16px;border:0;border-radius:7px;background:transparent;color:var(--muted);cursor:pointer}.tabs .selected,.filters .selected{background:var(--green-soft);color:var(--green-strong);font-weight:600}.toolbar{justify-content:space-between;flex-wrap:wrap}.toolbar input{max-width:360px;flex:1}.table-wrap{overflow:auto;padding:0}table{width:100%;border-collapse:collapse;font-size:12px;text-align:left}th,td{padding:15px 14px;border-bottom:1px solid var(--line);vertical-align:top}th{background:var(--bg);font-weight:400;color:var(--muted);white-space:nowrap}td{min-width:95px}td:first-child{min-width:0}.row-actions{display:flex;flex-wrap:wrap;gap:10px;max-width:230px}.text-link{background:none;border:0;color:var(--green-strong);font-size:12px;cursor:pointer;padding:2px 0}.text-link:disabled{opacity:.45;cursor:not-allowed}.danger-text{color:var(--red)}.badge{display:inline-block;border-radius:5px;padding:4px 7px;background:var(--bg);color:var(--muted);white-space:nowrap}.badge.good{color:var(--green-strong);background:var(--green-soft)}.case-line{padding:10px 0;white-space:nowrap;border-bottom:1px solid var(--line)}summary{cursor:pointer;color:var(--green-strong)}.wrap{max-width:400px;overflow-wrap:anywhere}.empty{padding:35px;text-align:center;color:var(--muted)}.settings{display:grid;gap:18px;max-width:1250px}.fields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px;margin:20px 0}.fields label,.field{display:grid;gap:9px;font-size:12px}.fields input,.fields select,.toolbar input,.field input,.field select,.field textarea{border:1px solid var(--line);border-radius:7px;background:var(--surface);color:var(--text);padding:10px 12px;width:100%;min-width:0}.check{display:flex!important;align-items:center;gap:9px;font-size:12px}.check input{width:auto;accent-color:var(--green-strong)}.template-row{border-top:1px solid var(--line);padding-top:20px}.progress{display:flex;gap:14px;align-items:center;background:var(--green-soft);padding:16px 20px;border-radius:10px;color:var(--green-strong);font-size:13px}.progress>div{flex:1}.progress small{margin-left:12px}progress{display:block;width:100%;height:5px;margin-top:10px;accent-color:var(--green-strong)}.nex-dialog{width:min(560px,calc(100vw - 32px));max-height:90vh;overflow:auto}.nex-dialog .field,.nex-dialog p,.nex-dialog .check{margin:20px 0}.nex-dialog .actions{justify-content:flex-end;margin-top:24px}.private-number{font-size:23px;font-family:monospace}.note{padding:0 16px}@media(max-width:1000px){.overview{grid-template-columns:repeat(2,minmax(0,1fr))}.heading{align-items:flex-start;flex-wrap:wrap}}@media(max-width:650px){.fields{grid-template-columns:1fr}.tabs{overflow:auto}.tabs button{white-space:nowrap}.overview article{padding:14px}.overview strong{font-size:22px}}
</style>

<style scoped>
.refund-notices{padding:12px 16px;border:1px solid var(--line);border-radius:10px;background:var(--surface);font-size:12px;color:var(--muted)}.refund-notices>summary{display:flex;align-items:center;justify-content:space-between;gap:12px;list-style:none}.refund-notices>summary::-webkit-details-marker{display:none}.refund-notices>summary>span{white-space:nowrap;font-size:11px}.refund-notices .note{padding:0;margin:10px 0 0}.row-more{flex-basis:100%;font-size:12px}.row-more>summary{width:fit-content;cursor:pointer}.row-more>div{display:flex;flex-wrap:wrap;gap:10px;padding:10px 0 0}.nex-page{gap:16px}.overview article{padding:18px;gap:8px;align-content:start}
</style>
