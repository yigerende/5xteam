<script setup>
import { sortCancellationCards } from '../ejh'
import ProOAICaseFlow from './ProOAICaseFlow.vue'
import { sortCardsByReturnState } from '../card-workflow-sort'
import CardPaymentPolicy from './CardPaymentPolicy.vue'
import { applyRefundFeeOverrides } from '../ejh-refund-cost'
import { applyPaymentPolicies } from '../payment-only'
import EJHCardOpenedAt from './EJHCardOpenedAt.vue'
import EJHCardCancelledAt from './EJHCardCancelledAt.vue'
import EJHCardAmounts from './EJHCardAmounts.vue'
import EJHCardFunding from './EJHCardFunding.vue'
import EJHBalanceOverview from './EJHBalanceOverview.vue'
import EJHCaseAmounts from './EJHCaseAmounts.vue'
import EJHCaseState from './EJHCaseState.vue'
import EJHCardTrades from './EJHCardTrades.vue'
import { cardLifecycle } from '../ejh'
import { computed, watch, nextTick, onMounted, onBeforeUnmount, reactive, ref } from 'vue'
import { api } from '../api'
import { createLatestRequest } from '../latestRequest'
import { ejhRefreshGate } from '../ejhRefresh'
import { refreshEJHSync } from '../ejhSync'
import { CreditCard, Plus, Settings2, ArrowUpRight, CheckCircle2, Clock3, CircleDashed, X, ChevronDown, SlidersHorizontal, Search, ShieldCheck, Users, LayoutGrid, List } from 'lucide-vue-next'
import { formatTime } from '../utils'
import { returnStates, returnLabel, tradeKinds, returnTypes, operationStates, caseSummary, cardHasCase, cardMatchesSearch, cardStates, cardState, cardMatchesFilters, readCardView, saveCardView } from '../ejh'
import { newestReturnTrades, returnPostedAt } from '../ejh-trade-sort'
import EJHCurrencySelect from './EJHCurrencySelect.vue'
import { currencySource } from '../ejh-currencies'
import ReturnStatusBadge from './ReturnStatusBadge.vue'
import EJHCardReturnBadge from './EJHCardReturnBadge.vue'
import EJHAccountReturnSummary from './EJHAccountReturnSummary.vue'
import CardAccountPreview from './CardAccountPreview.vue'
import MessageBar from './MessageBar.vue'
import PaymentCardStatus from './PaymentCardStatus.vue'
import CardOccupancy from './CardOccupancy.vue'
import { isClosedCard } from '../card-status'
import Pagination from './Pagination.vue'
import { newestCards, cancelCardReason, runEJHBatch, refreshCancellationProgress } from '../ejh'
import EJHProgress from './EJHProgress.vue'
import CardCopyButton from './CardCopyButton.vue'
import CardTemplateBadge from './CardTemplateBadge.vue'
import { cardOrigin } from '../card-platform'
const props=defineProps({defaultPageSize:{type:Number,default:10},entry:{type:Object,default:null}})
const emit=defineEmits(['entry-applied'])
const cardView=ref(readCardView(window.localStorage))
const targetCardID=ref('')
function setCardView(value){cardView.value=value;saveCardView(window.localStorage,value)}
const tab=ref('cards'),busy=ref(false),ready=ref(false),message=reactive({text:'',type:''})
const settings=reactive({enabled:false,url:'https://www.ejhcard.com',app_key:'',app_secret:'',default_max_accounts:3,single_active_account:true,sync_interval_seconds:300,auto_cancel_enabled:false,auto_cancel_interval_seconds:300,page_refresh_seconds:30,alert_hours:24,reversal_fee_percent:6,templates:[]})
const savedRefreshSeconds=ref(30)
const loading=ref(false),initializing=ref(false),initialError=ref('')
const dataRequest=createLatestRequest(value=>{loading.value=value})
const settingsRequest=createLatestRequest()
const platformFilter=ref('oai')
const origin=row=>cardOrigin(row,data.value.operations,settings.templates)
function setPlatform(value){platformFilter.value=value;targetCardID.value='';page.value=1;selectedCards.value=[]}
const savedEnabled=ref(false)
const data=ref({cards:[],cases:[],trades:[],operations:[],audit:[],balance:null,sync:{},reversal_fee_percent:6})
const accountReturnStates={opened:returnStates.opened,waiting:returnStates.waiting,returned:returnStates.returned,no_return:returnStates.no_return}
const caseCategory=ref('normal')
const caseCounts=computed(()=>({normal:data.value.cases.filter(c=>Object.hasOwn(accountReturnStates,c.state)).length,unopened:data.value.cases.filter(c=>c.state==='unopened').length}))
function setCaseCategory(value){caseCategory.value=value;status.value='';page.value=1}
const query=ref(''),status=ref(''),page=ref(1),pageSize=ref(props.defaultPageSize || 10),detail=ref(null),resolving=ref(null)
const cardStatusFilter=ref(''),cardReturnFilter=ref(''),lifecycleFilter=ref('normal'),tradeDialog=ref(null)
const lifecycleCounts=computed(()=>({normal:data.value.cards.filter(c=>cardLifecycle(c)==='normal').length,normal_pending:data.value.cards.filter(c=>cardLifecycle(c)==='normal_pending').length,closed:data.value.cards.filter(c=>cardLifecycle(c)==='closed').length}))
const categorizingCard=ref('')
async function changeCardCategory(card){
 if(busy.value||categorizingCard.value)return
 categorizingCard.value=card.id
 try{await api('/api/ejh/cards/'+encodeURIComponent(card.id)+'/category',{method:'PUT',body:{category:card.normal_pending?'normal':'normal_pending'}});await load();show('卡片分类已保存')}catch(e){show(e.message,'error')}finally{categorizingCard.value=''}
}
async function refreshPendingCards(){
 await action(async()=>{
  const cards=data.value.cards.filter(c=>c.normal_pending&&origin(c).platform===platformFilter.value&&cardLifecycle(c)==='normal_pending')
  for(const card of cards){if(disposed)break;for(const kind of ['refresh','balance','sync_trades'])await api('/api/ejh/cards/'+encodeURIComponent(card.id)+'/action',{method:'POST',body:{action:kind}})}
  await load();show('正常待定卡片资料、余额和交易已刷新')
 },'刷新正常待定卡')
}
function setLifecycle(value){selectedCards.value=[];lifecycleFilter.value=value;cardStatusFilter.value='';targetCardID.value='';page.value=1}
function showTrades(card){if(!busy.value&&card.issuer_linked!==false)tradeDialog.value.open(card)}
const hasFilters=computed(()=>!!(query.value||status.value||targetCardID.value||cardStatusFilter.value||cardReturnFilter.value||(tab.value==='cards'&&lifecycleFilter.value!=='normal')||(tab.value==='cases'&&caseCategory.value!=='normal')))
watch([cardStatusFilter,cardReturnFilter],()=>{page.value=1;if(cardStatusFilter.value)lifecycleFilter.value=cardStatusFilter.value==='normal_pending'?'normal_pending':['2','8','cancel_pending'].includes(cardStatusFilter.value)?'closed':'normal'},{flush:'sync'})
watch([targetCardID,()=>data.value.cards],()=>{const card=data.value.cards.find(c=>c.id===targetCardID.value);if(card){lifecycleFilter.value=cardLifecycle(card);platformFilter.value=origin(card).platform}},{flush:'sync'})
watch(()=>props.entry,entry=>{
 if(!entry?.email||!entry?.cardID)return
 clearFilters();tab.value='cards';query.value=entry.email;targetCardID.value=entry.cardID
 emit('entry-applied')
},{immediate:true})
function clearFilters(){platformFilter.value='oai';query.value='';status.value='';targetCardID.value='';cardStatusFilter.value='';cardReturnFilter.value='';lifecycleFilter.value='normal';caseCategory.value='normal';page.value=1}
function filterCardStatus(card){const state=cardState(card);cardStatusFilter.value=cardStatusFilter.value===state?'':state}
function filterCardReturn(state){cardReturnFilter.value=cardReturnFilter.value===state?'':state}
const openForm=reactive({template:'',count:1,request_id:'',input:null})
const resolveForm=reactive({order_no:'',outcome:'completed',evidence:''})
const balanceProgress = ref(null), balanceFailures = ref([])
const progress = ref(null), selectedCards = ref([])
const openUncertain = ref(false)
const eligibleVisible = computed(() => visible.value.filter(c => !cancelReason(c)))
function cancelReason(card) { return cancelCardReason(card, data.value.operations, data.value.cases) }
function selectCard(card, checked) {
 if(busy.value || cancelReason(card)) return
 selectedCards.value = checked ? [...new Set([...selectedCards.value,card.id])] : selectedCards.value.filter(id=>id!==card.id)
}
function selectPage(checked) {
 if(busy.value) return
 const ids=eligibleVisible.value.map(c=>c.id)
 selectedCards.value=checked?[...new Set([...selectedCards.value,...ids])]:selectedCards.value.filter(id=>!ids.includes(id))
}
function beginOpenBatch() { changeTab('open');openForm.count=Math.max(2,openForm.count) }
const settingsTab = ref('connection'), managedID = ref('')
const managedCard = computed(() => data.value.cards.find(c => c.id === managedID.value))
const limitID=ref(''),limitValue=ref(0)
const limitCard=computed(()=>data.value.cards.find(c=>c.id===limitID.value))
const tableTabs = [['cards','卡片管理'],['cases','账号回款'],['trades','回款记录'],['operations','操作记录'],['audit','核对审计']]
const statIcons = { no_return: CheckCircle2, unopened: CircleDashed, opened: CreditCard, waiting: Clock3, returned: CheckCircle2 }
const sectionDescriptions = { cards:'查看卡片状态、已开通账号和剩余名额。', cases:'默认显示已开通、已申请待回款和已回款订单；未开通订单可单独查看、删除。', trades:'优先匹配原授权码；无对应授权码时，按同卡开通与回款时间先后自动关联。', operations:'开卡、追加额度和销卡的执行结果。', audit:'保留关联、更正及核实依据，便于追溯。' }
function cardStatus(card) {
 return cardStates[cardState(card)]
}
function capacityPercent(card) { return Math.min(100, ((card.local?.opened_accounts || 0) + (card.local?.pending_accounts || 0)) / (card.local?.account_limit || settings.default_max_accounts) * 100) }
function expiryLabel(value) { return /^\d{4}$/.test(value || '') ? value.slice(0,2) + ' / ' + value.slice(2) : value || '未提供' }
function closeOverlay() { if (busy.value) return; detail.value=null;resolving.value=null;managedID.value='';limitID.value='' }
function revealInvalid(event) {
 event.preventDefault()
 const group=event.target.closest('[data-settings-tab]')
 if(group) settingsTab.value=group.dataset.settingsTab
 for(let parent=event.target.parentElement;parent;parent=parent.parentElement) if(parent.tagName==='DETAILS') parent.open=true
 show(event.target.validationMessage || '请检查输入内容','error')
 nextTick(()=>event.target.focus())
}
function keydown(event) { if(event.key==='Escape') closeOverlay() }
let disposed=false
const tabs=[['cards','卡片管理'],['cases','账号回款'],['trades','回款记录'],['operations','操作记录'],['audit','核对审计'],['open','开卡'],['settings','配置与模板']]
const filtered=computed(()=>(tab.value==='cards'?(lifecycleFilter.value==='closed'?sortCancellationCards(newestCards(data.value.cards),data.value.operations):sortCardsByReturnState(newestCards(data.value.cards),data.value.cases)):tab.value==='trades'?newestReturnTrades(data.value.trades):data.value[tab.value]||[]).filter(row=>{
 if(tab.value==='cases'&&(caseCategory.value==='unopened'?row.state!=='unopened':!Object.hasOwn(accountReturnStates,row.state)))return false
 if(tab.value==='cards')return (targetCardID.value||origin(row).platform===platformFilter.value)&&(!!targetCardID.value||cardLifecycle(row)===lifecycleFilter.value)&&cardMatchesSearch(row,data.value.cases,query.value,targetCardID.value)&&cardMatchesFilters(row,data.value.cases,cardStatusFilter.value,cardReturnFilter.value)
 else if(query.value&&!JSON.stringify(row).toLowerCase().includes(query.value.trim().toLowerCase()))return false
 if(!status.value)return true
 if(tab.value==='cases')return row.state===status.value
 if(tab.value==='trades')return status.value==='unmatched'?returnTypes.has(row.trade.tradeType)&&!row.payment_order_id:status.value==='returned'?row.confirmed:status.value==='pending'?returnTypes.has(row.trade.tradeType)&&!row.confirmed:!returnTypes.has(row.trade.tradeType)
 return true
}))
const visible=computed(()=>filtered.value.slice((page.value-1)*pageSize.value,page.value*pageSize.value))
const counts=computed(()=>Object.fromEntries(Object.keys(returnStates).map(k=>[k,data.value.cases.filter(c=>c.state===k).length])))
function changeTab(v){tab.value=v;clearFilters();detail.value=null}
function showCaseState(value){changeTab('cases');if(value==='unopened')setCaseCategory('unopened');else status.value=value}
async function deleteUnopenedCase(row=null){
 if(busy.value||row&&row.state!=='unopened')return
 const count=row?1:caseCounts.value.unopened
 if(!count)return
 const target=row?`${row.email} 的这条未开通订单（${row.id}）`:`全部 ${count} 条未开通订单（包含其他分页和搜索范围外的记录）`
 if(!window.confirm(`确认删除${target}？\n删除表示你已人工核实未开通，将结束这笔原订单并解除关联占用。原账号、订单证据和资金流水保留；正在执行或已有付款成功证据的记录不会被结束。`))return
 await action(async()=>{
  const result=await api(row?'/api/ejh/cases/'+encodeURIComponent(row.id):'/api/ejh/cases/unopened',{method:'DELETE',body:{confirm:true}})
  const deleted=new Set(result.deleted_ids)
  data.value.cases=data.value.cases.filter(c=>!deleted.has(c.id))
  const clampPage=()=>{page.value=Math.min(page.value,Math.max(1,Math.ceil(filtered.value.length/pageSize.value)))}
  clampPage()
  const text=`已删除 ${result.deleted} 条未开通订单记录`
  try{await load();clampPage();show(text)}catch(e){show(text+'；刷新失败：'+e.message,'error')}
 },row?'删除未开通订单':'删除全部未开通订单')
}
function show(text,type='success'){message.text=text;message.type=type}
async function action(fn,label='正在处理，请稍候'){
 if(busy.value)return
 busy.value=true;message.text=''
 progress.value={label,phase:'正在请求服务器',startedAt:Date.now(),elapsed:0,total:0,done:0,success:0,failed:0,unknown:0,results:[],stopRequested:false,stopped:false,finished:false}
 const timer=setInterval(()=>{progress.value.elapsed=Math.floor((Date.now()-progress.value.startedAt)/1000)},1000)
 try{await fn()}catch(e){show(e.message,'error');progress.value.phase=e.message}
 finally{clearInterval(timer);busy.value=false;progress.value.finished=true;if(progress.value.phase==='正在请求服务器')progress.value.phase=message.text||'操作完成'}
}
function load({background=false}={}){return dataRequest.run(signal=>api('/api/ejh/dashboard',{signal,timeoutMs:15000}),result=>{if(!disposed){data.value=applyRefundFeeOverrides(applyPaymentPolicies(result));if(detail.value?.cardID&&detail.value?.cases){const card=data.value.cards.find(c=>c.id===detail.value.cardID);if(card)detail.value.cases=cardCases(card)}selectedCards.value=selectedCards.value.filter(id=>result.cards.some(c=>c.id===id&&!cancelReason(c)));if(progress.value?.cancellation){refreshCancellationProgress(progress.value,result.operations);show(progress.value.phase,progress.value.failed?'error':progress.value.processing||progress.value.unknown?'info':'success')}if(openUncertain.value&&result.operations?.some(o=>o.id===openForm.request_id&&['completed','resolved','failed'].includes(o.status))){openForm.request_id='';openUncertain.value=false}}},{background})}
async function save(){await action(async()=>{const result=await api('/api/ejh/settings',{method:'PUT',body:settings});Object.assign(settings,result,{app_secret:''});savedEnabled.value=result.enabled===true;savedRefreshSeconds.value=result.page_refresh_seconds||30;await load();show('EJH 配置已保存')})}
async function test(){await action(async()=>{await api('/api/ejh/test',{method:'POST',body:{}});await load();show('连接成功，余额已更新（使用已保存配置）')})}
async function refreshCardsAndTrades(){
 const result=await refreshEJHSync({api,stopped:()=>disposed||progress.value.stopRequested,onProgress:text=>{if(!disposed)progress.value.phase=text}})
 if(result.stopped){progress.value.stopped=true;progress.value.phase='后续查询已停止，已提交的同步任务在后台完成'}
 return result
}
async function refreshAllBalances(includeSync = false, automatic = false) {
 if(busy.value || disposed || (automatic && !savedEnabled.value))return
 const lease=ejhRefreshGate.begin(automatic,savedRefreshSeconds.value)
 if(lease.reason){
  if(lease.reason==='running'){
   show('上次刷新仍在进行，完成后读取最新结果。')
   await lease.done
   if(!disposed)try{await load();show('上次刷新已结束，已读取保存结果；如有失败可手动重试。')}catch(e){show(e.message,'error')}
  }else show(savedRefreshSeconds.value+' 秒内已尝试刷新，当前显示已保存数据；可手动刷新重试。')
  return
 }
 try {
 await action(async () => {
  balanceFailures.value=[];progress.value.phase=includeSync===true?'正在读取卡片与交易并自动对账':'正在读取卡片列表'
  if(includeSync === true) {
   progress.value.total=1
   try { const result=await refreshCardsAndTrades();if(result.stopped)return;progress.value.success++ }
   catch(e) { progress.value.failed++;balanceFailures.value.push({kind:'sync',label:'卡片与交易更新',error:e.message}) }
   progress.value.done++
  }
  if(disposed||progress.value.stopRequested){progress.value.stopped=true;progress.value.phase='后续查询已停止';return}
  await load()
  if(disposed||progress.value.stopRequested){progress.value.stopped=true;progress.value.phase='后续查询已停止';return}
  const cards=data.value.cards.filter(card=>card.issuer_linked!==false&&!card.normal_pending&&!historicalBalance(card))
  balanceProgress.value={done:0,total:cards.length+1};progress.value.total=cards.length+1+(includeSync===true?1:0);progress.value.phase='正在查询账户余额'
  try {
   try { await api('/api/ejh/balance',{method:'POST',body:{}});progress.value.success++ }
   catch(e) { progress.value.failed++;balanceFailures.value.push({kind:'account_balance',label:'账户余额',error:e.message}) }
   balanceProgress.value.done++;progress.value.done++
   await load()
   for(const card of cards) {
    if(disposed||progress.value.stopRequested){progress.value.stopped=true;break}
    progress.value.phase='正在查询 '+balanceProgress.value.done+' / '+cards.length+'：'+(card.local?.name||card.remote.orderNo)
    try { await api('/api/ejh/cards/'+encodeURIComponent(card.id)+'/action',{method:'POST',body:{action:'balance'}});progress.value.success++ }
    catch(e) { progress.value.failed++;balanceFailures.value.push({kind:'card_balance',cardID:card.id,label:card.local?.name || card.remote.orderNo,error:e.message}) }
    balanceProgress.value.done++;progress.value.done++
   }
   if(!disposed) await load()
   progress.value.phase=progress.value.stopped?'后续查询已停止':'查询完成'
   show(progress.value.stopped?'后续查询已停止，已完成结果保留。':balanceFailures.value.length ? '刷新完成，'+balanceFailures.value.length+' 项失败，保留已有数据，可点击重试。' : '账户及未进入销卡的 '+cards.length+' 张卡片余额已刷新并保存。',balanceFailures.value.length?'error':'success')
  } finally { balanceProgress.value=null }
 },'刷新数据')
 } finally { lease.finish() }
}
async function refreshPage(automatic = false) { if(!disposed && (automatic!==true || savedEnabled.value)) await refreshAllBalances(true, automatic === true) }
async function retryFailedRefresh() {
 if(busy.value||disposed||!ready.value||!balanceFailures.value.length)return
 const lease=ejhRefreshGate.begin(false,savedRefreshSeconds.value)
 if(lease.reason){show('上次刷新仍在进行，请完成后重试失败项。','info');return}
 const failed=[...balanceFailures.value]
 try {
  await action(async()=>{
   balanceProgress.value={done:0,total:failed.length};progress.value.total=failed.length
   for(const item of failed){
    if(disposed||progress.value.stopRequested){progress.value.stopped=true;break}
    progress.value.phase='正在重试：'+item.label
    try {
     if(item.kind==='sync'){const result=await refreshCardsAndTrades();if(result.stopped)break}
     else if(item.kind==='account_balance')await api('/api/ejh/balance',{method:'POST',body:{}})
     else if(item.kind==='card_balance'&&item.cardID)await api('/api/ejh/cards/'+encodeURIComponent(item.cardID)+'/action',{method:'POST',body:{action:'balance'}})
     else throw new Error('无法识别失败项目，请手动刷新数据。')
     balanceFailures.value=balanceFailures.value.filter(f=>f!==item);progress.value.success++
    }catch(e){item.error=e.message;progress.value.failed++}
    balanceProgress.value.done++;progress.value.done++
   }
   if(!disposed)await load()
   progress.value.phase=progress.value.stopped?'失败项重试已停止':'失败项重试完成'
   show(progress.value.phase+'，成功 '+progress.value.success+' 项'+(balanceFailures.value.length?'，仍有 '+balanceFailures.value.length+' 项保留待重试。':'。'),balanceFailures.value.length?'error':'success')
  },'重试失败项')
 }finally{balanceProgress.value=null;lease.finish()}
}
async function initialize() {
 if(initializing.value||disposed)return
 initializing.value=true;initialError.value=''
 try {
  await Promise.all([
   settingsRequest.run(signal=>api('/api/ejh/settings',{signal,timeoutMs:15000}),cfg=>{Object.assign(settings,cfg);savedEnabled.value=cfg.enabled===true;savedRefreshSeconds.value=cfg.page_refresh_seconds||30}),
   load(),
  ])
  if(!disposed)ready.value=true
 } catch(e) { if(!disposed)initialError.value=e.message }
 finally { initializing.value=false }
 if(ready.value && !disposed && !initialError.value) await refreshPage(true)
}
function pollSavedData(){if(ready.value&&savedEnabled.value&&!disposed)load({background:true}).catch(e=>{if(!disposed)show(e.message,'error')})}
function addTemplate(){settings.templates.push({id:crypto.randomUUID(),platform:'oai',name:'新模板',plan_code:'',valid_days:30,cardType:'',cardAmount:'',cardCurrency:'USD',billCurrency:'USD',useTime:1,companyRate:'0.01',activeDate:'',supportedMccGroup:'ecom',extInfoyx:'',OrderTitle:'',OrderGuestName:'',OrderActiveDate:'',OrderExpiryDate:'',amount:''})}
function chooseTemplate(){if(busy.value||openUncertain.value)return;const t=settings.templates.find(t=>t.id===openForm.template);if(!t){openForm.input=null;return}const d=new Date(Date.now()+t.valid_days*86400000);openForm.input={...t,activeDate:d.toLocaleDateString('sv-SE',{timeZone:'Asia/Shanghai'})};delete openForm.input.id;delete openForm.input.name;delete openForm.input.valid_days;delete openForm.input.plan_code;delete openForm.input.platform;openForm.request_id=''}
async function openCards(){
 if(busy.value)return
 if(openUncertain.value)return show('上次开卡结果待核实，请先到操作记录核实后再开卡','error')
 if(!openForm.input||!Number.isInteger(openForm.count)||openForm.count<1||openForm.count>50)return show('请选择模板，数量须为 1～50','error')
 const input=JSON.parse(JSON.stringify(openForm.input)),count=openForm.count,templateID=openForm.template
 if(!window.confirm('确认真实开卡 '+count+' 张？每张额度 '+input.cardAmount+' '+input.cardCurrency+'，另有卡台费用。结果未知时停止，不自动重试。'))return
 await action(async()=>{
  const items=Array.from({length:count},(_,i)=>({label:'开卡 '+(i+1),request_id:i===0&&openForm.request_id?openForm.request_id:crypto.randomUUID()}))
  await runEJHBatch(items,progress.value,async item=>{
   openForm.request_id=item.request_id;openUncertain.value=true
   const result=await api('/api/ejh/open',{method:'POST',body:{request_id:item.request_id,input,template_id:templateID}})
   if(['completed','failed'].includes(result.status)){openForm.request_id='';openUncertain.value=false}
   return result
  },()=>disposed)
  page.value=1
  show('已开卡 '+progress.value.success+' 张；未提交 '+(count-progress.value.done)+' 张。'+(progress.value.unknown?'有结果待核实，请到操作记录核对。':progress.value.failed?'有开卡失败，详见逐项结果。':''),progress.value.unknown||progress.value.failed?'error':'success')
  await load()
 },'批量开卡')
}
async function cancelSelected(){
 if(busy.value)return
 const ids=[...selectedCards.value]
 if(!ids.length||ids.length>50)return show('请选择 1～50 张可销卡的卡片','error')
 const names=ids.map(id=>{const c=data.value.cards.find(c=>c.id===id);return c?.local?.name||c?.remote?.cardNo||id})
 if(!window.confirm('确认向 EJH 申请销卡 '+ids.length+' 张？\n'+names.join('、')+'\n销卡后不能再用于开通。将逐张提交；未知结果会停止后续操作。'))return
 await action(async()=>{
  progress.value.phase='正在核对所选卡片的最新状态'
  await load()
  const cards=ids.map(id=>data.value.cards.find(c=>c.id===id))
  if(cards.some(c=>!c||cancelReason(c)))throw new Error('所选卡片状态已变化，包含不可销卡项；请重新选择后提交')
  const items=cards.map(c=>({id:c.id,method:'cancel',label:c.local?.name||c.remote.cardNo,request_id:crypto.randomUUID()}))
  progress.value.cancellation=true
  await runEJHBatch(items,progress.value,async item=>{
   const result=await api('/api/ejh/cards/'+encodeURIComponent(item.id)+'/action',{method:'POST',body:{action:'cancel',request_id:item.request_id,confirm:true}})
   if(['completed','resolved','processing'].includes(result.status))selectedCards.value=selectedCards.value.filter(id=>id!==item.id)
   return result
  },()=>disposed)
  refreshCancellationProgress(progress.value)
  show(progress.value.phase,progress.value.failed?'error':progress.value.processing||progress.value.unknown?'info':'success')
  await load()
 },'批量销卡')
}
function historicalBalance(card){return isClosedCard(card)||cardState(card)==='8'}
function cardCases(card){return data.value.cases.filter(c=>cardHasCase(card,c))}
function hoverAccounts(card){return cardCases(card).slice(0,30).map(caseSummary).join('\n')||'暂无关联账号'}
function showAccounts(card){detail.value={title:(card.local?.name||card.remote.orderNo)+' · 账号回款',cardID:card.id,cases:cardCases(card)}}
async function caseStateUpdated(row){
 data.value.cases=data.value.cases.map(c=>c.id===row.id?row:c)
 if(detail.value?.cases)detail.value.cases=detail.value.cases.map(c=>c.id===row.id?row:c)
 show('账号回款状态已保存，卡片占用与销卡条件已更新')
 try{await load();if(detail.value?.cardID){const card=data.value.cards.find(c=>c.id===detail.value.cardID);if(card)detail.value.cases=cardCases(card)}}
 catch(e){show('状态已保存，刷新卡片信息失败：'+e.message,'error')}
}
async function cardAction(card,act){
 if(busy.value)return
 if(act==='cancel'&&cancelReason(card))return show(cancelReason(card),'error')
 if(card.issuer_linked===false)return show('此卡仅标记为 EJH，未绑定卡台订单，不能调用卡台接口','error')
 let amount=''
 if(act==='increase'){amount=window.prompt('输入追加刷卡额度（不是目标总额）');if(!amount)return}
 if(['increase','cancel'].includes(act)&&!window.confirm(act==='cancel'?'确认向 EJH 申请销卡？卡台处理需要时间，提交后会自动查询结果。':'确认追加 '+amount+' '+card.remote.cardCurrency+'？可能产生费用。'))return
 await action(async()=>{
  const requestID=crypto.randomUUID()
  if(act==='cancel'){
   progress.value.cancellation=true
   progress.value.total=1;progress.value.done=1
   progress.value.results=[{label:card.local?.name||card.remote.cardNo||'销卡',request_id:requestID,status:'unknown',error:''}]
   try{
    const result=await api('/api/ejh/cards/'+encodeURIComponent(card.id)+'/action',{method:'POST',body:{action:act,amount,request_id:requestID,confirm:true}})
    refreshCancellationProgress(progress.value,[{...result,id:requestID,method:'cancel'}])
   }catch(e){progress.value.results[0].error=e.message;if(e.status>=400&&e.status<500)progress.value.results[0].status='failed';refreshCancellationProgress(progress.value)}
   show(progress.value.phase,progress.value.failed?'error':progress.value.processing||progress.value.unknown?'info':'success')
   await load()
   return
  }
  const result=await api('/api/ejh/cards/'+encodeURIComponent(card.id)+'/action',{method:'POST',body:{action:act,amount,request_id:requestID,confirm:true}})
  await load()
  if(act==='details')detail.value={title:'卡片敏感信息（关闭即清除）',data:result}
  else if(act==='increase'&&!['completed','resolved'].includes(result.status))throw new Error(operationStates[result.status]+'：'+(result.error||'请核对操作记录'))
  else if(act==='balance'&&result.cached)show(result.processing?'销卡处理中，当前显示历史余额；销卡结果由后台自动跟踪。':'当前显示销卡前保存的历史余额。')
  else show('操作已完成')
 },{details:'查看卡密',refresh:'刷新卡片',balance:'查询卡片余额',increase:'追加额度',cancel:'销卡'}[act])
}
function editLimit(card){
 if(busy.value||!card.local)return
 managedID.value='';limitID.value=card.id;limitValue.value=card.local.max_accounts||0
}
async function saveLimit(){
 if(!limitCard.value||busy.value)return
 const n=Number(limitValue.value),id=limitCard.value.id
 if(limitValue.value===''||!Number.isInteger(n)||n<0||n>10000)return show('请输入 0～10000 的整数','error')
 await action(async()=>{
  await api('/api/gptpay/cards/'+encodeURIComponent(id),{method:'PUT',body:{max_accounts:n}})
  await load();limitID.value='';show('单卡最大开通数已保存，剩余名额已更新')
 },'保存单卡最大开通数')
}
async function toggle(card){if(isClosedCard(card))return;await action(async()=>{await api('/api/gptpay/cards/'+encodeURIComponent(card.id),{method:'PUT',body:{enabled:!card.local.enabled}});await load()})}
function resolve(row){resolving.value=row;Object.assign(resolveForm,{order_no:row.order_no||'',outcome:'completed',evidence:''})}
async function saveResolve(){await action(async()=>{await api('/api/ejh/operations/'+resolving.value.id+'/resolve',{method:'POST',body:resolveForm});resolving.value=null;await load();show('核实结果已保存')})}
function overdue(c){return c.state==='waiting'&&c.requested_at&&Date.now()-Date.parse(c.requested_at)>settings.alert_hours*3600000}
onMounted(()=>window.addEventListener('keydown',keydown))
onMounted(initialize)
let savedDataTimer
onMounted(()=>{savedDataTimer=setInterval(pollSavedData,5000)})
onBeforeUnmount(()=>clearInterval(savedDataTimer))
onBeforeUnmount(()=>{window.removeEventListener('keydown',keydown);disposed=true;dataRequest.dispose();settingsRequest.dispose();settings.app_secret='';detail.value=null})
</script>
<template>
 <div class="view-stack ejh-view">
  <header class="ejh-heading"><div><span class="overline">CARD OPERATIONS</span><h1>EJH 卡台</h1><p>卡片、开通账号与回款记录，一个地方查看。</p></div><div class="ejh-toolbar"><button class="btn ghost" :class="{selected:tab==='settings'}" @click="changeTab('settings')"><Settings2 :size="16"/>配置与模板</button><button class="btn primary" @click="changeTab('open')"><Plus :size="16"/>新建卡片</button></div></header>
  <p v-if="initializing&&!ready" class="muted" role="status">正在读取已保存数据…</p>
  <div v-if="initialError" class="ejh-toolbar" role="alert"><span class="danger-text">{{initialError}}</span><button class="btn ghost" :disabled="initializing" @click="initialize">重新读取</button></div>
  <MessageBar v-if="message.text" :message="message"/><EJHProgress v-if="progress" :progress="progress" @stop="progress.stopRequested=true" @dismiss="progress=null"/>
  <div v-show="tab!=='settings'&&tab!=='open'" class="ejh-stats"><button v-for="(label,key) in returnStates" :key="key" :class="['ejh-stat',key,{selected:tab==='cases'&&(key==='unopened'?caseCategory==='unopened':caseCategory==='normal'&&status===key)}]" @click="showCaseState(key)"><span class="ejh-stat-icon"><component :is="statIcons[key]" :size="21"/></span><span class="ejh-stat-value"><ReturnStatusBadge :state="key"/><strong>{{counts[key]||0}}<em>条订单</em></strong></span><ArrowUpRight :size="16"/></button></div>
  <EJHBalanceOverview v-show="tab!=='settings'&&tab!=='open'" :data="data" :busy="busy" :ready="ready" :enabled="savedEnabled" :progress="balanceProgress" :failures="balanceFailures" @refresh="refreshPage" @retry-failed="retryFailedRefresh" @ledger-updated="data.recharge_ledger=$event" @refund-fee-saved="load"/>
  <div class="ejh-statusbar"><span class="ejh-connection"><i :class="{enabled:settings.enabled}"></i>{{settings.enabled?'接口已启用':'接口未启用'}}</span><span class="ejh-last-sync">最近更新 {{formatTime(data.sync?.last_success)}}</span><span>{{settings.enabled?'后台每 '+settings.sync_interval_seconds+' 秒自动对账':'接口未启用，显示保存数据'}} · 销卡退回确认后停止查询</span></div><p v-if="data.sync?.error" class="danger-text">最近更新有异常：{{data.sync.error}}。成功项目已保存，失败项目保留旧数据，将按配置间隔重试。</p>
  <nav class="ejh-tabs" aria-label="EJH 管理分区"><button v-for="[id,label] in tableTabs" :key="id" :aria-current="tab===id?'page':undefined" :class="{active:tab===id}" @click="changeTab(id)">{{label}}<span v-if="id==='cards'">{{data.cards.length}}</span></button></nav>
  <form v-if="tab==='settings'" class="panel ejh-settings" @submit.prevent="save" @invalid.capture="revealInvalid">
   <div class="ejh-section-heading"><div><span class="overline">CONFIGURATION</span><h2>配置与模板</h2><p>接口凭证与开卡参数分开管理，修改后统一保存。</p></div></div>
   <nav class="ejh-settings-nav" aria-label="EJH 配置分组"><button type="button" :class="{active:settingsTab==='connection'}" @click="settingsTab='connection'">接口与更新</button><button type="button" :class="{active:settingsTab==='templates'}" @click="settingsTab='templates'">开卡模板 <span>{{settings.templates.length}}</span></button></nav>
   <section v-show="settingsTab==='connection'" data-settings-tab="connection"><div class="ejh-grid"><label class="toggle-row wide"><div><strong>启用 EJH 接口</strong><small>关闭后 EJH 卡不能用于新的 Pro 开通；开启后按下方间隔更新卡片及回款，自动开卡需单独配置。</small></div><input v-model="settings.enabled" type="checkbox"/><i></i></label><label class="field"><span>HTTPS 地址</span><input v-model="settings.url" required/></label><label class="field"><span>AppKey</span><input v-model="settings.app_key" autocomplete="off"/></label><label class="field"><span>AppSecret</span><input v-model="settings.app_secret" type="password" autocomplete="new-password" :placeholder="settings.secret_present?'已保存，留空不修改':'填写密钥'"/></label><label class="field"><span>EJH 每卡默认最大开通数</span><input v-model.number="settings.default_max_accounts" type="number" min="1" max="10000" required/></label><label class="toggle-row wide"><div><strong>一卡同时一个号</strong><small>仅限 EJH 卡，手动与全自动共用限制。从开通到回款确认持续占用；授权撤销或退款到账后即可继续开通，累计开通数不清零。仅合并、移出成功仍继续占用。关闭后仍受最大开通数限制。</small></div><input v-model="settings.single_active_account" type="checkbox"/><i></i></label><label class="field"><span>自动更新间隔（秒）</span><input v-model.number="settings.sync_interval_seconds" type="number" min="60" max="86400" required/></label><label class="toggle-row wide"><div><strong>自动销卡</strong><small>仅正常、未冻结的 OAI 卡片参与。达到累计开通上限，所有关联账号已回款且本金流水核实到账后，自动提交销卡。正常待定、销卡中、已销卡、死号及未决操作跳过；不设卡片数量门槛。</small></div><input v-model="settings.auto_cancel_enabled" type="checkbox"/><i></i></label><label class="field"><span>自动销卡检查间隔（秒）</span><input v-model.number="settings.auto_cancel_interval_seconds" type="number" min="60" max="86400" required/><small>默认 300 秒，后台独立检查，关闭页面仍执行。结果不明确时只查询原申请，不重复销卡；在操作记录查看自动销卡进度。</small></label><label class="field"><span>页面刷新间隔（秒）</span><input v-model.number="settings.page_refresh_seconds" type="number" min="5" max="86400" required/><small>默认 30 秒。间隔内重复进入卡台不重复查询；成本统计、财务对账按此间隔读取保存数据，手动刷新不受限制。</small></label><label class="field"><span>撤销 / 退款手续费比例（%）</span><input v-model.number="settings.reversal_fee_percent" type="number" min="0" max="100" step="0.01" required aria-label="撤销 / 退款手续费比例（%）"/><small>自定义 0～100%，支持两位小数。已回款账号保留回款成功时的费率；未回款（包括申请中、待回款）按最新设置计算，回款成功后锁定。撤销按撤销金额估算；退款优先按实际 USD 结算退款金额计算，逐笔计入手续费。</small></label><label class="field"><span>待回款提醒（小时）</span><input v-model.number="settings.alert_hours" type="number" min="1" max="8760" required/></label></div></section>
   <p v-if="tab==='settings' && settingsTab==='connection'" class="note">自动销卡{{settings.auto_cancel_enabled?'已开启':'已关闭'}} · 最近检查 {{formatTime(data.auto_cancel?.last_attempt)}} · 本轮提交 {{data.auto_cancel?.submitted||0}} 张<span v-if="data.auto_cancel?.error" class="danger-text"> · {{data.auto_cancel.error}}</span></p>
   <section v-show="settingsTab==='templates'" data-settings-tab="templates"><div class="ejh-section-heading"><div><h3>已保存模板</h3><p>卡类型由 EJH 分配，点击模板展开编辑。红色 <span class="ejh-required" aria-hidden="true">*</span> 为必填项（含默认选项）。</p><p>币种从 <a :href="currencySource" target="_blank" rel="noopener noreferrer">EJH 官方币种表</a> 选择（代码 · 中文名称 · 地区）；实际可用币种以账户及卡类型权限为准。</p></div><button class="btn ghost" type="button" @click="addTemplate"><Plus :size="15"/>增加模板</button></div><details v-for="(t,index) in settings.templates" :key="t.id" class="ejh-template"><summary><CreditCard :size="18"/><span><strong>{{t.name || '新模板'}}</strong><small>{{t.cardAmount || '待设置额度'}} {{t.cardCurrency}} · {{t.valid_days}} 天</small></span><ChevronDown :size="16"/></summary><div class="template-body"><div class="ejh-grid"><label class="field"><span>模板名称 <span class="ejh-required" aria-hidden="true">*</span></span><input v-model="t.name" required/></label><label class="field"><span>平台</span><select :value="t.platform||'oai'" @change="t.platform=$event.target.value"><option value="oai">OAI</option><option value="cc">CC</option></select><small>Pro 仅使用 OAI；CC 用于手动开卡。</small></label><label class="field"><span>适用套餐</span><select :value="t.plan_code || ''" @change="t.plan_code=$event.target.value"><option value="">未分类（原有模板）</option><option value="pro5">5x</option><option value="pro20">20x</option></select><small>用于 Pro 分时开通匹配模板。</small></label><label class="field"><span>卡类型 cardType <span class="ejh-required" aria-hidden="true">*</span></span><input v-model="t.cardType" required/></label><label class="field"><span>刷卡额度 <span class="ejh-required" aria-hidden="true">*</span></span><input v-model="t.cardAmount" inputmode="decimal" required/></label><label class="field"><span>卡币种 <span class="ejh-required" aria-hidden="true">*</span></span><EJHCurrencySelect label="卡币种" v-model="t.cardCurrency" required/></label><label class="field"><span>结算币种 <span class="ejh-required" aria-hidden="true">*</span></span><EJHCurrencySelect label="结算币种" v-model="t.billCurrency" required/></label><label class="field"><span>有效天数 <span class="ejh-required" aria-hidden="true">*</span></span><input v-model.number="t.valid_days" type="number" min="1" max="3650" required/></label><label class="field"><span>使用方式 <span class="ejh-required" aria-hidden="true">*</span></span><select v-model.number="t.useTime" required><option :value="1">多次</option><option :value="0">单次</option></select></label><label class="field"><span>风控订单类别 supportedMccGroup <span class="ejh-required" aria-hidden="true">*</span></span><select v-model="t.supportedMccGroup" required><option value="ecom">ecom · 跨境电商</option><option value="flight">flight · 机票</option><option value="hotel">hotel · 酒店</option><option value="trv">trv · 非酒店机票</option></select></label><label class="field"><span>汇率加成（小数）</span><input v-model="t.companyRate"/></label><label class="field"><span>备注</span><input v-model="t.extInfoyx" maxlength="100"/></label></div><details><summary>可选订单参数</summary><div class="ejh-grid"><label class="field"><span>授权次数（留空不限）</span><input :value="t.authCount" type="number" min="1" @input="t.authCount=$event.target.value?Number($event.target.value):null"/></label><label v-for="[key,label] in [['OrderTitle','订单标题'],['OrderGuestName','客人姓名'],['OrderActiveDate','订单生效日期'],['OrderExpiryDate','订单失效日期'],['amount','订单金额']]" :key="key" class="field"><span>{{label}}</span><input v-model="t[key]"/></label></div></details><button class="btn ghost danger-text" type="button" @click="settings.templates.splice(index,1)">移除模板</button></div></details><div v-if="!settings.templates.length" class="ejh-empty"><CreditCard :size="28"/><strong>还没有开卡模板</strong><p>先添加模板，之后可用于手动开卡或 Pro 自动补卡。</p><button class="btn ghost" type="button" @click="addTemplate">添加第一个模板</button></div></section>
   <footer class="ejh-savebar"><span>配置保存后生效。</span><button class="btn primary" :disabled="busy||!ready">保存配置与模板</button></footer>
  </form>
