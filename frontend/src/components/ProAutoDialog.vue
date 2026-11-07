<script setup>
import { accountDisposition } from '../pro-disposition'
import { computed, onBeforeUnmount, ref } from 'vue'
import { X } from 'lucide-vue-next'
import { api } from '../api'
import { proPlanName, payProviderName, paymentCountryLabel } from '../gptpay'
import { formatTime } from '../utils'
import Pagination from './Pagination.vue'
import ProAutoStages from './ProAutoStages.vue'
import ProMergeV2Stages from './ProMergeV2Stages.vue'
import ProMerchantStages from './ProMerchantStages.vue'
import CardOccupancy from './CardOccupancy.vue'
const props=defineProps({defaultPageSize:{type:Number,default:10}})
const emit=defineEmits(['updated'])
const email=ref(''),opened=ref(false),busy=ref(false),error=ref(''),state=ref({}),settings=ref({}),cards=ref([]),cardID=ref(''),page=ref(1),pageSize=ref(props.defaultPageSize || 10),total=ref(0)
const preparingCard=ref(false),cardWaitMessage=ref('')
let cancelCardWait=()=>{}
const openedAccount=ref({})
const displayDisposition=computed(()=>accountDisposition({...openedAccount.value,...state.value,pro_auto:state.value},settings.value))
const visibleError=computed(()=>busy.value?'':error.value||(state.value.error?(state.value.restock_retry_available?'上次开卡结果：':'')+state.value.error:''))
const autoCardEnabled=computed(()=>settings.value.card_provider==='nexovcc'?settings.value.nexovcc_auto_open:settings.value.ejh_auto_open)
const autoCardLabel=computed(()=>settings.value.card_provider==='nexovcc'?'环球':'EJH')
const followsReplacement=value=>!value.stop_requested&&value.status!=='stopped'&&value.payment_status!=='success'&&!value.quota_resume_available&&value.stage!=='quota'
const active=computed(()=>(followsReplacement(state.value)&&(state.value.replacement?.pending||state.value.replacement?.queued||!!state.value.replacement?.next_email))||state.value.pro_workflow_running===true||['running','waiting_quota','awaiting_deletion','awaiting_merchant'].includes(state.value.status))
const manualRunning=computed(()=>Object.values(state.value.stage_progress?.steps||{}).includes('running'))
const quotaResumeAvailable=computed(()=>state.value.quota_resume_available===true)
const deletionSubmitted=computed(()=>!!state.value.pro_deactivation?.submitted_at)
const resumable=computed(()=>!state.value.pro_merge_v2&&!state.value.pro_merchant&&!deletionSubmitted.value&&!quotaResumeAvailable.value&&state.value.steps?.oauth==='completed'&&!active.value&&state.value.status!=='completed')
const replacementFailed=computed(()=>!!state.value.replacement&&state.value.stage==='recharge'&&state.value.status==='failed')
const retryCurrent=computed(()=>!replacementFailed.value&&!state.value.pro_merge_v2&&!state.value.pro_merchant&&!deletionSubmitted.value&&!quotaResumeAvailable.value&&!!(state.value.id||state.value.order_id||state.value.restock_retry_available)&&!active.value&&!resumable.value&&state.value.status!=='completed')
const termsMissing=computed(()=>['cmsnav','xupay'].includes(settings.value.provider)&&!settings.value.terms_accepted)
const restartable=computed(()=>retryCurrent.value&&(!state.value.order_id||state.value.payment_status==='failed'))
const canStart=computed(()=>!replacementFailed.value&&!state.value.pro_merge_v2&&!state.value.pro_merchant&&!deletionSubmitted.value&&!quotaResumeAvailable.value&&!active.value&&!resumable.value&&!retryCurrent.value&&state.value.status!=='completed'&&!state.value.order_id)
const blockedCards=ref([]),checkingOccupancy=ref(false),occupancyMessage=ref('')
async function loadBlockedCards(){
 const found=[]
 for(let p=1;;p++){
  const d=await api(`/api/gptpay/cards?page=${p}&page_size=500`)
  const items=d.items||[];found.push(...items.filter(c=>['ejh','nexovcc'].includes(c.card_type)&&c.occupants?.some(o=>o.deleted&&o.order_id)))
  if(!items.length||p*500>=d.total)break
 }
 blockedCards.value=found
}
async function inspectOccupancy(){
 if(checkingOccupancy.value)return
 checkingOccupancy.value=true;occupancyMessage.value=''
 try{await loadBlockedCards();if(!blockedCards.value.length)occupancyMessage.value='没有待处理的已删除账号占用，请重试当前流程。'}catch(e){error.value=e.message}finally{checkingOccupancy.value=false}
}
async function occupancyResolved(){
 occupancyMessage.value='核实结果已保存；已开通并移出的账号仍须等回款后才可复用此卡。'
 try{await loadCards();await loadBlockedCards();emit('updated')}catch(e){error.value='核实已保存，刷新卡片失败：'+e.message}
}
let timer,generation=0
const path=()=>`/api/pro-accounts/${encodeURIComponent(email.value)}/auto-pro`
async function loadCards(){const data=await api(`/api/gptpay/cards?page=${page.value}&page_size=${pageSize.value}&enabled=true&card_provider=${settings.value.card_provider||'configured'}`);cards.value=data.items||[];total.value=data.total||0;cardID.value=autoCardEnabled.value?'':cards.value[0]?.id||''}
async function open(account){if(busy.value)return;openedAccount.value=account;clearTimeout(timer);generation++;blockedCards.value=[];occupancyMessage.value='';email.value=account.email;state.value=account.pro_auto||{};error.value='';opened.value=true;busy.value=true;page.value=1;pageSize.value=props.defaultPageSize || 10;try{const [config,current,pro]=await Promise.all([api('/api/gptpay/settings'),api(path()),api('/api/pro-settings')]);settings.value={...config,auto_delete_enabled:pro.auto_delete_enabled,auto_merchant_enabled:pro.auto_merchant_enabled,auto_merge_v2_enabled:pro.auto_merge_v2_enabled,quota_used_threshold:pro.quota_used_threshold,ejh_auto_open:pro.ejh_auto_open,nexovcc_auto_open:pro.nexovcc_auto_open,card_provider:pro.card_provider};await loadCards();state.value=current||{}}catch(e){error.value=e.message}finally{preparingCard.value=false;busy.value=false;schedule()}}
function close(){if(busy.value&&!cardWaitMessage.value)return;cancelCardWait();opened.value=false;generation++;clearTimeout(timer)}
function schedule(){clearTimeout(timer);if(opened.value&&(active.value||manualRunning.value))timer=setTimeout(refresh,state.value.status==='running'||manualRunning.value?1500:5000)}
async function refresh(){const gen=generation;try{let currentEmail=email.value;let value=await api(path());for(let i=0;i<20&&followsReplacement(value)&&value.replacement?.next_email;i++){currentEmail=value.replacement.next_email;value=await api(`/api/pro-accounts/${encodeURIComponent(currentEmail)}/auto-pro`)}if(gen!==generation)return;email.value=currentEmail;state.value=value;emit('updated');return true}catch(e){if(gen===generation)error.value=e.message}finally{if(gen===generation)schedule()}}
// Only an explicit rejection before issuing/paying permits automatic retry.
// The progress guard prevents a delayed retry undoing a newer Stop or start.
async function submitWithCardWait(body){
 const gen=generation,requestPath=path()
 for(let attempt=0;;attempt++){
  try{return await api(requestPath,{method:'POST',body})}
  catch(e){
   if(gen!==generation)return null
   if(!e.cardPreparationRetry||e.status!==409)throw e
   if(attempt>=30)throw new Error('卡台持续忙碌，已停止自动等待；本次未开卡、未付款，可稍后重试。')
   body={...body,card_preparation_retry:e.cardPreparationRetry}
   cardWaitMessage.value=`卡台正在处理其他任务，本次尚未提交；3 秒后自动继续（${attempt+1}/30）。`
   const proceed=await new Promise(resolve=>{
    const waitTimer=setTimeout(()=>{cancelCardWait=()=>{};resolve(true)},3000)
    cancelCardWait=()=>{clearTimeout(waitTimer);cancelCardWait=()=>{};resolve(false)}
   })
   cardWaitMessage.value=''
   if(!proceed||gen!==generation)return null
  }
 }
}
async function submitAuto(retryCurrent=false){
 if(busy.value)return
 if(retryCurrent&&state.value.payment_review_required&&!window.confirm('确认已核实原付款订单及风控原因，继续处理这个账号？'))return
 const gen=generation
 busy.value=true;preparingCard.value=retryCurrent?restartable.value:canStart.value;error.value=''
 const body={provider:settings.value.provider,card_provider:settings.value.card_provider,card_id:cardID.value,plan_code:settings.value.plan_code,country:settings.value.country,credit_quantity:settings.value.credit_quantity||0}
 if(retryCurrent)body.retry_current=true
 try{
  const p=await submitWithCardWait(body)
  if(p&&gen===generation){state.value=p.pro_auto;emit('updated')}
 }catch(e){if(gen===generation){error.value=e.message;const refreshed=await refresh();if(refreshed&&(active.value||state.value.status==='completed'))error.value=''}}
 finally{cardWaitMessage.value='';preparingCard.value=false;busy.value=false;if(gen===generation)schedule()}
}
async function start(){await submitAuto()}
async function retryStep(){await submitAuto(true)}
async function resumeMergeV2(){if(busy.value)return;busy.value=true;error.value='';try{await api(`/api/pro-accounts/${encodeURIComponent(email.value)}/merge-v2`,{method:'POST'});await refresh()}catch(e){error.value=e.message}finally{busy.value=false;schedule()}}
async function resumeMerchant(){if(busy.value)return;busy.value=true;error.value='';try{await api(`/api/pro-accounts/${encodeURIComponent(email.value)}/merchant`,{method:'POST'});await refresh()}catch(e){error.value=e.message}finally{busy.value=false;schedule()}}
async function resumeDeletion(){if(busy.value)return;busy.value=true;error.value='';try{await api(`/api/pro-accounts/${encodeURIComponent(email.value)}/deactivate`,{method:'POST',body:{}});await refresh()}catch(e){error.value=e.message}finally{busy.value=false;schedule()}}
async function resumeQuota(){if(busy.value)return;busy.value=true;error.value='';try{const p=await api(path(),{method:'POST',body:{resume_quota:true,expected_flow_id:state.value.id,expected_stop_version:state.value.stop_version||0}});state.value=p.pro_auto;emit('updated')}catch(e){error.value=e.message;await refresh()}finally{busy.value=false;schedule()}}
async function stop(){if(busy.value)return;busy.value=true;try{await api(path()+'/stop',{method:'POST',body:{}});await refresh()}catch(e){error.value=e.message}finally{preparingCard.value=false;busy.value=false;schedule()}}
async function changeProvider(){page.value=1;cardID.value='';cards.value=[];total.value=0;busy.value=true;error.value='';try{await loadCards()}catch(e){error.value=e.message}finally{busy.value=false}}
async function changePage(v){if(busy.value)return;page.value=v;busy.value=true;try{await loadCards()}catch(e){error.value=e.message}finally{busy.value=false}}
function changeSize(v){pageSize.value=v;changePage(1)}
onBeforeUnmount(()=>{generation++;opened.value=false;clearTimeout(timer);cancelCardWait()})
defineExpose({open})
</script>
<template><Teleport to="body"><div v-if="opened" class="modal-backdrop" @click.self="close"><section class="modal pro-auto-dialog" role="dialog" aria-modal="true" aria-label="全自动开通 Pro">
<div class="modal-heading"><h2>全自动开通 Pro</h2><button class="icon-button" title="关闭全自动窗口" :disabled="busy&&!cardWaitMessage" @click="close"><X :size="17" /></button></div>
<p>{{ email }}</p><ProAutoStages :state="state.stage_progress || state" :disposition="displayDisposition" />
<p v-if="cardWaitMessage" role="status">{{cardWaitMessage}} 等待期间关闭窗口会取消本次等待。</p>
<p v-if="preparingCard&&autoCardEnabled&&!cardWaitMessage" role="status">正在准备银行卡；新卡资料同步完成后会继续全自动，请稍候。</p>
<p v-if="state.replacement?.attempts" role="status">{{ state.replacement.provider_switched ? `已切换至 ${payProviderName(state.replacement.payment?.provider)}，账号尝试：${state.replacement.attempts-state.replacement.limit} / ${state.replacement.failover_limit}` : `第二步失败换号：${state.replacement.attempts} / ${state.replacement.limit} 次` }} · 原账号 {{ state.replacement.previous_email }}</p>
<p v-if="state.replacement?.message" role="status">{{ state.replacement.message }}</p>
<p v-if="state.retry_attempts" role="status">原账号重试：第 {{ state.retry_attempts }} 次</p>
<p v-if="state.proxy_retry_attempts" role="status">代理连接重试：第 {{ state.proxy_retry_attempts }} 次 / 最多 3 次（保留原账号和原卡）</p>
<p v-if="state.status==='running'&&state.retry_at" role="status">正在等待重试 · 下次执行：{{ formatTime(state.retry_at) }}</p>
<ProMergeV2Stages v-if="state.pro_merge_v2" :account="{pro_merge_v2:state.pro_merge_v2,pro_auto:state}" :disabled="busy || state.pro_workflow_running" readonly @retry="resumeMergeV2"/>
<ProMerchantStages v-if="state.pro_merchant" :account="{pro_merchant:state.pro_merchant,pro_auto:state}" :disabled="busy || state.pro_workflow_running" @retry="resumeMerchant"/>
<p v-if="state.status==='completed'" class="done">{{ state.stage === 'merge_v2' ? '合并空间2.0流程完成，上游停用和退款另行核实。' : state.stage === 'merchant' ? 'API 商户已处理成功。' : state.stage === 'deactivate' ? '删除账户及后续处理已完成。' : '全自动流程已完成，已移出空间。' }}</p>
<p v-if="state.status==='awaiting_deletion'">删除已提交，等待上游明确确认；可在账号菜单续跑核验，不会重复提交删除。</p>
<p v-if="state.stop_requested" role="status">{{state.pro_workflow_running?'正在停止当前任务…':'已停止，定时任务不会继续执行。'}}<span v-if="deletionSubmitted">已提交上游的删除请求无法撤回。</span></p>
<p v-if="state.status==='waiting_quota'">等待额度达到 {{ state.quota_used_threshold }}%，下次检测：{{ formatTime(state.next_check_at) }}</p>
<p v-if="quotaResumeAvailable">已开通并完成授权、推送，可恢复定时额度检测及后续合并/删除。首次推送计时继续沿用。</p>
<p v-if="state.status==='awaiting_push'">RT/AT 已保存，请推送当前下游，成功后自动恢复定时额度检测；也可点击“继续后续流程”。</p>
<p v-if="state.proxy_name">登录代理：{{ state.proxy_name }}<span v-if="state.exit_ip"> · 首次记录 IP：{{ state.exit_ip }}</span></p>
<p v-if="visibleError" role="alert" class="danger-text">{{ visibleError }}</p>
<div v-if="(state.error||'').includes('占用')||(error||'').includes('占用')" class="occupancy-help"><button class="btn ghost" :disabled="busy||checkingOccupancy" @click="inspectOccupancy">{{checkingOccupancy?'正在读取占用…':'处理已删除账号占用'}}</button><p v-if="occupancyMessage" role="status">{{occupancyMessage}}</p><div v-for="card in blockedCards" :key="card.id"><strong>{{card.name}} · 尾号 {{card.last4}}</strong><CardOccupancy :card="card" @updated="occupancyResolved"/></div></div>
<p v-if="(canStart||restartable)&&termsMissing" class="danger-text">请先到 Pro 全自动配置 → 开通设置确认并保存商户条款。</p><p v-if="state.payment_retry_blocked" class="danger-text">供应商不允许重试：{{state.payment_retry_hint || '请先联系供应商处理，并在开通订单中查询状态。'}}</p><template v-if="canStart||restartable"><p>供应商：{{ payProviderName(settings.provider) }}</p><p>套餐：{{ proPlanName(settings.plan_code) }}；7 天已用额度达到 {{ settings.quota_used_threshold || 100 }}% 后{{ settings.auto_merge_v2_enabled ? '执行合并空间2.0' : settings.auto_merchant_enabled ? '执行 API 商户' : settings.auto_delete_enabled ? '删除账户' : '合并' }}。</p>
<p>开通国家：{{ paymentCountryLabel(settings.country) }}<span v-if="settings.plan_code === 'codex_point'">；购买点数：{{ settings.credit_quantity }}</span></p>
<label class="field"><span>本次卡台</span><select v-model="settings.card_provider" :disabled="busy" @change="changeProvider"><option value="ejh">EJH</option><option value="nexovcc">环球</option></select><small>仅本次生效，使用所选卡台的模板和补卡预算。</small></label>
<label class="field"><span>开通银行卡</span><select v-model="cardID" :disabled="busy"><option value="">{{autoCardEnabled?`自动选卡（无可用卡时从 ${autoCardLabel} 补卡）`:'请选择银行卡'}}</option><option v-for="card in cards" :key="card.id" :value="card.id">{{ card.name }} · 尾号 {{ card.last4 }} · 已开通 {{ card.opened_accounts ?? 0 }} · 剩余 {{ card.remaining_accounts ?? 0 }}</option></select></label>
<Pagination v-if="total>10" :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
<p class="muted">首次登录后保留会话等待开通，成功后沿用该会话取得新 RT/AT，再推送配置的下游。关闭窗口不影响后台流程。</p></template>
<p v-if="state.payment_review_required&&!state.replacement" role="status" class="payment-review">付款待核实：自动重试已停止，原账号及订单保留。核实付款拦截原因后，可手动继续处理。</p>
<p v-if="!replacementFailed&&!deletionSubmitted&&state.order_id&&!active&&!resumable&&!quotaResumeAvailable&&state.status!=='completed'">开通失败：从登录开始重跑；开通成功：继续后续流程；结果待确认：先核查原订单。</p>
<button v-if="deletionSubmitted&&!manualRunning&&state.status!=='completed'" class="btn primary" :disabled="busy" @click="resumeDeletion">续跑删除核验及后续处理</button>
<div class="panel-actions"><button v-if="active" class="btn ghost danger-text" :disabled="busy" @click="stop">停止全自动</button><button v-if="quotaResumeAvailable" class="btn primary" :disabled="busy" @click="resumeQuota">{{busy?'正在恢复…':'恢复额度监控'}}</button><button v-if="retryCurrent" class="btn primary" :disabled="busy||state.payment_retry_blocked||(restartable&&termsMissing)" @click="retryStep">{{busy?'正在处理…':'重试并继续'}}</button><button v-if="resumable" class="btn primary" :disabled="busy" @click="start">继续后续流程</button><button v-if="canStart" class="btn primary" :disabled="busy||(!cardID&&!autoCardEnabled)||!settings.key_present||termsMissing" @click="start">确认全自动开通 {{ proPlanName(settings.plan_code) }}</button><button class="btn ghost" :disabled="busy&&!cardWaitMessage" @click="close">{{cardWaitMessage?'取消等待并关闭':'关闭'}}</button></div>
</section></div></Teleport></template>
<style scoped>
.pro-auto-dialog{width:min(720px,calc(100vw - 32px));max-height:90vh;overflow:auto;}
.pro-auto-dialog p{margin:15px 0;overflow-wrap:anywhere;}.pro-auto-dialog .field{margin:15px 0;}.done{color:var(--green-strong);}
</style>
