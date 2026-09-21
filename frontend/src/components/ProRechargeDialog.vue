<script setup>
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import { LoaderCircle, X } from 'lucide-vue-next'
import { api } from '../api'
import { needsPayPoll, payRequestID, payStatusName, proPlanName } from '../gptpay'
import Pagination from './Pagination.vue'

const emit = defineEmits(['updated', 'progress'])
const opened = ref(false), busy = ref(false), error = ref(''), email = ref(''), cards = ref([]), cardID = ref(''), order = ref(null)
const settings = reactive({ plan_code: 'pro5', key_present: false })
const page = ref(1), pageSize = ref(10), total = ref(0)
const chosen = computed(() => cards.value.find(card => card.id === cardID.value))
let requestID = '', timer, disposed = false, generation = 0
async function loadCards() { const result = await api(`/api/gptpay/cards?page=${page.value}&page_size=${pageSize.value}&enabled=true`); cards.value = result.items || []; total.value = result.total || 0; cardID.value = cards.value[0]?.id || '' }
async function open(account) {
  if (busy.value) return
  generation++; clearTimeout(timer); opened.value = true; email.value = account.email; order.value = null; error.value = ''; page.value = 1; cards.value = []; cardID.value = ''; requestID = payRequestID(); busy.value = true
  try { const [config, history] = await Promise.all([api('/api/gptpay/settings'), api(`/api/gptpay/orders?page=1&page_size=10&email=${encodeURIComponent(account.email)}`), loadCards()]); Object.assign(settings, config); order.value = history.items?.find(item => !['success', 'failed'].includes(item.status)) || history.items?.[0] || null; schedule() }
  catch (e) { error.value = e.message }
  finally { busy.value = false }
}
async function changePage(v) { if(busy.value)return; busy.value = true; page.value = v; try { await loadCards() } catch(e) { error.value=e.message } finally { busy.value=false } }
function changeSize(v) { if(busy.value)return; pageSize.value = v; return changePage(1) }
function close() { if (busy.value) return; opened.value = false; generation++; clearTimeout(timer) }
function newOrder() {
  if (busy.value || !order.value || needsPayPoll(order.value)) return
  if (!window.confirm(`上一次订单状态为“${payStatusName(order.value.status)}”。确认要为 ${email.value} 新建一次开通订单？`)) return
  order.value = null; requestID = payRequestID(); error.value = ''; clearTimeout(timer)
}
function schedule() { clearTimeout(timer); if (disposed || !opened.value || !needsPayPoll(order.value)) return; timer = setTimeout(() => refresh(), Date.now() - Date.parse(order.value.created_at) < 60000 ? 5000 : 15000) }
async function refresh() {
  if (busy.value || !order.value) { schedule(); return }
  const gen = generation; busy.value = true
  try { const value = await api(`/api/gptpay/orders/${encodeURIComponent(order.value.id)}/refresh`, { method: 'POST', body: {} }); if (disposed || gen !== generation) return; order.value = value; error.value = ''; emit('updated') }
  catch(e) { error.value = e.message }
  finally { busy.value = false; schedule() }
}
async function submit(retry = false) {
  if (busy.value) return
  busy.value = true; error.value = ''; clearTimeout(timer)
  emit('progress', { email: email.value, running: true })
  try {
    order.value = await api(retry ? `/api/gptpay/orders/${encodeURIComponent(order.value.id)}/retry` : `/api/pro-accounts/${encodeURIComponent(email.value)}/recharge`, { method: 'POST', body: retry ? {} : { card_id: cardID.value, plan_code: settings.plan_code, request_id: requestID } })
    emit('updated')
  } catch(e) {
    error.value = e.message
    // A lost response is not permission to create another purchase.
    try { const history = await api(`/api/gptpay/orders?page=1&page_size=10&email=${encodeURIComponent(email.value)}`); order.value = history.items?.find(item => item.id === 'pro-' + requestID || !['success', 'failed'].includes(item.status)) || order.value } catch { /* Keep the same request ID for an explicit retry. */ }
  } finally { busy.value = false; emit('progress', { email: email.value, running: false }); emit('updated'); schedule() }
}
onBeforeUnmount(() => { disposed = true; generation++; clearTimeout(timer) })
defineExpose({ open })
</script>