<form v-else-if="tab==='open'" class="panel ejh-open-panel" @submit.prevent="openCards"><div class="ejh-section-heading"><div><span class="overline">ISSUE CARDS</span><h2>开卡 / 批量开卡</h2><p>选择已保存的模板，确认后顺序开卡；只有 OAI 卡加入 Pro 银行卡列表。</p></div><CreditCard :size="28"/></div><fieldset :disabled="busy||openUncertain" class="ejh-open-fields"><div class="ejh-grid"><label class="field"><span>已保存模板</span><select v-model="openForm.template" @change="chooseTemplate"><option value="">请选择</option><option v-for="t in settings.templates" :key="t.id" :value="t.id">{{t.name}} · {{(t.platform||'oai').toUpperCase()}}</option></select></label><label class="field"><span>数量</span><input v-model.number="openForm.count" type="number" min="1" max="50" required/></label><template v-if="openForm.input"><label class="field"><span>每张刷卡额度</span><input v-model="openForm.input.cardAmount" required/></label><label class="field"><span>到期日期</span><input v-model="openForm.input.activeDate" type="date" required/></label></template></div></fieldset><p v-if="openUncertain" class="danger-text">上次开卡结果待核实：{{openForm.request_id}}。请先在操作记录核实。</p><p class="ejh-callout">顺序提交；网络错误或结果未知时停止。不要换请求编号重试，先在操作记录核实。</p><button class="btn primary" :disabled="busy||!ready||!openForm.input||openUncertain">{{busy?'处理中…':'确认开卡'}}</button></form>
  <section v-else class="panel ejh-records"><div class="ejh-section-heading"><div><h2>{{tabs.find(t=>t[0]===tab)?.[1]}}</h2><p>{{sectionDescriptions[tab]}}</p></div><span class="ejh-record-count">{{filtered.length}} 条记录</span></div><div class="ejh-filterbar"><label class="ejh-search"><Search :size="16"/><input v-model="query" aria-label="搜索账号、尾号、流水号" placeholder="搜索账号、尾号、流水号" @input="page=1;targetCardID=''"/></label><template v-if="tab==='cards'"><div class="ejh-lifecycle-toggle" role="group" aria-label="正常或销卡筛选"><button type="button" :class="{active:lifecycleFilter==='normal'}" :aria-pressed="lifecycleFilter==='normal'" title="未销卡的卡片；可用性以各卡状态为准" @click="setLifecycle('normal')">正常 {{lifecycleCounts.normal}}</button><button type="button" :class="{active:lifecycleFilter==='normal_pending'}" @click="setLifecycle('normal_pending')">正常待定 {{lifecycleCounts.normal_pending}}</button><button type="button" :class="{active:lifecycleFilter==='closed'}" :aria-pressed="lifecycleFilter==='closed'" title="已销卡及销卡处理中" @click="setLifecycle('closed')">销卡 {{lifecycleCounts.closed}}</button></div><label class="ejh-filter-field"><span>卡状态</span><select v-model="cardStatusFilter" aria-label="卡状态筛选"><option value="">全部卡状态</option><option v-for="(label,key) in cardStates" :key="key" :value="key">{{label}}</option></select></label><label class="ejh-filter-field"><span>账号回款状态</span><select v-model="cardReturnFilter" aria-label="卡片账号回款状态筛选"><option value="">全部回款状态</option><option v-for="(label,key) in returnStates" :key="key" :value="key">{{label}}</option><option value="none">暂无关联账号</option></select></label></template><div v-if="tab==='cases'" class="ejh-lifecycle-toggle" role="group" aria-label="账号回款分类"><button type="button" :class="{active:caseCategory==='normal'}" :aria-pressed="caseCategory==='normal'" @click="setCaseCategory('normal')">正常 {{caseCounts.normal}}</button><button type="button" :class="{active:caseCategory==='unopened'}" :aria-pressed="caseCategory==='unopened'" @click="setCaseCategory('unopened')">未开通 {{caseCounts.unopened}}</button></div><select v-if="tab==='cases'&&caseCategory==='normal'" v-model="status" aria-label="回款状态筛选" @change="page=1"><option value="">全部状态</option><option v-for="(label,k) in accountReturnStates" :key="k" :value="k">{{label}}</option></select><select v-if="tab==='trades'" v-model="status" aria-label="流水状态筛选" @change="page=1"><option value="">全部流水</option><option value="unmatched">等待自动匹配</option><option value="pending">待入账 / 待识别</option><option value="returned">已识别回款</option><option value="other">其他资金事件</option></select><button v-if="hasFilters" type="button" class="ejh-text-button" @click="clearFilters">清除筛选</button><div v-if="tab==='cards'" class="ejh-view-toggle" role="group" aria-label="卡片显示方式"><button type="button" :class="{active:cardView==='cards'}" :aria-pressed="cardView==='cards'" @click="setCardView('cards')"><LayoutGrid :size="15"/>卡片视图</button><button type="button" :class="{active:cardView==='list'}" :aria-pressed="cardView==='list'" @click="setCardView('list')"><List :size="15"/>列表视图</button></div></div><div v-if="tab==='cards'" class="ejh-lifecycle-toggle" role="group" aria-label="开卡平台筛选"><button v-for="p in ['oai','cc']" :key="p" :class="{active:platformFilter===p}" @click="setPlatform(p)">{{p.toUpperCase()}}</button></div><p v-if="tab==='cards' && targetCardID" class="ejh-search-hint">已定位 {{query}} 的开通卡。修改搜索或清除筛选可查看其他卡片。</p>
   <div v-if="tab==='cases'&&caseCategory==='unopened'" class="ejh-batchbar"><button type="button" class="btn danger" :disabled="busy||!ready||!caseCounts.unopened" @click="deleteUnopenedCase()">删除全部未开通（{{caseCounts.unopened}}）</button><small>删除即确认已核实未开通并解除本单占用；批量包含全部分页，正在执行的订单保留。</small></div>
   <div v-if="tab==='cards'" class="ejh-batchbar"><button class="btn primary" type="button" :disabled="busy||!ready" @click="beginOpenBatch"><Plus :size="16"/>批量开卡</button><label><input type="checkbox" :checked="eligibleVisible.length>0&&eligibleVisible.every(c=>selectedCards.includes(c.id))" :disabled="busy||!eligibleVisible.length" @change="selectPage($event.target.checked)"/> 选择本页可销卡项</label><span>已选 {{selectedCards.length}} 张（跨页保留）</span><button class="btn danger" type="button" :disabled="busy||!selectedCards.length" @click="cancelSelected">批量销卡</button><button v-if="selectedCards.length" class="btn ghost" type="button" :disabled="busy" @click="selectedCards=[]">清空选择</button><small>{{lifecycleFilter==='closed'?'销卡处理中、销卡结果确认中优先；成功销卡按成功时间从新到旧，未记录时间的排在后面':'按未开通 / 开通中、已开通、待回款、已回款排序；同状态按创建时间从新到旧'}} · 仅全部账号已回款或未开通的卡可销卡</small></div>
   <div v-if="tab==='cards'&&lifecycleFilter==='normal_pending'" class="ejh-toolbar"><button class="btn ghost" :disabled="busy||!ready" @click="refreshPendingCards">一键刷新正常待定卡片数据</button><small>仅刷新当前平台的待定卡资料、余额和交易明细</small></div>
   <div v-if="tab==='cards' && cardView==='cards'" class="ejh-card-grid"><article v-for="c in visible" :key="c.id" class="ejh-card"><div class="ejh-card-top"><label class="ejh-card-select" :title="cancelReason(c)||'选择用于批量销卡'"><input type="checkbox" :aria-label="'选择卡片 '+(c.local?.name||c.remote.cardNo)+(cancelReason(c)?'：'+cancelReason(c):'')" :checked="selectedCards.includes(c.id)" :disabled="busy||!!cancelReason(c)" @change="selectCard(c,$event.target.checked)"/></label><span class="ejh-card-symbol"><CreditCard :size="21"/></span><button type="button" class="ejh-state ejh-filter-chip" :class="[c.local?.available?'returned':c.error?'waiting':'unopened',{selected:cardStatusFilter===cardState(c)}]" :aria-pressed="cardStatusFilter===cardState(c)" :aria-label="'筛选卡状态：'+cardStatus(c)" title="点击筛选此卡状态，再次点击取消" @click="filterCardStatus(c)">{{cardStatus(c)}}</button></div><EJHCardCancelledAt :card="c" :operations="data.operations"/><h3>{{c.local?.name||'EJH 卡片'}}</h3><CardTemplateBadge :origin="origin(c)"/><div class="ejh-card-number">{{c.remote.cardNo}}</div><EJHCardOpenedAt :card="c" :operations="data.operations"/><div class="ejh-card-meta"><span>{{c.remote.cardCurrency}}</span><span>有效期 {{expiryLabel(c.remote.validityDate)}}</span></div><div class="ejh-card-balance"><div><small>卡余额</small><strong>{{c.balance_at ? c.balance : '尚未查询'}} <em v-if="c.balance_at">{{c.remote.cardCurrency}}</em></strong><small>{{historicalBalance(c)?'历史保存于':'查询于'}} {{formatTime(c.balance_at)}}</small></div><button class="ejh-text-button" type="button" :disabled="busy||!ready||c.issuer_linked===false" @click="cardAction(c,'balance')">{{historicalBalance(c)?'历史余额':'查询余额'}}</button><button type="button" class="btn ghost compact" :disabled="busy||!ready||c.issuer_linked===false" @click="showTrades(c)">交易明细</button></div><p v-if="c.balance_error&&!historicalBalance(c)" class="danger-text">最近查询失败，保留旧值：{{c.balance_error}}</p><EJHCardFunding class="card-funding-summary" :card="c" :operations="data.operations" :trades="data.trades"/><EJHCardAmounts :operations="data.operations" :card="c" :trades="data.trades" :cases="cardCases(c)" :fee-percent="data.reversal_fee_percent"/><div class="ejh-card-capacity"><div><span>已开通账号</span><CardAccountPreview :rows="cardCases(c)"><button type="button" @click="showAccounts(c)"><strong>{{c.local?.opened_accounts||0}}</strong> / {{c.local?.account_limit||settings.default_max_accounts}}<ChevronDown :size="12"/></button></CardAccountPreview></div><div class="ejh-capacity-track"><span :style="{width:capacityPercent(c)+'%'}"></span></div><div class="ejh-capacity-hint"><span>在途 {{c.local?.pending_accounts||0}} · 剩余 {{c.local?.remaining_accounts||0}}</span><span>{{c.local?.max_accounts?'单卡上限':'EJH 默认'}}</span><button v-if="c.local" type="button" class="ejh-text-button" :disabled="busy" @click="editLimit(c)">修改上限</button></div></div><CardOccupancy :card="c" compact @updated="load" /><div class="ejh-return-counts"><button v-for="(label,k) in returnStates" :key="k" type="button" class="ejh-filter-chip" :class="{selected:cardReturnFilter===k}" :aria-pressed="cardReturnFilter===k" :aria-label="'筛选账号回款状态：'+label" title="点击筛选此回款状态，再次点击取消" @click="filterCardReturn(k)"><EJHCardReturnBadge :state="k" :rows="cardCases(c)"/></button></div><p v-if="c.error" class="danger-text">{{c.error}}</p><footer><PaymentCardStatus :card="c" compact /><button v-if="c.issuer_linked!==false&&cardLifecycle(c)!=='closed'" type="button" class="text-link" :disabled="busy||!!categorizingCard" @click="changeCardCategory(c)">{{c.normal_pending?'恢复正常':'移到正常待定'}}</button><CardPaymentPolicy v-if="c.issuer_linked!==false" issuer="ejh" :card-id="c.id" :data="data" :disabled="busy" @saved="load"/><CardCopyButton issuer="ejh" :card-id="c.id" @error="show($event,'error')"/><button type="button" class="btn ghost compact" @click="managedID=c.id"><SlidersHorizontal :size="14"/>管理卡片</button></footer></article></div>
   <div v-else class="table-shell"><table v-if="tab==='cards'" class="ejh-card-table"><thead><tr><th class="ejh-select-cell">选择</th><th>卡片 / 尾号</th><th>开卡模板 / 平台</th><th>状态 / 有效期</th><th>卡余额</th><th>额度 / 退回 / 差额</th><th>收益 / 成本 / 结余</th><th>开通账号 / 名额</th><th>账号回款</th><th>操作</th></tr></thead><tbody><tr v-for="c in visible" :key="c.id"><td class="ejh-select-cell"><label class="ejh-card-select" :title="cancelReason(c)||'选择用于批量销卡'"><input type="checkbox" :aria-label="'选择卡片 '+(c.local?.name||c.remote.cardNo)+(cancelReason(c)?'：'+cancelReason(c):'')" :checked="selectedCards.includes(c.id)" :disabled="busy||!!cancelReason(c)" @change="selectCard(c,$event.target.checked)"/></label></td><td><strong>{{c.local?.name||'EJH 卡片'}}</strong><small>{{c.remote.cardNo}}</small><small>{{c.remote.orderNo}}</small><EJHCardOpenedAt :card="c" :operations="data.operations"/></td><td><CardTemplateBadge :origin="origin(c)"/></td><td><button type="button" class="ejh-state ejh-filter-chip" :class="[c.local?.available?'returned':c.error?'waiting':'unopened',{selected:cardStatusFilter===cardState(c)}]" :aria-pressed="cardStatusFilter===cardState(c)" :aria-label="'筛选卡状态：'+cardStatus(c)" title="点击筛选此卡状态，再次点击取消" @click="filterCardStatus(c)">{{cardStatus(c)}}</button><EJHCardCancelledAt :card="c" :operations="data.operations"/><PaymentCardStatus :card="c" compact /><small>有效期 {{expiryLabel(c.remote.validityDate)}} · {{c.remote.cardCurrency}}</small><small v-if="c.error" class="danger-text">{{c.error}}</small></td><td><strong>{{c.balance_at ? c.balance : '尚未查询'}} {{c.balance_at ? c.remote.cardCurrency : ''}}</strong><small>{{historicalBalance(c)?'历史保存于':'查询于'}} {{formatTime(c.balance_at)}}</small><small v-if="c.balance_error&&!historicalBalance(c)" class="danger-text">最近查询失败，保留旧值：{{c.balance_error}}</small></td><td><EJHCardFunding :card="c" :operations="data.operations" :trades="data.trades"/></td><td><EJHCardAmounts :operations="data.operations" :card="c" :trades="data.trades" :cases="cardCases(c)" :fee-percent="data.reversal_fee_percent"/></td><td class="ejh-account-capacity"><CardAccountPreview :rows="cardCases(c)"><button type="button" class="ejh-text-button ejh-account-count" @click="showAccounts(c)"><span>{{c.local?.opened_accounts||0}} / {{c.local?.account_limit||settings.default_max_accounts}} 个账号</span><ChevronDown :size="12"/></button></CardAccountPreview><small>在途 {{c.local?.pending_accounts||0}} · 剩余 {{c.local?.remaining_accounts||0}}</small><small>{{c.local?.max_accounts?'单卡上限':'EJH 默认'}}</small><button v-if="c.local" type="button" class="ejh-text-button" :disabled="busy" @click="editLimit(c)">修改上限</button></td><td><CardOccupancy :card="c" compact @updated="load" /><div class="ejh-return-counts"><button v-for="(label,k) in returnStates" :key="k" type="button" class="ejh-filter-chip" :class="{selected:cardReturnFilter===k}" :aria-pressed="cardReturnFilter===k" :aria-label="'筛选账号回款状态：'+label" title="点击筛选此回款状态，再次点击取消" @click="filterCardReturn(k)"><EJHCardReturnBadge :state="k" :rows="cardCases(c)"/></button></div></td><td><div class="ejh-list-actions"><button v-if="c.issuer_linked!==false&&cardLifecycle(c)!=='closed'" type="button" class="text-link" :disabled="busy||!!categorizingCard" @click="changeCardCategory(c)">{{c.normal_pending?'恢复正常':'移到正常待定'}}</button><CardPaymentPolicy v-if="c.issuer_linked!==false" issuer="ejh" :card-id="c.id" :data="data" :disabled="busy" @saved="load"/><CardCopyButton issuer="ejh" :card-id="c.id" @error="show($event,'error')"/><button type="button" class="btn ghost compact" :disabled="busy||!ready||c.issuer_linked===false" @click="cardAction(c,'balance')">{{historicalBalance(c)?'历史余额':'查询余额'}}</button><button type="button" class="btn ghost compact" :disabled="busy||!ready||c.issuer_linked===false" @click="showTrades(c)">交易明细</button><button type="button" class="btn ghost compact" @click="managedID=c.id">管理卡片</button></div></td></tr></tbody></table><table v-else-if="tab==='cases'"><thead><tr><th>账号 / 开通订单</th><th>银行卡 / 套餐</th><th>状态</th><th>OAI 确认</th><th>开通 / 申请时间</th><th>付款 / 回款 / 差额 / 消费收益</th><th v-if="caseCategory==='unopened'">操作</th></tr></thead><tbody><tr v-for="c in visible" :key="c.id"><td>{{c.email}}<small>{{c.id}}{{c.deleted?' · 账号已删除':''}}</small></td><td>{{c.card_name}} · {{c.last4}}<small>{{c.provider}} / {{c.plan}}</small></td><td><EJHCaseState :row="c" editable :disabled="busy" @updated="caseStateUpdated"/><small v-if="overdue(c)" class="danger-text">超过提醒时长</small><small>{{c.request_source==='manual'?'人工标记合并成功':c.request_source==='remote'?'原合并流程成功标记':''}}</small></td><td><ProOAICaseFlow :row="c" @updated="load"/></td><td>{{formatTime(c.opened_at)}}<small>{{formatTime(c.requested_at)}}</small></td><td><EJHCaseAmounts :trades="data.trades" :operations="data.operations" :cards="data.cards" :fee-percent="data.reversal_fee_percent" :row="c" @updated="load"/></td><td v-if="caseCategory==='unopened'"><button type="button" class="btn danger compact" :aria-label="'删除 '+c.email+' 的未开通订单 '+c.id" :disabled="busy||!ready" @click="deleteUnopenedCase(c)">删除</button></td></tr></tbody></table>
 <table v-else-if="tab==='trades'"><thead><tr><th>流水 / 卡片</th><th>类型 / 入账状态</th><th>金额（原始字段）</th><th>商户 / 日期</th><th>自动归属</th></tr></thead><tbody><tr v-for="t in visible" :key="t.id"><td>{{t.trade.tradeNo}}<small>{{t.trade.orderNo}} · {{t.trade.cardNo}}</small></td><td>{{tradeKinds[t.trade.tradeType]||t.trade.tradeType}}<small>{{t.trade.tradeStatus==='2'?'卡台已入账':'待入账 / 未知'}}</small></td><td>{{t.trade.tradeAmount}} {{t.trade.tradeCurrency}}<small>账单 {{t.trade.billAmount}} {{t.trade.billCurrency}}</small><small>refundBuyPrice：{{t.trade.refundBuyPrice||'未提供'}}</small></td><td>{{t.trade.collectionName}}<template v-if="['2','5','16'].includes(t.trade.tradeType)"><small title="优先取卡台记账时间；无有效记账时间时取交易时间">回款 {{returnPostedAt(t)?formatTime(returnPostedAt(t)):'—'}}</small><small>交易 {{t.trade.tradeTime||'—'}}</small></template><small v-else>{{t.trade.tradeTime||t.trade.createdAt}}</small></td><td>{{data.cases.find(c=>c.id===t.payment_order_id)?.email||'未关联'}}<ReturnStatusBadge v-if="returnTypes.has(t.trade.tradeType)" :state="t.confirmed ? 'returned' : t.payment_order_id ? 'unverified' : 'unmatched'"/><small v-else>其他资金事件</small><small v-if="t.evidence">{{t.evidence}}</small></td></tr></tbody></table>
 <table v-else-if="tab==='operations'"><thead><tr><th>请求编号</th><th>操作</th><th>状态</th><th>额度</th><th>时间 / 核实</th></tr></thead><tbody><tr v-for="o in visible" :key="o.id"><td>{{o.id}}<small>{{o.order_no}}</small></td><td>{{{open:'开卡',increase:'追加额度',cancel:'销卡'}[o.method]}} · {{o.automatic?'自动':'手动'}}</td><td>{{operationStates[o.status]||o.status}}<small class="danger-text">{{o.error}}</small><small>{{o.note}}</small><small v-if="o.check_error" class="muted">状态查询暂时失败，后台将重试。</small></td><td>{{o.amount}} {{o.currency}}</td><td>{{formatTime(o.created_at)}}<button v-if="['unknown','submitting','import_pending'].includes(o.status)" class="btn ghost compact" :disabled="busy" @click="resolve(o)">核实结果</button></td></tr></tbody></table>
 <template v-else><details v-for="(a,i) in visible" :key="i"><summary>{{formatTime(a.at)}} · {{a.action||'关联核实'}}</summary><pre>{{JSON.stringify(a,null,2)}}</pre></details></template></div><div v-if="!visible.length" class="ejh-empty"><Search :size="26"/><strong>{{hasFilters ? '没有匹配的记录' : '暂无记录'}}</strong><p>{{hasFilters ? '试试其他关键词或清除筛选。' : '后台更新或点击刷新数据后，记录会显示在这里。'}}</p></div><Pagination :page="page" :page-size="pageSize" :total="filtered.length" @update:page="page=$event" @update:page-size="pageSize=$event;page=1"/></section>
 <Teleport to="body"><div v-if="detail||resolving||managedCard||limitCard" class="modal-backdrop" @click.self="closeOverlay"><section class="modal ejh-modal" :class="{'account-returns-modal':detail?.cases}" role="dialog" aria-modal="true" :aria-label="detail?.title || (limitCard ? '修改单卡最大开通数' : managedCard ? '卡片管理' : '核实卡台操作')"><button type="button" class="icon-button ejh-modal-close" aria-label="关闭弹窗" :disabled="busy" @click="closeOverlay"><X :size="18"/></button><EJHProgress v-if="progress&&!detail?.cases" :progress="progress" @stop="progress.stopRequested=true" @dismiss="progress=null"/><form v-if="limitCard" @submit.prevent="saveLimit">
 <h2>修改单卡最大开通数</h2>
 <p>{{limitCard.local?.name || 'EJH 卡片'}} · {{limitCard.remote.cardNo}}</p>
 <p>累计已开通 {{limitCard.local?.opened_accounts||0}} 个，在途 {{limitCard.local?.pending_accounts||0}} 个；当前有效上限 {{limitCard.local?.account_limit||settings.default_max_accounts}} 个。</p>
 <label class="field"><span>最大开通数</span><input v-model.number="limitValue" type="number" min="0" max="10000" step="1" required :disabled="busy" aria-label="单卡最大开通数"/><small>1～10000；填 0 沿用 EJH 默认（当前 {{settings.default_max_accounts}}）。</small></label>
 <p class="muted">仅调整这张实体卡的累计开通上限，已开通和在途次数保留。提高上限后仍须满足回款释放、卡片启用及有效状态等条件；回款不会返还名额。</p>
 <p v-if="Number(limitValue)>0 && Number(limitValue)<(limitCard.local?.opened_accounts||0)+(limitCard.local?.pending_accounts||0)" class="danger-text">新上限小于已用次数，保存后将停止新增开通，已有订单不受影响。</p>
 <button class="btn primary" :disabled="busy">{{busy?'保存中…':'保存单卡上限'}}</button>
 </form><template v-else-if="detail"><h2>{{detail.title}}<span v-if="detail.cases" class="account-return-count">（{{detail.cases.length}} 笔）</span></h2><p v-if="detail.cases && !detail.cases.length" class="ejh-callout">暂无账号回款记录</p><div v-if="detail.cases" class="account-return-list"><EJHAccountReturnSummary @updated="load" :trades="data.trades" :operations="data.operations" :cards="data.cards" :fee-percent="data.reversal_fee_percent" v-for="c in detail.cases" :key="c.id" :row="c"/></div><pre v-if="detail.data">{{JSON.stringify(detail.data,null,2)}}</pre></template>
 <template v-else-if="managedCard">
 <div class="ejh-section-heading"><div><span class="overline">CARD DETAILS</span><h2>{{managedCard.local?.name || 'EJH 卡片'}}</h2><p>{{managedCard.remote.cardNo}} · {{cardStatus(managedCard)}}</p></div></div>
 <CardOccupancy :card="managedCard" @updated="load" /><p v-if="managedCard.auto_cancel_check" class="ejh-callout">自动销卡检查：{{formatTime(managedCard.auto_cancel_check.at)}} · {{managedCard.auto_cancel_check.reason||'已完成检查'}}</p><div class="ejh-detail-summary"><div><small>累计开通</small><strong>{{managedCard.local?.opened_accounts || 0}} 个账号</strong></div><div><small>单卡上限</small><strong>{{managedCard.local?.account_limit || settings.default_max_accounts}}</strong></div><div><small>卡片余额</small><strong>{{managedCard.balance || '未查询'}} {{managedCard.remote.cardCurrency}}</strong></div></div>
 <p v-if="managedCard.issuer_linked===false" class="muted">手动添加的 EJH 类型卡尚未绑定卡台订单，只能管理本地类型、占用和开通次数；余额与资金操作不可用。</p><PaymentCardStatus :card="managedCard" /><h3>查看与设置</h3><div class="ejh-management-actions"><CardPaymentPolicy v-if="managedCard.issuer_linked!==false" issuer="ejh" :card-id="managedCard.id" :data="data" :disabled="busy" @saved="load"/><CardCopyButton issuer="ejh" :card-id="managedCard.id" @error="show($event,'error')"/><button v-for="[a,label] in [['details','查看卡密'],['refresh','刷新卡片'],['balance','查询余额']]" :key="a" class="btn ghost" :disabled="busy||managedCard.issuer_linked===false" @click="cardAction(managedCard,a)">{{isClosedCard(managedCard)&&a==='refresh'?'查看历史卡片':historicalBalance(managedCard)&&a==='balance'?'历史余额':label}}</button><button class="btn ghost" :disabled="busy||managedCard.issuer_linked===false" @click="showTrades(managedCard)">交易明细</button><button class="btn ghost" @click="showAccounts(managedCard)">关联账号</button><template v-if="managedCard.local"><button class="btn ghost" :disabled="busy" @click="editLimit(managedCard)">修改单卡上限</button><button class="btn ghost" :disabled="busy||isClosedCard(managedCard)" @click="toggle(managedCard)">{{isClosedCard(managedCard)?'已销卡':managedCard.local.enabled?'停用':'启用'}}</button></template></div>
 <div class="ejh-risk-zone"><h3>资金操作</h3><p>这些操作会提交至 EJH。请先核实卡片与金额，结果未知时不要重复提交。</p><div class="ejh-toolbar"><button class="btn ghost" :disabled="busy||managedCard.issuer_linked===false" @click="cardAction(managedCard,'increase')">追加额度</button><button class="btn danger" :disabled="busy||!!cancelReason(managedCard)" :title="cancelReason(managedCard)" @click="cardAction(managedCard,'cancel')">{{cardState(managedCard)==='8'?'销卡处理中':cardState(managedCard)==='cancel_pending'?'销卡结果确认中':isClosedCard(managedCard)?'已销卡':'销卡'}}</button></div></div>
 </template>
 <form v-else-if="resolving" @submit.prevent="saveResolve"><h2>核实卡台操作</h2><p>未知结果不能直接重复提交，先核对卡台记录。</p><label class="field"><span>实际结果</span><select v-model="resolveForm.outcome"><option value="completed">已完成</option><option value="not_executed">确认未执行</option></select></label><label class="field"><span>EJH 开卡订单号</span><input v-model="resolveForm.order_no" :required="resolving.method==='open'&&resolveForm.outcome==='completed'"/></label><label class="field"><span>核实依据</span><textarea v-model="resolveForm.evidence" minlength="3" required/></label><button class="btn primary" :disabled="busy">提交核实</button></form><p v-if="message.type==='error'" class="danger-text">{{message.text}}</p><button class="btn" :disabled="busy" @click="closeOverlay">关闭</button></section></div></Teleport>
  <EJHCardTrades :saved-trades="data.trades" :default-page-size="defaultPageSize" :operations="data.operations" :fee-percent="data.reversal_fee_percent" ref="tradeDialog" @updated="load"/>
  <p class="ejh-footnote"><ShieldCheck :size="14"/>同卡付款与回款自动识别；授权撤销及已入账退款均计入回款。销卡退余额不计入账号回款。</p>
 </div>
