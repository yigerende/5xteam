<script setup>
import { computed, onBeforeUnmount, ref } from 'vue'
import { X } from 'lucide-vue-next'
import { api } from '../api'
import { proPlanName } from '../gptpay'
import { formatTime } from '../utils'
import Pagination from './Pagination.vue'
import ProAutoStages from './ProAutoStages.vue'
const emit=defineEmits(['updated'])
const email=ref(''),opened=ref(false),busy=ref(false),error=ref(''),state=ref({}),settings=ref({}),cards=ref([]),cardID=ref(''),page=ref(1),pageSize=ref(10),total=ref(0)
const active=computed(()=>['running','waiting_quota'].includes(state.value.status))
const resumable=computed(()=>state.value.steps?.oauth==='completed'&&!active.value&&state.value.status!=='completed')
const canStart=computed(()=>!active.value&&state.value.status!=='completed'&&!state.value.order_id)
let timer,generation=0
const path=()=>`/api/pro-accounts/${encodeURIComponent(email.value)}/auto-pro`
async function loadCards(){const data=await api(`/api/gptpay/cards?page=${page.value}&page_size=${pageSize.value}&enabled=true`);cards.value=data.items||[];total.value=data.total||0;cardID.value=cards.value[0]?.id||''}
async function open(account){if(busy.value)return;clearTimeout(timer);generation++;email.value=account.email;state.value=account.pro_auto||{};error.value='';opened.value=true;busy.value=true;page.value=1;try{const [config,current,pro]=await Promise.all([api('/api/gptpay/settings'),api(path()),api('/api/pro-settings'),loadCards()]);settings.value={...config,quota_used_threshold:pro.quota_used_threshold};state.value=current||{}}catch(e){error.value=e.message}finally{busy.value=false;schedule()}}
function close(){if(busy.value)return;opened.value=false;generation++;clearTimeout(timer)}
function schedule(){clearTimeout(timer);if(opened.value&&active.value)timer=setTimeout(refresh,state.value.status==='running'?1500:5000)}
async function refresh(){const gen=generation;try{const value=await api(path());if(gen!==generation)return;state.value=value;emit('updated')}catch(e){if(gen===generation)error.value=e.message}finally{if(gen===generation)schedule()}}
async function start(){if(busy.value)return;busy.value=true;error.value='';try{const p=await api(path(),{method:'POST',body:{card_id:cardID.value,plan_code:settings.value.plan_code}});state.value=p.pro_auto;emit('updated')}catch(e){error.value=e.message}finally{busy.value=false;schedule()}}
async function stop(){if(busy.value||!window.confirm('停止后续自动步骤？已提交的 GPTPay 订单仍会继续处理，请查询订单结果。'))return;busy.value=true;try{await api(path()+'/stop',{method:'POST',body:{}});await refresh()}catch(e){error.value=e.message}finally{busy.value=false;schedule()}}
async function changePage(v){if(busy.value)return;page.value=v;busy.value=true;try{await loadCards()}catch(e){error.value=e.message}finally{busy.value=false}}
function changeSize(v){pageSize.value=v;changePage(1)}
onBeforeUnmount(()=>{generation++;opened.value=false;clearTimeout(timer)})
defineExpose({open})
</script>
<template><Teleport to="body"><div v-if="opened" class="modal-backdrop" @click.self="close"><section class="modal pro-auto-dialog" role="dialog" aria-modal="true" aria-label="全自动开通 Pro">
<div class="modal-heading"><h2>全自动开通 Pro</h2><button class="icon-button" title="关闭全自动窗口" :disabled="busy" @click="close"><X :size="17" /></button></div>
<p>{{ email }}</p><ProAutoStages :state="state" />
<p v-if="state.status==='completed'" class="done">全自动流程已完成，已移出空间。</p>
<p v-if="state.status==='waiting_quota'">等待额度达到 {{ state.quota_used_threshold }}%，下次检测：{{ formatTime(state.next_check_at) }}</p>
<p v-if="state.proxy_name">登录代理：{{ state.proxy_name }}<span v-if="state.exit_ip"> · 首次记录 IP：{{ state.exit_ip }}</span></p>
<p v-if="state.error" class="danger-text">{{ state.error }}</p><p v-if="error" role="alert" class="danger-text">{{ error }}</p>
<template v-if="canStart"><p>套餐：{{ proPlanName(settings.plan_code) }}；7 天已用额度达到 {{ settings.quota_used_threshold || 100 }}% 后合并。</p>
<label class="field"><span>开通银行卡</span><select v-model="cardID" :disabled="busy"><option value="">请选择银行卡</option><option v-for="card in cards" :key="card.id" :value="card.id">{{ card.name }} · 尾号 {{ card.last4 }}</option></select></label>
<Pagination v-if="total>10" :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
<p class="muted">首次登录后保留会话等待开通，成功后沿用该会话取得新 RT/AT，再推送 Sub2。关闭窗口不影响后台流程。</p></template>
<p v-if="state.order_id&&!active&&!resumable&&state.status!=='completed'">请先在 GPTPay 供应商页面核实原订单；原会话已释放时，请使用单项按钮处理后续步骤。</p>
<div class="panel-actions"><button v-if="active" class="btn ghost danger-text" :disabled="busy" @click="stop">停止全自动</button><button v-if="resumable" class="btn primary" :disabled="busy" @click="start">继续后续流程</button><button v-if="canStart" class="btn primary" :disabled="busy||!cardID||!settings.key_present" @click="start">确认全自动开通 {{ proPlanName(settings.plan_code) }}</button><button class="btn ghost" :disabled="busy" @click="close">关闭</button></div>
</section></div></Teleport></template>
<style scoped>
.pro-auto-dialog{width:min(720px,calc(100vw - 32px));max-height:90vh;overflow:auto;}
.pro-auto-dialog p{margin:15px 0;overflow-wrap:anywhere;}.pro-auto-dialog .field{margin:15px 0;}.done{color:var(--green-strong);}
</style>