<template>
  <Teleport to="body"><div v-if="opened" class="modal-backdrop" @click.self="close"><section class="modal pro-recharge-dialog" role="dialog" aria-modal="true" aria-label="开通 Pro">
    <div class="modal-heading"><h2>开通 Pro</h2><button class="icon-button" title="关闭开通窗口" :disabled="busy" @click="close"><X :size="17" /></button></div>
    <p class="account-email">{{ email }}</p>
    <p v-if="error" class="danger-text" role="alert">{{ error }}</p>
    <template v-if="!order">
      <p>开通套餐：<strong>{{ proPlanName(settings.plan_code) }}</strong></p>
      <p v-if="!settings.key_present" class="danger-text">请先在“Pro 全自动配置”中保存 GPTPay API Key。</p>
      <label class="field"><span>选择银行卡</span><select v-model="cardID" :disabled="busy"><option value="">请选择已启用银行卡</option><option v-for="card in cards" :key="card.id" :value="card.id">{{ card.name }} · 尾号 {{ card.last4 }} · {{ card.exp_year }}/{{ String(card.exp_month).padStart(2, '0') }}</option></select></label>
      <Pagination v-if="total > 10" :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
      <p v-if="!cards.length && !busy">请先到“银行卡信息”添加并启用银行卡。</p>
      <p class="muted">将使用此账号已保存的 AT 和所选银行卡向 GPTPay 提交开通订单，费用按供应商配置扣除。</p>
      <div class="panel-actions"><button class="btn ghost" :disabled="busy" @click="close">取消</button><button class="btn primary" :disabled="busy || !chosen || !settings.key_present" @click="submit(false)"><LoaderCircle v-if="busy" class="spin" :size="15" />{{ busy ? '正在提交…' : '确认开通 ' + proPlanName(settings.plan_code) }}</button></div>
    </template>
    <template v-else>
      <h3 :class="{ 'danger-text': order.status === 'failed' }">{{ proPlanName(order.plan_code) }} · {{ payStatusName(order.status) }}</h3>
      <dl><dt>订单编号</dt><dd>{{ order.remote?.orderNo || order.id }}</dd><dt>供应商订单 ID</dt><dd>{{ order.remote?.id || '尚未返回' }}</dd><dt>银行卡</dt><dd>{{ order.card_name }} · 尾号 {{ order.card_last4 }}</dd><dt>结算 / 取消续费</dt><dd>{{ payStatusName(order.remote?.settlementStatus) }} / {{ payStatusName(order.remote?.cancellationStatus) }}</dd><dt>扣除 Credits</dt><dd>{{ order.remote?.chargedCredits || 0 }}</dd></dl>
      <p v-if="order.error" class="danger-text">{{ order.error }}</p>
      <p v-if="order.status_check_error" class="danger-text">订单同步：{{ order.status_check_error }}；后台会继续查询。</p>
      <p v-if="order.status === 'success' && order.remote?.cancellationStatus === 'failed'" class="muted">Pro 已开通成功；取消续费失败单独记录，不影响开通完成状态。</p>
      <p v-if="needsPayPoll(order)" class="muted">后台正在定时同步订单；关闭窗口或重启服务后仍会继续查询。</p>
      <div class="panel-actions"><button v-if="order.remote?.id" class="btn ghost" :disabled="busy" @click="refresh">查询状态</button><button v-else-if="!['success', 'failed'].includes(order.status)" class="btn primary" :disabled="busy" @click="submit(true)">重试原订单</button><button v-if="['success', 'failed'].includes(order.status) && !needsPayPoll(order)" class="btn ghost" :disabled="busy" @click="newOrder">新建开通</button><button class="btn ghost" :disabled="busy" @click="close">关闭</button></div>
    </template>
  </section></div></Teleport>
</template>

<style scoped>
.pro-recharge-dialog { width:min(650px,calc(100vw - 32px)); max-height:90vh; overflow:auto; }
.account-email,dd,.danger-text { overflow-wrap:anywhere; }
.pro-recharge-dialog .field { margin:18px 0; }
dl { display:grid; grid-template-columns:130px minmax(0,1fr); gap:10px; } dt { color:var(--muted); } dd { margin:0; }
</style>