</template>
<style scoped>
.ejh-card-table .ejh-select-cell{width:42px;min-width:42px;max-width:42px;padding-inline:10px;text-align:center}.ejh-select-cell .ejh-card-select{justify-content:center;min-height:24px}.ejh-card .ejh-card-select{min-height:24px;padding:4px}.ejh-card-table .ejh-account-capacity{min-width:116px}.ejh-account-count{white-space:nowrap;flex-wrap:nowrap}.ejh-account-count>span,.ejh-account-count>svg{flex-shrink:0}.ejh-card-table :deep(.card-occupancy){margin-top:0}

.ejh-card .card-funding-summary{margin:12px 0;padding:12px 0;border-top:1px solid var(--line);border-bottom:1px solid var(--line)}
.ejh-lifecycle-toggle{display:flex;gap:4px;padding:4px;border:1px solid var(--line);border-radius:9px;background:var(--bg-elevated)}.ejh-lifecycle-toggle button{padding:8px 14px;border-radius:6px;color:var(--muted);font-size:12px}.ejh-lifecycle-toggle button.active{background:var(--green-bg);color:var(--green-strong);font-weight:600}.ejh-card-balance{flex-wrap:wrap}.ejh-card-balance>div{flex-basis:100%}

.ejh-batchbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:0 0 18px;font-size:12px}.ejh-batchbar label,.ejh-card-select{display:flex;align-items:center;gap:6px}.ejh-batchbar small{width:100%;color:var(--muted)}.ejh-card-select{font-size:10px;color:var(--muted);min-width:0}.ejh-card-select input,.ejh-batchbar input{width:16px;height:16px;flex-shrink:0;accent-color:var(--green)}.ejh-open-fields{border:0;padding:0;margin:0;min-width:0}.ejh-modal :deep(.ejh-progress){margin:12px 0}

.ejh-occupancy{color:var(--green-strong);overflow-wrap:anywhere}.ejh-occupancy small{display:block;color:var(--muted)}
.ejh-required{color:var(--red);font-weight:600;margin-left:2px}
.ejh-view-toggle{display:flex;gap:3px;margin-left:auto;padding:4px;border-radius:9px;background:var(--bg-elevated);border:1px solid var(--line)}.ejh-view-toggle button{display:flex;align-items:center;gap:6px;padding:8px 10px;border-radius:6px;color:var(--muted);font-size:12px}.ejh-view-toggle button.active{background:var(--surface);color:var(--green-strong);box-shadow:0 1px 4px #0000000c}.ejh-view-toggle button:focus-visible{outline:2px solid var(--green)}.ejh-return-counts{display:flex;flex-wrap:wrap;gap:6px;margin-top:12px}.ejh-list-actions{display:flex;flex-wrap:wrap;gap:8px}.ejh-search-hint{font-size:12px;color:var(--blue);margin:-8px 0 20px}.ejh-card-table td{vertical-align:top}.ejh-card-table .danger-text{color:var(--red)}

.ejh-view{gap:20px;min-width:0}.ejh-heading{display:flex;align-items:center;justify-content:space-between;gap:20px}.ejh-heading h1{font-size:29px;font-weight:700;margin:6px 0}.ejh-heading p{color:var(--muted);font-size:13px;line-height:1.7}.ejh-toolbar{display:flex;flex-wrap:wrap;gap:10px;align-items:center}.ejh-view .panel{border:1px solid var(--line);border-radius:16px;padding:24px;box-shadow:none}.ejh-view>:deep(.ejh-progress){position:sticky;top:68px;z-index:10}.ejh-view :deep(.message-bar){padding:12px 16px;margin:0;border:1px solid var(--line);border-radius:10px;background:var(--surface)}
.ejh-stats{display:grid;grid-template-columns:repeat(5,minmax(0,1fr));gap:14px}.ejh-stat{display:flex;align-items:center;gap:12px;padding:16px;background:var(--surface);border:1px solid var(--line);border-radius:14px;text-align:left;transition:border-color .15s}.ejh-stat:hover,.ejh-stat.selected{border-color:var(--green)}.ejh-stat-icon{display:grid;place-items:center;width:42px;height:42px;flex-shrink:0;border-radius:12px;background:var(--surface-2);color:var(--muted)}.ejh-stat.opened .ejh-stat-icon{color:var(--blue);background:var(--blue-bg)}.ejh-stat.waiting .ejh-stat-icon{color:var(--amber);background:var(--amber-bg)}.ejh-stat.returned .ejh-stat-icon{color:var(--green-strong);background:var(--green-bg)}.ejh-stat-value{flex:1;min-width:0}.ejh-stat-value small{display:block;color:var(--text-2);font-size:12px}.ejh-stat-value strong{display:block;font-size:28px;font-weight:650;margin-top:8px;line-height:1}.ejh-stat-value em{font-size:10px;color:var(--muted);font-weight:400;font-style:normal;margin-left:6px}.ejh-stat>svg{color:var(--muted);align-self:flex-start;width:14px}
.ejh-statusbar{display:flex;align-items:center;gap:20px;font-size:12px;color:var(--muted);padding:2px 2px 8px;flex-wrap:wrap}.ejh-connection{display:flex;align-items:center;gap:7px}.ejh-connection i{width:7px;height:7px;border-radius:50%;background:var(--muted)}.ejh-connection i.enabled{background:var(--green)}.ejh-last-sync{flex:1}.ejh-text-button{display:inline-flex;gap:6px;align-items:center;font-size:12px;color:var(--text-2)}.ejh-text-button:hover{color:var(--green-strong)}.rotated{transform:rotate(180deg)}.ejh-tabs{display:flex;gap:26px;border-bottom:1px solid var(--line);overflow-x:auto;white-space:nowrap}.ejh-tabs button{padding:12px 2px 15px;border-bottom:2px solid transparent;color:var(--muted);font-size:13px;flex-shrink:0}.ejh-tabs button.active{color:var(--green-strong);border-color:var(--green);font-weight:600}.ejh-tabs button>span{font-size:10px;background:var(--surface-2);padding:2px 6px;border-radius:5px;margin-left:7px}
.ejh-section-heading{display:flex;align-items:center;justify-content:space-between;gap:20px;margin-bottom:22px}.ejh-section-heading h2{font-size:18px;font-weight:650;margin:4px 0}.ejh-section-heading h3{font-size:15px;font-weight:600}.ejh-section-heading p{font-size:12px;color:var(--muted);margin-top:7px;line-height:1.7}.ejh-record-count{font-size:12px;color:var(--muted);white-space:nowrap}.ejh-filterbar{display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin-bottom:18px}.ejh-search{position:relative;display:flex;align-items:center;min-width:200px;max-width:380px;flex:1}.ejh-search svg{position:absolute;left:13px;color:var(--muted)}.ejh-search input{padding-left:38px;width:100%}.ejh-filterbar>select{width:auto;min-width:140px}
.ejh-card-grid{display:grid;grid-template-columns:repeat(auto-fill,minmax(min(100%,250px),1fr));gap:12px;align-items:stretch;grid-auto-rows:1fr}.ejh-card{display:flex;flex-direction:column;border:1px solid var(--line);border-radius:11px;padding:14px;min-width:0;background:var(--surface)}.ejh-card-top{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-bottom:10px}.ejh-card-symbol{color:var(--green-strong)}.ejh-card h3{font-size:13px;font-weight:650;overflow-wrap:anywhere}.ejh-card-number{font-family:ui-monospace,monospace;font-size:17px;letter-spacing:1px;margin:8px 0 10px;color:var(--text);overflow-wrap:anywhere}.ejh-card-meta{display:flex;justify-content:space-between;gap:8px;flex-wrap:wrap;color:var(--muted);font-size:11px}.ejh-card-capacity{border-top:1px solid var(--line);margin-top:12px;padding-top:10px}.ejh-card-capacity>div:first-child{display:flex;align-items:center;justify-content:space-between;font-size:12px}.ejh-card-capacity button{display:flex;align-items:center;gap:5px;color:var(--text-2)}.ejh-card-capacity button strong{font-size:15px;color:var(--text)}.ejh-capacity-track{height:5px;background:var(--surface-2);border-radius:5px;margin:8px 0;overflow:hidden}.ejh-capacity-track span{display:block;height:100%;background:var(--green);border-radius:5px}.ejh-capacity-hint{flex-wrap:wrap;display:flex;justify-content:space-between;gap:8px;font-size:10px;color:var(--muted)}.ejh-card footer{min-height:46px;border-top:1px solid var(--line);padding-top:10px;margin-top:auto;display:flex;align-items:center;justify-content:space-between;gap:6px}.ejh-card footer>span{font-size:10px;color:var(--muted)}.ejh-card .danger-text{font-size:11px;line-height:1.6;margin-top:10px}
.ejh-state{display:inline-flex;align-items:center;padding:5px 9px;border-radius:6px;font-size:11px;font-weight:550;background:var(--surface-2);color:var(--muted);white-space:nowrap}.ejh-state.opened{color:var(--blue);background:var(--blue-bg)}.ejh-state.waiting{color:var(--amber);background:var(--amber-bg)}.ejh-state.returned{color:var(--green-strong);background:var(--green-bg)}.ejh-view table{min-width:850px;width:100%}.ejh-view td{max-width:330px;overflow-wrap:anywhere}.ejh-view td small{display:block;white-space:normal;color:var(--muted);margin-top:6px;font-size:11px;line-height:1.6}.ejh-view :deep(.pagination){margin-top:22px;border-top:1px solid var(--line);padding-top:18px}.ejh-empty{display:flex;flex-direction:column;align-items:center;justify-content:center;gap:12px;padding:48px 20px;text-align:center;color:var(--muted)}.ejh-empty strong{font-size:14px;color:var(--text-2)}.ejh-empty p{font-size:12px}.ejh-empty svg{opacity:.6}
.ejh-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:22px;max-width:860px}.ejh-grid .wide{grid-column:1/-1}.ejh-settings-nav{display:flex;gap:8px;padding:5px;border-radius:10px;background:var(--bg-elevated);width:fit-content;margin-bottom:28px}.ejh-settings-nav button{padding:10px 20px;border-radius:7px;font-size:12px;color:var(--muted)}.ejh-settings-nav button.active{background:var(--surface);color:var(--text);box-shadow:0 1px 4px #0000000c}.ejh-settings .toggle-row{border:1px solid var(--line);border-radius:12px;background:var(--bg-elevated)}.ejh-settings .toggle-row small{font-size:11px;line-height:1.7}.ejh-settings .toggle-row input:focus-visible+i{outline:2px solid var(--green);outline-offset:4px}.ejh-template{border:1px solid var(--line);border-radius:12px;margin:14px 0;overflow:hidden}.ejh-template>summary{display:flex;align-items:center;gap:14px;padding:18px 20px;cursor:pointer}.ejh-template>summary>span{flex:1}.ejh-template summary strong{font-size:13px}.ejh-template summary small{display:block;color:var(--muted);font-size:11px;margin-top:5px}.ejh-template[open]>summary{border-bottom:1px solid var(--line);background:var(--bg-elevated)}.ejh-template[open]>summary>svg:last-child{transform:rotate(180deg)}.template-body{padding:22px}.template-body>details{margin:22px 0;font-size:12px}.template-body>details>summary{cursor:pointer;color:var(--text-2);padding:12px 0}.ejh-savebar{display:flex;align-items:center;justify-content:space-between;gap:20px;border-top:1px solid var(--line);padding-top:20px;margin-top:28px}.ejh-savebar>span{font-size:11px;line-height:1.7;color:var(--muted)}.ejh-savebar .btn{flex-shrink:0}.ejh-sync-fields{display:flex;align-items:flex-end;gap:14px;flex-wrap:wrap}.ejh-sync-fields .field{width:210px}.ejh-balance{padding:10px 14px;border:1px solid var(--line);border-radius:8px;margin-top:18px;font-size:12px;color:var(--muted)}.ejh-balance strong{margin-left:10px;color:var(--text)}.ejh-callout{padding:14px 18px;background:var(--blue-bg);border-radius:10px;font-size:12px;color:var(--text-2);line-height:1.8;margin:22px 0}.ejh-open-panel{max-width:960px}.ejh-footnote{display:flex;align-items:flex-start;gap:8px;color:var(--muted);font-size:11px;line-height:1.8}.ejh-footnote svg{flex-shrink:0;margin-top:3px}
.ejh-modal{position:relative;width:min(700px,95vw);max-height:88vh;overflow:auto;padding:30px;border-radius:16px}.ejh-modal-close{position:absolute;right:18px;top:18px}.ejh-modal h2{padding-right:38px;font-size:20px}.ejh-modal .field{margin:18px 0}.ejh-modal p{line-height:1.8}.ejh-modal pre,.ejh-view pre{white-space:pre-wrap;overflow-wrap:anywhere;background:var(--bg-elevated);border-radius:10px;padding:16px;font-size:11px}.ejh-modal>button:last-child{margin-top:24px}.ejh-modal form>button{display:block;margin-top:20px}.ejh-detail-summary{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin:22px 0 28px}.ejh-detail-summary>div{padding:14px;background:var(--bg-elevated);border-radius:10px}.ejh-detail-summary small{display:block;color:var(--muted);font-size:11px}.ejh-detail-summary strong{display:block;margin-top:9px;font-size:15px}.ejh-modal h3{font-size:13px;font-weight:650}.ejh-management-actions{display:flex;flex-wrap:wrap;gap:10px;margin:16px 0 28px}.ejh-risk-zone{padding:18px;border:1px solid color-mix(in srgb,var(--red) 25%,transparent);border-radius:12px;background:var(--red-bg)}.ejh-risk-zone h3{color:var(--red)}.ejh-risk-zone p{margin:8px 0 16px;font-size:11px}.danger-text{color:var(--red)}
@media(max-width:1180px){.ejh-stat{gap:10px;padding:18px 14px}.ejh-stat-icon{width:34px;height:34px}.ejh-stat-value em{display:none}}
@media(max-width:760px){.ejh-heading{align-items:flex-start;flex-direction:column}.ejh-stats{grid-template-columns:repeat(2,minmax(0,1fr));gap:10px}.ejh-card-grid,.ejh-grid{grid-template-columns:minmax(0,1fr)}.ejh-view .panel{padding:18px}.ejh-tabs{gap:20px}.ejh-stat-value strong{font-size:25px}.ejh-section-heading{flex-wrap:wrap;gap:12px}.ejh-last-sync{min-width:calc(100% - 140px)}.ejh-statusbar{gap:12px}.ejh-savebar{align-items:flex-end}.ejh-filterbar>select{max-width:100%}.ejh-modal{padding:22px}.ejh-detail-summary{grid-template-columns:repeat(2,minmax(0,1fr))}.ejh-sync-fields .field{width:100%}}
.ejh-card-balance{display:flex;align-items:center;justify-content:space-between;gap:8px;margin-top:12px;padding:10px;background:var(--bg-elevated);border-radius:8px}.ejh-card-balance small{display:block;font-size:10px;color:var(--muted);margin:4px 0}.ejh-card-balance strong{display:block;font-size:17px;margin:4px 0;overflow-wrap:anywhere}.ejh-card-balance em{font-size:11px;color:var(--muted);font-style:normal}.ejh-card-balance button{flex-shrink:0}

.ejh-filter-field{display:flex;align-items:center;gap:7px;font-size:12px;color:var(--muted);max-width:100%}.ejh-filter-field>span{white-space:nowrap}.ejh-filter-field select{width:auto;min-width:140px;max-width:100%;color:var(--text)}.ejh-filter-chip{padding:0;border-radius:6px;cursor:pointer}.ejh-filter-chip:hover{filter:brightness(.94)}.ejh-filter-chip.selected{box-shadow:0 0 0 2px var(--green)}.ejh-filter-chip:focus-visible{outline:2px solid var(--green);outline-offset:3px}.ejh-state.ejh-filter-chip{padding:5px 8px;text-align:left;white-space:normal}.ejh-card .ejh-return-counts{gap:5px;margin-top:10px}.ejh-card :deep(.return-status){padding:4px 6px;gap:4px;font-size:10px}.ejh-card .ejh-occupancy{font-size:11px;margin-top:10px}.ejh-card footer .btn{padding:6px 8px;font-size:11px}.ejh-card-symbol svg{width:18px;height:18px}
@media(max-width:760px){.ejh-filter-field{width:100%;justify-content:space-between}.ejh-filter-field select{flex:1;min-width:0;max-width:230px}.ejh-card{padding:13px}}
.account-returns-modal{width:min(620px,calc(100vw - 44px));max-width:100%}.account-return-count{font-size:13px;color:var(--muted);font-weight:400}.account-return-list{min-width:0}
</style>

<style scoped>
@media(min-width:761px) and (max-width:1180px){.ejh-stat-icon,.ejh-stat>svg{display:none}.ejh-stat{padding:16px 10px;justify-content:center}.ejh-stat-value strong{font-size:25px}}
</style>
