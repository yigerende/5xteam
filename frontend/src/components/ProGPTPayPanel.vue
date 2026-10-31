<script setup>
import { dispositionLabels, normalizeOAISettings } from '../pro-disposition'
import { defaultOAIInquiry } from '../oai-confirmation'
import { oaiTemplates } from '../card-platform'
import { computed, nextTick, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { api } from '../api'
import { CreditCard, CalendarClock, SlidersHorizontal, Workflow, Settings2, ChevronRight, Save, ShieldCheck } from 'lucide-vue-next'
import { formatTime } from '../utils'
import { supplierCredits, xupayStates, parseCardText, payStatusName, proPlanName, supplierPaymentLabel, payProviderName, paymentCountryLabel } from '../gptpay'
import XUPayOrderDetails from './XUPayOrderDetails.vue'
import { canCheckXUPay, supplierAmountTitle } from '../gptpay'
import MessageBar from './MessageBar.vue'
import PaymentCardStatus from './PaymentCardStatus.vue'
import CardOccupancy from './CardOccupancy.vue'
import { providerName } from '../nexovcc'
import { isClosedCard } from '../card-status'
import Pagination from './Pagination.vue'
import ProSchedulePanel from './ProSchedulePanel.vue'
import ProProviderRow from './ProProviderRow.vue'
import ProPurchaseGroups from './ProPurchaseGroups.vue'
import { caseSummary, cardHasCase } from '../ejh'
import ReturnCaseLine from './ReturnCaseLine.vue'
import CardAccountPreview from './CardAccountPreview.vue'
import NexovccProCasesDialog from './NexovccProCasesDialog.vue'

const props = defineProps({ mode: String, defaultPageSize: { type: Number, default: 10 }, adminAccounts: { type: Array, default: () => [] } })
const emit = defineEmits(['saved','open-card'])
const settings = reactive({ terms_accepted: false, provider: 'tokenseek', backup_provider: '', country: 'PH', credit_quantity: 0, url: 'https://gptpay.tokenseek.app/api/v1', plan_code: 'pro5', key_present: false, api_key: '', session_timeout_minutes: 30,card_account_limit:3 })
const providerIDs = ['tokenseek', 'cmsnav', 'xupay']
const providerRows = ref([]), savedProvider = ref(''), savedBackup = ref(''), savedVersion = ref(0)
function setProviderRole(provider, role) {
  if (role === 'primary') {
    settings.provider = provider
    if (settings.backup_provider === provider) settings.backup_provider = ''
  } else if (role === 'backup' && provider !== settings.provider) settings.backup_provider = provider
  else if (role === 'disabled' && settings.backup_provider === provider) settings.backup_provider = ''
}
const queryProvider = ref('tokenseek')
const automation = reactive({ merge_v2_recovery:{enabled:true,retry_seconds:30,max_cycles:2,max_attempts:3}, balance_fallback_enabled:false,purchase_schedule_enabled:false,purchase_windows:[],ejh_plan_templates:{},nexovcc_plan_templates:{}, scheduled_enabled:false,max_unmerged:5,schedule_interval_seconds:120,quota_enabled: false, quota_used_threshold: 100, quota_check_interval_seconds: 120, auto_merge_enabled: false, auto_delete_enabled: false, auto_merchant_enabled:false,auto_merge_v2_enabled:false,merchant_success_fee_usd:'0.00', merchant_api_url:'https://refunds.llmrefund.com/v1/refunds', merchant_key_present:false, deletion_check_interval_seconds: 300, target_admin_id: '', target_seat_type: 'default', concurrency: 2, merge_concurrency: 2, retry_count: 2,recharge_replacement_count: 2,recharge_failover_count: 0, retry_interval_seconds: 3 })
automation.oai_confirmation=normalizeOAISettings()
automation.oai_confirmation_concurrency=2
automation.oai_inquiry={...defaultOAIInquiry}
watch(()=>automation.oai_confirmation, value=>{if(!value || Object.keys(dispositionLabels).some(mode=>!value[mode]))automation.oai_confirmation=normalizeOAISettings(value)},{immediate:true})
const merchantAPIKey=ref('')
const selectDisposition=(key)=>{if(automation[key])for(const name of ['auto_merge_enabled','auto_delete_enabled','auto_merchant_enabled','auto_merge_v2_enabled'])if(name!==key)automation[name]=false}
const selectedAdmin = computed(() => props.adminAccounts.find(a => a.id === automation.target_admin_id))
Object.assign(automation,{card_provider:'ejh',nexovcc_auto_open:false,nexovcc_template_id:'',nexovcc_daily_open_limit:1,nexovcc_daily_amount_limit:'',ejh_auto_open:false,ejh_template_id:'',ejh_daily_open_limit:1,ejh_daily_amount_limit:'',force_merge_enabled:false,force_merge_minutes:60})
const nexovccTemplates=ref([])
const nexovccCasesDialog=ref(null)
function showCardAccounts(card){if(card.card_type==='nexovcc')nexovccCasesDialog.value.open({cardID:associated(card).find(c=>c.nexovcc)?.nexovcc.card_id||card.id});else cardDetail.value=card}
const ejhTemplates=ref([]),cardCases=ref([]),cardDetail=ref(null)
function associated(card){return cardCases.value.filter(c=>cardHasCase(card,c)||c.nexovcc?.card_id===card.id)}
function cardHint(card){return associated(card).slice(0,30).map(caseSummary).join('\n')||'暂无关联账号'}
const purchasePlans = [{code:'pro5',label:'5x'},{code:'pro20',label:'20x'}]
function matchingTemplates(provider,plan){ return (provider==='ejh'?ejhTemplates.value:nexovccTemplates.value).filter(t=>t.plan_code===plan) }
function addPurchaseWindow(){ automation.purchase_windows.push({start:'17:00',end:'20:50',card_provider:automation.card_provider||'ejh',plan_code:'pro5',balance_fallback_enabled:false,balance_fallback_provider:'',extra_sub2_group_ids:[]}) }
const purchaseGroups = ref([]), purchaseGroupsLoading = ref(false), purchaseGroupsError = ref(''), purchaseGroupsLoaded = ref(false)
let purchaseGroupsController
async function loadPurchaseGroups(force = false) {
  if (disposed || automation.provider !== 'sub2' || purchaseGroupsLoading.value || (!force && purchaseGroupsLoaded.value)) return
  purchaseGroupsLoading.value = true; purchaseGroupsError.value = ''
  const controller = purchaseGroupsController = new AbortController()
  try {
    const result = await api('/api/pro-settings/sub2/groups', { signal: controller.signal })
    if (disposed) return
    purchaseGroups.value = result.groups || result || []; purchaseGroupsLoaded.value = true
  } catch (e) {
    if (!disposed && !controller.signal.aborted) purchaseGroupsError.value = e.message
  } finally {
    if (!disposed) purchaseGroupsLoading.value = false
  }
}
function purchaseTemplateName(w){const id=(w.card_provider==='ejh'?automation.ejh_plan_templates:automation.nexovcc_plan_templates)?.[w.plan_code];return matchingTemplates(w.card_provider,w.plan_code).find(t=>t.id===id)?.name||'请在银行卡与补卡选择对应模板'}
watch(()=>automation.ejh_plan_templates,v=>{if(!v)automation.ejh_plan_templates={}},{immediate:true})
watch(()=>automation.nexovcc_plan_templates,v=>{if(!v)automation.nexovcc_plan_templates={}},{immediate:true})
watch(()=>automation.purchase_windows,v=>{if(!v)automation.purchase_windows=[]},{immediate:true})
const configSection = ref('purchase')
const configSections = [
  { id: 'purchase', label: '开通设置', hint: '供应商、套餐与国家', icon: CreditCard },
  { id: 'schedule', label: '定时任务', hint: '自动开通与任务容量', icon: CalendarClock },
  { id: 'cards', label: '银行卡与补卡', hint: '用卡上限、卡台与预算', icon: CreditCard },
  { id: 'merge', label: '空间合并/删除账户/API 商户/合并2.0', hint: '额度条件、超时与目标', icon: Workflow },
  { id: 'advanced', label: '高级设置', hint: '并发、重试与会话', icon: SlidersHorizontal },
]
const currentConfigSection = computed(() => configSections.find(s => s.id === configSection.value))
function revealInvalid(event) {
  event.preventDefault()
  const section = event.target.closest('[data-config-section]')
  if (section) configSection.value = section.dataset.configSection
  notify(event.target.validationMessage || '请检查必填项和数值范围', 'error')
  nextTick(() => event.target.focus())
}
const settingsReady = ref(false)
const busy = ref(false), message = reactive({ text: '', type: '' }), account = ref(null)
const rows = ref([]), page = ref(1), pageSize = ref(props.defaultPageSize), total = ref(0)
const cardFilter = ref('available')
const queryIDs = ref(''), queryRows = ref([]), emailFilter = ref('')
const editor = ref(null), rawCard = ref('')
let disposed = false, requestID = 0
const notify = (text = '', type = '') => Object.assign(message, { text, type })
async function action(fn) { if (busy.value) return; busy.value = true; notify(); try { await fn() } catch (e) { notify(e.message, 'error') } finally { busy.value = false } }
async function load() {
  const id = ++requestID
  const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize.value) })
  if (props.mode === 'cards' && cardFilter.value === 'available') params.set('enabled', 'true')
  if (props.mode === 'orders' && emailFilter.value.trim()) params.set('email', emailFilter.value.trim())
  const result = await api(`/api/gptpay/${props.mode === 'cards' ? 'cards' : 'orders'}?${params}`)
  if (disposed || id !== requestID) return
  rows.value = result.items || []; total.value = result.total || 0
  if(props.mode==='cards'){const d=await api('/api/gptpay/card-cases');if(!disposed&&id===requestID)cardCases.value=d||[]}
  if (page.value > Math.max(1, Math.ceil(total.value / pageSize.value))) { page.value = Math.max(1, Math.ceil(total.value / pageSize.value)); await load() }
}
function changePage(v) { page.value = v; action(load) }
function changeSize(v) { pageSize.value = v; page.value = 1; action(load) }
function changeCardFilter(value) {
  if (busy.value || cardFilter.value === value) return
  action(async () => {
    cardFilter.value = value; page.value = 1; rows.value = []; total.value = 0
    await load()
  })
}
async function saveSettings() {
  if (!settingsReady.value || providerRows.value.length !== 3) return
  await action(async () => {
    const saved = await api('/api/pro-settings/automation', { method: 'PUT', body: { automation:{...automation,merchant_success_fee_usd:String(automation.merchant_success_fee_usd??'0.00')}, gptpay: settings, providers: providerRows.value, merchant_api_key:merchantAPIKey.value } })
    Object.assign(settings, saved.gptpay, { api_key: '', backup_provider: saved.gptpay.backup_provider || '' }); Object.assign(automation, saved.automation); merchantAPIKey.value=''
    providerRows.value = saved.providers.map(row => ({ ...row, api_key: '' })); savedProvider.value = saved.gptpay.provider; savedBackup.value = settings.backup_provider; savedVersion.value++
    notify('Pro 全自动配置已保存', 'success'); emit('saved')
  })
}
async function queryAccount() { await action(async () => { account.value = await api('/api/gptpay/account?provider=' + queryProvider.value); notify('供应商账户信息已更新', 'success') }) }
function edit(card = null) {
  editor.value = { id: card?.id || '', source: card?.source || '', card_type: card?.card_type || 'other', name: card?.name || '', closed: isClosedCard(card), enabled: isClosedCard(card) ? false : card?.enabled ?? true, number: '', cvv: '', exp_month: card?.exp_month || 0, exp_year: card?.exp_year || 0, last4: card?.last4 || '', max_accounts: card?.max_accounts || 0 }
  rawCard.value = ''; notify()
}
function parseCard() { try { Object.assign(editor.value, parseCardText(rawCard.value)); rawCard.value = ''; notify('已解析，请核对有效期后保存', 'success') } catch (e) { notify(e.message, 'error') } }
async function saveCard() {
  await action(async () => {
    const card = editor.value
    if (rawCard.value.trim()) Object.assign(card, parseCardText(rawCard.value))
    const maxAccounts = Number(card.max_accounts || 0)
    if (!Number.isInteger(maxAccounts) || maxAccounts < 0 || maxAccounts > 10000) throw new Error('最大开通数须为 1～10000，或填 0 沿用全局上限')
    await api('/api/gptpay/cards' + (card.id ? '/' + encodeURIComponent(card.id) : ''), { method: card.id ? 'PUT' : 'POST', body: { card_type: card.card_type, name: card.name, enabled: card.enabled, number: card.number, cvv: card.cvv, exp_month: card.exp_month, exp_year: card.exp_year, ...(card.id || maxAccounts > 0 ? { max_accounts: maxAccounts } : {}) } })
    editor.value = null; rawCard.value = ''; await load(); notify('银行卡已保存', 'success')
  })
}
async function toggle(card) { if (isClosedCard(card)) return; await action(async () => { await api('/api/gptpay/cards/' + encodeURIComponent(card.id), { method: 'PUT', body: { enabled: !card.enabled } }); await load() }) }
async function remove(card) {
  if (!window.confirm(`删除银行卡“${card.name} · 尾号 ${card.last4}”？已提交的订单记录会保留。`)) return
  await action(async () => { await api('/api/gptpay/cards/' + encodeURIComponent(card.id), { method: 'DELETE' }); await load(); notify('银行卡已删除', 'success') })
}
async function refreshOrder(order, retry = false) {
  await action(async () => { const result = await api(`/api/gptpay/orders/${encodeURIComponent(order.id)}/${retry ? 'retry' : 'refresh'}`, { method: 'POST', body: {} }); await load(); notify(`${result.email}：${payStatusName(result.status)}${result.error ? ' · ' + result.error : ''}`, result.status === 'failed' ? 'error' : 'success') })
}
async function checkOrder(order) {
  await action(async () => { await api(`/api/gptpay/orders/${encodeURIComponent(order.id)}/check`, { method: 'POST', body: {} }); await load(); notify('核实原单已受理，正在跟踪结果', 'success') })
}
async function queryOrders() {
  await action(async () => {
    const ids = [...new Set(queryIDs.value.split(/[\s,，]+/).filter(Boolean))]
    if (!ids.length || ids.length > 50) throw new Error('请输入 1～50 个供应商订单 ID')
    const result = await api('/api/gptpay/orders/query', { method: 'POST', body: { provider: queryProvider.value, order_ids: ids } }); queryRows.value = result.orders || []; notify('供应商订单状态已更新', 'success')
  })
}
onMounted(() => action(async () => {
  if (props.mode === 'settings') {
    const [ejh,nex]=await Promise.all([api('/api/ejh/settings'),api('/api/nexovcc/settings')]);ejhTemplates.value=oaiTemplates(ejh.templates);nexovccTemplates.value=oaiTemplates(nex.templates)
    const [pay, pro, providers] = await Promise.all([api('/api/gptpay/settings'), api('/api/pro-settings'), Promise.all(providerIDs.map(provider => api('/api/gptpay/settings?provider=' + provider)))])
    if (disposed) return
    Object.assign(settings, pay); savedProvider.value = pay.provider; savedBackup.value = pay.backup_provider || ''
    providerRows.value = providers.map(row => ({ ...(row.provider === pay.provider ? pay : row), api_key: '' }))
    Object.assign(automation, pro); settingsReady.value = true
    if (automation.purchase_windows?.some(w => w.extra_sub2_group_ids?.length)) void loadPurchaseGroups()
  } else {
    if (props.mode === 'orders') { const config = await api('/api/gptpay/settings'); queryProvider.value = config.provider || 'tokenseek' }
    await load()
  }
}))
onBeforeUnmount(() => { disposed = true; purchaseGroupsController?.abort(); editor.value = null; rawCard.value = ''; settings.api_key = ''; providerRows.value.forEach(row => { row.api_key = '' }); providerRows.value = [] })
</script>

<template>
  <div class="gptpay-panel view-stack">
    <NexovccProCasesDialog ref="nexovccCasesDialog" :default-page-size="defaultPageSize" @open-card="emit('open-card',$event)" @updated="load"/>
    <Teleport to="body"><div v-if="cardDetail" class="modal-backdrop" @click.self="cardDetail=null"><section class="modal" style="max-height:80vh;overflow:auto"><h2>{{cardDetail.name}} · 关联账号</h2><ReturnCaseLine v-for="c in associated(cardDetail)" :key="c.id" :row="c"/><p v-if="!associated(cardDetail).length">暂无关联账号</p><button class="btn" @click="cardDetail=null">关闭</button></section></div></Teleport>
    <MessageBar v-if="message.text" :message="message" />
    <form v-if="mode === 'settings'" class="automation-config" @submit.prevent="saveSettings" @invalid.capture="revealInvalid">
      <header class="config-heading">
        <div><span class="overline">PRO AUTOMATION</span><h2>全自动配置</h2><p>按流程配置，修改后统一保存。切换分组不会丢失已填内容。</p></div>
        <span class="config-badge" :class="{ enabled: automation.scheduled_enabled }"><span></span>{{ automation.scheduled_enabled ? '定时开通已开启' : '定时开通未开启' }}</span>
      </header>
      <div class="config-workspace">
        <nav class="config-nav" aria-label="全自动配置分组">
          <button v-for="item in configSections" :key="item.id" type="button" :aria-current="configSection === item.id ? 'page' : undefined" :class="{ selected: configSection === item.id }" @click="configSection = item.id">
            <component :is="item.icon" :size="18"/><span><strong>{{ item.label }}</strong><small>{{ item.hint }}</small></span><ChevronRight :size="14"/>
          </button>
          <div class="config-nav-note"><ShieldCheck :size="17"/><p>配置保存不会立即开通。真实开通仍按原任务流程执行。</p></div>
        </nav>
        <div class="config-body">
          <header class="config-section-heading"><span>配置 / {{ currentConfigSection.label }}</span><h3>{{ currentConfigSection.label }}</h3><p>{{ currentConfigSection.hint }}</p></header>
          <section v-show="configSection === 'purchase'" data-config-section="purchase">
            <div class="provider-heading"><div><h4>供应商套餐与支付地区</h4><p>可设置一家主供应商和一家备用供应商。修改后点击底部“保存 Pro 配置”生效。</p></div><span v-if="savedProvider" class="config-badge enabled"><span></span>主启用：{{ payProviderName(savedProvider) }}</span></div>
            <p v-if="!settingsReady" class="muted">正在读取三家供应商的已有配置…</p>
            <div v-else class="table-shell provider-table-shell"><table class="provider-table"><thead><tr><th>供应商 / 接口配置</th><th>开通套餐</th><th>支付地区</th><th>启用状态</th><th>操作</th></tr></thead><tbody><ProProviderRow v-for="row in providerRows" :key="row.provider" :row="row" :selected="settings.provider" :backup="settings.backup_provider" :saved-provider="savedProvider" :saved-backup="savedBackup" :saved-version="savedVersion" :disabled="busy" @role="setProviderRole(row.provider, $event)" /></tbody></table></div>
            <p class="provider-note">报价为供应商服务费，不是银行卡扣款金额。新任务默认使用主供应商，已有订单沿用原供应商。备用选择仅保存配置，尚未接入自动切备。</p>
            <div class="config-divider"><h4>分时开通</h4><p>用于自动补货与定时全自动；手动开通自行选择。时段外使用默认套餐和卡台。</p></div>
            <label class="toggle-row"><div><strong>时段外／手动开通允许备用卡台</strong><small>未命中分时时段时使用此设置；时段内按下方每个时段单独设置。仅明确余额不足时切换另一卡台同套餐 OAI 模板。</small></div><input v-model="automation.balance_fallback_enabled" type="checkbox"/><i></i></label>
            <label class="toggle-row"><div><strong>启用分时开通</strong><small>北京时间，支持跨天；每日禁补规则仍独立生效。</small></div><input v-model="automation.purchase_schedule_enabled" type="checkbox"/><i></i></label>
            <div v-for="(w,i) in automation.purchase_windows" :key="i" class="purchase-window">
              <div class="settings-fields">
                <label class="field"><span>开始时间</span><input v-model="w.start" type="time" required/></label>
                <label class="field"><span>结束时间</span><input v-model="w.end" type="time" required/><small v-if="w.end<w.start">次日结束</small></label>
                <label class="field"><span>卡台</span><select v-model="w.card_provider"><option value="ejh">EJH</option><option value="nexovcc">环球</option></select></label>
                <label class="field"><span>开通套餐</span><select v-model="w.plan_code"><option value="pro5">5x</option><option value="pro20">20x</option></select><small>模板：{{purchaseTemplateName(w)}}</small></label>
              </div>
              <label class="toggle-row"><div><strong>本时段余额不足时启用备用卡台</strong><small>保留套餐和推送分组；已开始的任务沿用启动时规则，付款结果未知时不切换。</small></div><input v-model="w.balance_fallback_enabled" type="checkbox"/><i></i></label>
              <label v-if="w.balance_fallback_enabled" class="field"><span>备用卡台</span><select v-model="w.balance_fallback_provider"><option value="">自动选择另一卡台</option><option v-if="w.card_provider!=='ejh'" value="ejh">EJH</option><option v-if="w.card_provider!=='nexovcc'" value="nexovcc">环球</option></select><small>模板：{{purchaseTemplateName({...w,card_provider:w.balance_fallback_provider||(w.card_provider==='ejh'?'nexovcc':'ejh')})}}</small></label>
              <ProPurchaseGroups v-model="w.extra_sub2_group_ids" :groups="purchaseGroups" :base-ids="automation.sub2?.group_ids || []" :base-names="automation.sub2?.group_names || []" :enabled="automation.provider === 'sub2'" :loading="purchaseGroupsLoading" :error="purchaseGroupsError" @load="loadPurchaseGroups()" @refresh="loadPurchaseGroups(true)" />
              <button class="btn ghost compact danger-text" type="button" @click="automation.purchase_windows.splice(i,1)">删除时段</button>
            </div>
            <button class="btn ghost compact" type="button" :disabled="automation.purchase_windows.length>=32" @click="addPurchaseWindow">添加时段</button>
            <p class="muted">供应商与支付国家沿用上方配置。时段不可重叠；禁补允许指定卡台时，用该卡台的同套餐模板，完全禁补时暂停。</p>
          </section>
          <section v-show="configSection === 'schedule'" data-config-section="schedule">
            <div class="settings-fields">        <label class="toggle-row wide"><div><strong>定时执行全自动 Pro</strong><small>从 Pro 管理选择未开通过的新账号，自动选择有名额的银行卡，登录 → 开通 → RT/AT → Sub2 → 刷新额度。</small></div><input v-model="automation.scheduled_enabled" type="checkbox"/><i></i></label>
        <label class="field"><span>开通并在 Sub 最大数（未合并）</span><input v-model.number="automation.max_unmerged" type="number" min="1" max="10000" required/><small>已开通未合并及开通在途共同占位，避免超出上限。</small></label>
        <label class="field"><span>定时开通检查间隔（秒）</span><input v-model.number="automation.schedule_interval_seconds" type="number" min="10" max="86400" required/><small>本批推送成功并完成首次额度刷新后，才允许下一批启动。</small></label>
</div>
            <div class="config-callout">仅选择未开通过的新账号；登录和付款在途也占用容量。上一批推送并完成首次额度刷新后，才启动下一批。</div>
          </section>
          <section v-show="configSection === 'cards'" data-config-section="cards">
            <div class="settings-fields">        <label class="field"><span>银行卡默认最大开通数</span><input v-model.number="settings.card_account_limit" type="number" min="1" max="10000" required/><small>未单独设置的卡沿用此上限，默认 3 个。成功开通与在途占位共同计数，同一卡号共用上限。</small></label>
</div>
            <div class="config-divider"><h4>自动补卡</h4><p>按所选卡台优先使用最早开出的可用卡，无可用卡时补卡。各卡台单独保存模板和预算。</p></div>
            <label class="field"><span>默认卡台</span><select v-model="automation.card_provider"><option value="ejh">EJH</option><option value="nexovcc">环球</option></select><small>手动开通可临时切换；自动任务优先按分时开通规则选择，禁补规则独立限制。</small></label>
            <div class="config-divider"><h4>EJH 补卡设置</h4></div>
            <div class="settings-fields">        <label class="toggle-row wide"><div><strong>无可用卡时自动从 EJH 开卡</strong><small>优先使用现有可用卡；新卡加入银行卡信息。结果未知时停止等待核实，不重复扣款。</small></div><input v-model="automation.ejh_auto_open" type="checkbox"/><i></i></label>
        <label v-for="plan in purchasePlans" :key="'ejh-'+plan.code" class="field"><span>EJH {{plan.label}} 模板（分时）</span><select v-model="automation.ejh_plan_templates[plan.code]"><option value="">请选择已标记的 {{plan.label}} 模板</option><option v-for="t in matchingTemplates('ejh',plan.code)" :key="t.id" :value="t.id">{{t.name}}</option></select><small>到 EJH 卡台 → 开卡模板设置“适用套餐”。</small></label>
              <label class="field"><span>EJH 开卡模板</span><select v-model="automation.ejh_template_id" :required="automation.ejh_auto_open"><option value="">请先到 EJH 卡台保存模板</option><option v-for="t in ejhTemplates" :key="t.id" :value="t.id">{{t.name}} · {{t.cardAmount}} {{t.cardCurrency}}</option></select></label>
        <label class="field"><span>自动开卡每日数量上限</span><input v-model.number="automation.ejh_daily_open_limit" type="number" min="1" max="1000" :required="automation.ejh_auto_open"/></label>
        <label class="field wide"><span>自动开卡每日刷卡额度预算（模板币种）</span><input v-model="automation.ejh_daily_amount_limit" :required="automation.ejh_auto_open" placeholder="例如 100"/><small>累计新卡额度，不含手续费和汇率差，不是实际扣款上限；按北京时间每日计算。</small></label>
</div>
            <div class="config-divider"><h4>环球补卡设置</h4><p>手动选环球或时段允许环球时，使用这里的模板和预算。</p></div>
            <div class="settings-fields">
              <label class="toggle-row wide"><div><strong>无可用卡时自动从环球开卡</strong><small>优先使用环球可用卡；原开卡结果待核实时停止补卡，查询原卡继续导入。</small></div><input v-model="automation.nexovcc_auto_open" type="checkbox"/><i></i></label>
              <label v-for="plan in purchasePlans" :key="'nexovcc-'+plan.code" class="field"><span>环球 {{plan.label}} 模板（分时）</span><select v-model="automation.nexovcc_plan_templates[plan.code]"><option value="">请选择已标记的 {{plan.label}} 模板</option><option v-for="t in matchingTemplates('nexovcc',plan.code)" :key="t.id" :value="t.id">{{t.name}}</option></select><small>到环球卡台 → 开卡模板设置“适用套餐”。</small></label>
              <label class="field"><span>环球开卡模板</span><select v-model="automation.nexovcc_template_id" :required="automation.nexovcc_auto_open"><option value="">请先到环球卡台保存模板</option><option v-for="t in nexovccTemplates" :key="t.id" :value="t.id">{{t.name}} · {{t.amount}} USD</option></select></label>
              <label class="field"><span>自动开卡每日数量上限</span><input v-model.number="automation.nexovcc_daily_open_limit" type="number" min="1" max="1000" :required="automation.nexovcc_auto_open"/></label>
              <label class="field wide"><span>自动开卡每日充值金额预算（USD）</span><input v-model="automation.nexovcc_daily_amount_limit" :required="automation.nexovcc_auto_open" placeholder="例如 100"/><small>累计新卡充值金额；按北京时间每日计算。平台手续费另计。</small></label>
            </div>
          </section>
          <section v-show="configSection === 'merge'" data-config-section="merge">
            <p class="config-callout">空间合并、删除账户、API 商户、合并空间2.0只能开启一个。额度达标、推送超时、降智直接处理和每日 EJH 定点处理均执行所选动作；全自动沿用启动时的额度配置。四个开关均关闭时保留原全自动合并行为。</p>
            <label class="field"><span>合并/删除/API 商户/2.0并发数</span><input v-model.number="automation.merge_concurrency" type="number" min="1" max="20" required /><small>同时执行的合并或删除账户任务上限，包含后续处理；同一母号严格串行，不同母号可并行。与高级设置的全流程并发分别计数。</small></label>
            <div class="settings-fields">        <label class="field"><span>7 天已用额度达到（%）</span><input v-model.number="automation.quota_used_threshold" type="number" min="0.01" max="100" step="0.01" required /></label>
        <label class="field"><span>额度检测间隔（秒）</span><input v-model.number="automation.quota_check_interval_seconds" type="number" min="10" max="86400" required /></label>
        <label class="field wide"><span>删除结果核验间隔（秒）</span><input v-model.number="automation.deletion_check_interval_seconds" type="number" min="1" max="86400" step="1" required /><small>默认 300 秒。单独控制删除提交后的结果核验；修改后已有等待任务按新间隔安排。确认删除成功后停止核验，不再提交删除；未完成的分组调整仍可续跑。</small></label>
        <label class="toggle-row wide"><div><strong>定时查额度</strong><small>定时查询已推送且未合并的手动账号，并保存最新额度到数据库。</small></div><input v-model="automation.quota_enabled" type="checkbox" /><i></i></label>
        <label class="toggle-row wide"><div><strong>查额度达标后自动合并</strong><small>查额度成功且达到阈值时执行合并；定时触发需同时开启“定时查额度”。401、超时或无 7 天额度数据不触发。</small></div><input v-model="automation.auto_merge_enabled" @change="selectDisposition('auto_merge_enabled')" type="checkbox" /><i></i></label>
        <label class="toggle-row wide"><div><strong>查额度达标后自动删除</strong><small>使用当前账号登录凭据执行个人账户删除。已受理立即调整所选分组，最终删除状态单独核验；回款仍以卡台流水为准。</small></div><input v-model="automation.auto_delete_enabled" @change="selectDisposition('auto_delete_enabled')" type="checkbox"/><i></i></label>
        <label class="toggle-row wide"><div><strong>查额度达标后 API 商户</strong><small>将账号、ChatGPT 密码和 2FA 提交给商户，每 15 秒查询处理进度；最终成功才完成。不执行空间合并或本地删除账户流程。</small></div><input v-model="automation.auto_merchant_enabled" @change="selectDisposition('auto_merchant_enabled')" type="checkbox"/><i></i></label>
        <label class="toggle-row wide"><div><strong>查额度达标后合并空间2.0</strong><small>邀请 → 重新登录受邀团队 → 提交导出停用 → 移出团队 → 调整分组。受理后移出并收尾；结果不明时按下方配置移出、核验个人套餐。退款仍以卡台流水为准。</small></div><input v-model="automation.auto_merge_v2_enabled" @change="selectDisposition('auto_merge_v2_enabled')" type="checkbox"/><i></i></label>
        <section class="oai-confirmation-settings wide" aria-label="合并2.0自动恢复设置">
          <h4>合并2.0 · 移出后套餐核验</h4>
          <label class="toggle-row"><div><strong>提交结果不明时自动恢复</strong><small>沿用原账号、原团队和代理，先移出并复查，再重新登录个人空间。明确 Free 则完成收尾；明确仍付费才重新排队办理。等待期间释放执行名额，超限保留证据等待人工处理。</small></div><input v-model="automation.merge_v2_recovery.enabled" type="checkbox"/><i></i></label>
          <div class="settings-fields">
            <label class="field"><span>恢复间隔（秒）</span><input v-model.number="automation.merge_v2_recovery.retry_seconds" type="number" min="5" max="3600" required/></label>
            <label class="field"><span>最多恢复轮次</span><input v-model.number="automation.merge_v2_recovery.max_cycles" type="number" min="1" max="5" required/></label>
            <label class="field"><span>每轮最多核验次数</span><input v-model.number="automation.merge_v2_recovery.max_attempts" type="number" min="1" max="10" required/></label>
          </div>
          <p class="table-note">默认 30 秒、2 轮、每轮 3 次。停止账号或停止全自动最后一步时暂停恢复；重启保留次数和进度。套餐核验完成不代表退款到账。</p>
        </section>
        <section class="oai-confirmation-settings wide" aria-label="OAI 确认设置">
          <h4>流程完成后询问 OAI</h4>
          <p class="table-note">各流程完成后独立等待并询问 Help，默认等待 300 秒、间隔 300 秒、总计尝试 3 次。已确认或已回款后停止；待核实也按间隔和次数重试，历史完成账号不会自动补问。删除账户前会尝试保留 Help 会话。</p>
          <label class="oai-delay"><span>OAI 确认并发数</span><input v-model.number="automation.oai_confirmation_concurrency" type="number" min="1" max="100" step="1" required aria-label="OAI 确认并发数"/><span>个账号（1～100）</span></label>
          <p class="table-note">四种流程共用此并发；同一账号串行。降低并发后，正在执行的任务先完成。</p>
          <label class="field"><span>提问内容</span><textarea v-model="automation.oai_inquiry.prompt" rows="6" maxlength="6000" required aria-label="OAI 提问内容"/><small v-pre>可用 {{email}} 自动填入邮箱，{{success_marker}}、{{failure_marker}}、{{review_marker}} 自动填入下方标识。修改后用于新一轮提问。</small></label>
          <div class="oai-markers"><label v-for="(label,key) in {success_marker:'成功标识',failure_marker:'失败标识',review_marker:'待核实标识'}" :key="key" class="field"><span>最后一行 · {{label}}</span><input v-model="automation.oai_inquiry[key]" maxlength="40" required :aria-label="'OAI '+label"/></label></div>
          <p class="table-note">最后一行按标识识别；成功回答仍须明确已发起／处理退款，且正文没有冲突。</p>
          <div v-for="(label,mode) in dispositionLabels" :key="mode" class="oai-rule">
            <label class="oai-rule-toggle"><input v-model="automation.oai_confirmation[mode].enabled" type="checkbox" :aria-label="label+'完成后询问 OAI'"/><span>{{label}}完成后询问</span></label>
            <label class="oai-delay"><span>等待</span><input v-model.number="automation.oai_confirmation[mode].delay_seconds" type="number" min="1" max="604800" step="1" required :aria-label="label+' OAI 询问等待秒数'"/><span>秒</span></label>
            <label class="oai-delay"><span>重试间隔</span><input v-model.number="automation.oai_confirmation[mode].retry_interval_seconds" type="number" min="1" max="604800" step="1" required :aria-label="label+' OAI 重试间隔秒数'"/><span>秒</span></label>
            <label class="oai-delay"><span>总尝试</span><input v-model.number="automation.oai_confirmation[mode].max_attempts" type="number" min="1" max="10" step="1" required :aria-label="label+' OAI 总尝试次数'"/><span>次</span></label>
          </div>
        </section>
        <label class="field wide"><span>API 商户每次成功收费（USD）</span><input v-model="automation.merchant_success_fee_usd" type="number" min="0" max="1000000" step="0.01" required/><small>最终处理成功后计入所属卡台的 API 收费成本；每个申请只记一次，提交时锁定金额，默认 0。</small></label>
        <label class="field wide"><span>API 商户地址</span><input v-model="automation.merchant_api_url" type="url" placeholder="https://refunds.llmrefund.com/v1/refunds"/><small>填写完整提交接口，例如 http://127.0.0.1:18740/open/v1/requests 或 http://127.0.0.1:18740/v1/cemifh。直接请求填写的地址，进度查询在其后加 /申请ID；留空使用原接口。</small></label>
        <label class="field wide"><span>API 商户密钥</span><input v-model="merchantAPIKey" type="password" autocomplete="new-password" :placeholder="automation.merchant_key_present?'已保存，留空保留原密钥':'填写该商户的 API Key'"/><small>密钥加密保存，不回显。修改地址和密钥仅影响新申请；已有申请继续使用提交时的地址和密钥。</small></label>
        <label class="field wide"><span>目标母号（空间合并及2.0使用）</span><select v-model="automation.target_admin_id"><option value="">请选择母号</option><option v-for="admin in adminAccounts" :key="admin.id" :value="admin.id">{{ admin.label || admin.email }} · {{ admin.team_account_id || '无 Team ID' }}</option></select></label>
        <label class="field"><span>空间席位套餐</span><select v-model="automation.target_seat_type"><option value="default">Standard（普通空间）</option><option value="prolite">Premium（5x）</option></select></label>
        <div class="target-summary wide"><strong>{{ selectedAdmin?.label || selectedAdmin?.email || '尚未选择母号' }}</strong><span>{{ selectedAdmin?.team_account_id || '选择后显示目标 Team ID' }}</span></div>
</div>
            <div class="config-divider"><h4>推送等待时限</h4><p>从首次推送开始计时，重试和重启不清零。</p></div>
            <div class="settings-fields">        <label class="toggle-row wide"><div><strong>推送超时执行合并/删除/API 商户/2.0</strong><small>保存后同步更新尚未开始合并/删除的在途账号，仍从首次成功推送开始计时。缩短至已超时会在后续检查时执行；重试和重启不清零，停止、暂停和待确认任务仍遵守原规则。按当前选择执行空间合并、删除账户或 API 商户；API 商户模式会提交退款申请并查询进度，卡台到账仍以真实流水为准。</small></div><input v-model="automation.force_merge_enabled" type="checkbox"/><i></i></label>
        <label class="field"><span>首次推送后最多等待（分钟）</span><input v-model.number="automation.force_merge_minutes" type="number" min="1" max="525600" :required="automation.force_merge_enabled"/></label>
</div>
          </section>
          <section v-show="configSection === 'advanced'" data-config-section="advanced">
            <p class="config-callout">一般保留当前值即可。资金操作的未知结果不会因重试设置而自动重复提交。</p>
            <div class="settings-fields">        <label class="field"><span>并发账号数</span><input v-model.number="automation.concurrency" type="number" min="1" max="20" required /><small>控制从登录、开通、获取凭证到推送、额度检测及合并的活动链条。等待下一次额度检测时释放名额；进入合并阶段还需遵守空间合并页的合并并发数。</small></label>
<label class="field"><span>第二步失败换号次数</span><input v-model.number="automation.recharge_replacement_count" type="number" min="0" max="10"><small>仅第二步开通失败：换一个新账号，从登录开始，不重试原账号。自动补货、定时全自动、手动启动全自动均生效。2 次表示首次账号之外最多再换 2 个；0 为主供应商首次失败后结束本轮。每条流程启动时固定次数，原失败账号和订单保留。</small></label>
<label class="field"><span>切换供应商后账号尝试次数</span><input v-model.number="automation.recharge_failover_count" type="number" min="0" max="10"><small>0 关闭自动切换；1～10 为切换后总共领取的账号数。主供应商本轮次数用尽且第二步仍失败时，交换主启用与备启用，并继续同一个任务。每个任务最多切换一次，需先配置备用供应商及密钥。</small></label>
<label class="field"><span>其他步骤失败重试次数</span><input v-model.number="automation.retry_count" type="number" min="0" max="10"><small>保留登录及后续步骤的原有重试规则，不用于第二步开通。</small></label>
        <label class="field"><span>重试间隔（秒）</span><input v-model.number="automation.retry_interval_seconds" type="number" min="1" required /><small>失败后从登录重新执行的等待时间；登录阶段代理连接失败也按此间隔更换代理会话，最多额外重试 3 次，不占账号业务重试次数。已付款或付款待确认时不会重新付款。修改后在下一次安排重试时生效。</small></label>
        <label class="field"><span>登录及开通会话最长保留（分钟）</span><input v-model.number="settings.session_timeout_minutes" type="number" min="5" max="60" required /><small>出口 IP 变化或检测失败仅记录日志，继续执行；任务超时仍会停止，已提交订单可查询。</small></label>
</div>
          </section>
        </div>
      </div>
      <footer class="config-savebar"><span>{{ busy ? '正在处理，请稍候…' : !settingsReady ? '正在读取配置…' : '修改应用于后续任务，已有订单保留原供应商。' }}</span><button class="btn primary" :disabled="busy || !settingsReady"><Save :size="16"/>保存 Pro 配置</button></footer>
    </form>

    <template v-else-if="mode === 'cards'">
      <section class="panel">
        <div class="panel-title"><div><span>PAYMENT CARDS</span><h2>银行卡信息</h2><p>卡片须启用、未过期且有剩余名额；已绑定卡台的卡还须卡台已启用且卡片状态可用。</p></div><button class="btn primary" :disabled="busy" @click="edit()">添加银行卡</button></div>
        <div class="card-filter" role="group" aria-label="银行卡筛选"><button v-for="[value,label] in [['available','可开通'],['all','全部']]" :key="value" type="button" :disabled="busy" :class="{active:cardFilter===value}" :aria-pressed="cardFilter===value" @click="changeCardFilter(value)">{{label}}</button><span>{{busy?'正在读取…':`共 ${total} 张`}}</span></div>
        <div class="table-shell"><table><thead><tr><th>名称</th><th>类型</th><th>银行卡</th><th>已开通账号</th><th>有效期</th><th>状态</th><th>操作</th></tr></thead><tbody>
          <tr v-if="!rows.length"><td colspan="7" class="empty-cell">{{busy?'正在读取银行卡…':cardFilter==='available'?'暂无可开通银行卡，可切换“全部”查看':'暂无银行卡'}}</td></tr>
          <tr v-for="card in rows" :key="card.id"><td>{{ card.name }}</td><td>{{providerName(card.card_type)}}</td><td>•••• {{ card.last4 }}</td><td><CardAccountPreview :rows="associated(card)"><button class="btn ghost compact" @click="showCardAccounts(card)">{{card.opened_accounts||0}} / {{card.account_limit||3}}</button></CardAccountPreview><small class="order-id">{{ card.max_accounts ? '单独设置' : card.card_type==='ejh'?'EJH 默认':card.card_type==='nexovcc'?'环球默认':'沿用全局' }}</small><small class="order-id">在途 {{card.pending_accounts||0}} · 剩余 {{card.remaining_accounts??0}}</small><CardOccupancy v-if="['ejh','nexovcc'].includes(card.card_type)" :card="card" @updated="load" /></td><td>{{ card.exp_year }}-{{ String(card.exp_month).padStart(2, '0') }}</td><td><PaymentCardStatus :card="card" /></td><td><div class="row-actions"><button class="btn ghost compact" :disabled="busy || isClosedCard(card)" @click="toggle(card)">{{ isClosedCard(card) ? '已销卡' : card.enabled ? '禁用' : '启用' }}</button><button class="btn ghost compact" :disabled="busy" @click="edit(card)">编辑</button><button class="btn ghost compact danger-text" :disabled="busy" @click="remove(card)">删除</button></div></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
      </section>
      <form v-if="editor" class="panel card-editor" @submit.prevent="saveCard">
        <h2>{{ editor.id ? '编辑银行卡' : '添加银行卡' }}</h2>
        <label class="field"><span>粘贴银行卡信息</span><input v-model="rawCard" type="password" autocomplete="off" placeholder="银行卡号----202812----CVV" /><small>年月支持 202812、2028-12、12/28；四位连续数字按 YYMM 解析。</small></label>
        <button class="btn ghost compact" type="button" :disabled="!rawCard.trim() || busy" @click="parseCard">解析填入</button>
        <div class="settings-fields">
          <label class="field"><span>类型</span><select v-model="editor.card_type" :disabled="editor.source==='ejh'||editor.source==='nexovcc'" required><option value="other">其他</option><option value="ejh">EJH</option><option v-if="editor.source==='nexovcc'" value="nexovcc">环球</option></select><small>同一卡号共用类型；从卡台导入的卡保持来源不变。环球卡请从环球卡台同步导入。</small></label><label class="field"><span>名称</span><input v-model="editor.name" maxlength="60" placeholder="可选，默认使用银行卡尾号" /></label>
          <label class="field"><span>卡号</span><input v-model="editor.number" type="password" autocomplete="off" :required="!editor.id" :placeholder="editor.id ? `尾号 ${editor.last4}，留空不修改` : '银行卡号'" /></label>
          <label class="field"><span>到期年份</span><input v-model.number="editor.exp_year" required type="number" min="2000" max="9999" /></label>
          <label class="field"><span>到期月份</span><input v-model.number="editor.exp_month" required type="number" min="1" max="12" /></label>
          <label class="field"><span>CVV</span><input v-model="editor.cvv" type="password" autocomplete="new-password" :required="!editor.id" :placeholder="editor.id ? '已保存，留空不修改' : '3～4 位数字'" /></label>
          <label class="field"><span>最大开通数</span><input v-model.number="editor.max_accounts" type="number" min="0" max="10000" step="1" required /><small>0 表示沿用全局上限。已开通和在途共同计数；调低后停止新增，已有在途继续。同一卡号共用此设置。</small></label>
          <label class="field"><span>启用</span><input v-model="editor.enabled" type="checkbox" :disabled="editor.closed" /><small v-if="editor.closed">卡台已销卡，不能重新启用。</small></label>
        </div>
        <div class="panel-actions"><button class="btn ghost" type="button" :disabled="busy" @click="editor = null; rawCard = ''">取消</button><button class="btn primary" :disabled="busy">保存银行卡</button></div>
      </form>
    </template>

    <template v-else>
      <label class="field"><span>查询供应商</span><select v-model="queryProvider" :disabled="busy" @change="account = null; queryRows = []"><option value="tokenseek">GPTPay · Tokenseek</option><option value="cmsnav">GPTPay · CMSNav</option><option value="xupay">序付 · XUPAY</option></select></label>
      <section class="panel">
        <div class="panel-title"><div><span>GPTPAY</span><h2>供应商账户</h2></div><button class="btn ghost" :disabled="busy" @click="queryAccount">查询账户信息</button></div>
        <template v-if="account">
          <div class="pay-stats"><div><small>账户</small><strong>{{ account.user?.email || '供应商未返回' }}</strong></div><div><small>可用服务费余额</small><strong>{{ supplierCredits(account.wallet?.availableCredits, queryProvider) }}</strong></div><div><small>冻结服务费</small><strong>{{ supplierCredits(account.wallet?.reservedCredits, queryProvider) }}</strong></div><div><small>等级 / Key 到期</small><strong>{{ account.level?.name || '—' }} / {{ account.apiKey?.expiresAt ? formatTime(account.apiKey.expiresAt) : queryProvider !== 'tokenseek' ? '未提供' : '永久有效' }}</strong></div></div>
          <div class="table-shell"><table><thead><tr><th>可用套餐</th><th>成功服务费</th><th>失败服务费</th></tr></thead><tbody><tr v-for="plan in account.plans" :key="plan.planCode"><td>{{ proPlanName(plan.planCode) }}</td><td>{{ supplierCredits(plan.successCredits, queryProvider) }}</td><td>{{ supplierCredits(plan.failureCredits, queryProvider) }}</td></tr></tbody></table></div>
        </template><p v-else class="muted">点击查询余额、等级、套餐价格和 Key 到期时间。</p>
      </section>
      <section class="panel">
        <div class="panel-title"><div><h2>开通订单</h2><p>显示本地保存的最新状态；点击每行“查询状态”向供应商刷新。</p></div><button class="btn ghost" :disabled="busy" @click="action(load)">刷新列表</button></div>
        <form class="filter-row" @submit.prevent="page = 1; action(load)"><input v-model="emailFilter" placeholder="按完整账号邮箱筛选" /><button class="btn ghost" :disabled="busy">查询</button></form>
        <div class="table-shell"><table class="orders-table"><thead><tr><th>账号 / 订单 ID</th><th>套餐 / 银行卡</th><th>订单状态</th><th>结算 / 取消续费</th><th>扣除 / 释放服务费</th><th>更新时间</th><th>操作</th></tr></thead><tbody>
          <tr v-if="!rows.length"><td colspan="7" class="empty-cell">暂无开通订单</td></tr>
          <tr v-for="order in rows" :key="order.id"><td>{{ order.email }}<small class="order-id">{{ order.remote?.orderNo || order.id }}</small><small class="order-id">{{ order.remote?.id || '等待供应商订单 ID' }}</small></td><td>{{ proPlanName(order.plan_code) }}<small class="order-id">{{ payProviderName(order.provider) }} · {{ paymentCountryLabel(order.country || order.remote?.country) }}</small><small v-if="order.plan_code === 'codex_point'" class="order-id">购买点数：{{ order.credit_quantity || order.remote?.creditQuantity }}</small><small class="order-id">{{ order.card_name }} · {{ order.card_last4 }}</small><small v-if="['cmsnav', 'xupay'].includes(order.provider)" class="order-id">{{ supplierAmountTitle(order.remote, order.provider) }}：{{ supplierPaymentLabel(order.remote) }}</small></td><td>{{ payStatusName(order.status) }}<small v-if="order.provider === 'xupay'" class="order-id">{{ xupayStates(order.remote) }}</small><small v-if="order.remote?.retryHint" class="order-id">{{ order.remote.retryHint }}</small><small v-if="order.error" class="danger-text order-error">{{ order.error }}</small><small v-if="order.status_check_error" class="danger-text order-error">同步失败{{ order.status === 'manual_review' ? '，请人工查询' : '，后台重查' }}：{{ order.status_check_error }}</small><XUPayOrderDetails :order="order" /></td><td>{{ payStatusName(order.remote?.settlementStatus) }}<small class="order-id">取消续费：{{ payStatusName(order.remote?.cancellationStatus) }}</small></td><td>{{ supplierCredits(order.remote?.chargedCredits, order.provider) }} / {{ supplierCredits(order.remote?.releasedCredits, order.provider) }}</td><td>{{ formatTime(order.updated_at) }}</td><td><button v-if="canCheckXUPay(order)" class="btn ghost compact" :disabled="busy" @click="checkOrder(order)">核实原单</button><button v-if="order.remote?.id" class="btn ghost compact" :disabled="busy" @click="refreshOrder(order)">查询状态</button><button v-else-if="!order.rate_limited && !['success', 'failed', 'manual_review'].includes(order.status)" class="btn ghost compact" :disabled="busy" @click="refreshOrder(order, true)">重试原订单</button></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
      </section>
      <form class="panel" @submit.prevent="queryOrders"><h2>按供应商订单 ID 查询</h2><label class="field"><span>订单 ID（每行一个，最多 50 个）</span><textarea v-model="queryIDs" rows="3" placeholder="填写供应商返回的 id，不是订单号 orderNo" required /></label><div class="panel-actions"><button class="btn primary" :disabled="busy">查询订单状态</button></div><div v-if="queryRows.length" class="table-shell"><table><thead><tr><th>订单 ID</th><th>套餐 / 国家</th><th>状态</th><th>结算 / 取消续费</th><th>原因</th></tr></thead><tbody><tr v-for="order in queryRows" :key="order.id"><td>{{ order.id }}</td><td>{{ proPlanName(order.planCode) }} · {{ paymentCountryLabel(order.country) }}<small v-if="order.planCode === 'codex_point'" class="order-id">购买点数：{{ order.creditQuantity || '未返回' }}</small></td><td>{{ payStatusName(order.status) }}<small v-if="queryProvider !== 'tokenseek'" class="order-id">{{ supplierAmountTitle(order, queryProvider) }}：{{ supplierPaymentLabel(order) }}</small></td><td>{{ payStatusName(order.settlementStatus) }} / {{ payStatusName(order.cancellationStatus) }}</td><td>{{ order.failureReason || order.retryHint || '—' }}<XUPayOrderDetails :order="{ provider: queryProvider, remote: order }" /></td></tr></tbody></table></div></form>
    </template>
    <details v-if="mode === 'settings'" class="config-runtime"><summary><CalendarClock :size="17"/><span>查看定时运行概况</span><ChevronRight :size="15"/></summary><ProSchedulePanel compact/></details>
  </div>
</template>

<style scoped>
.provider-heading{display:flex;align-items:center;justify-content:space-between;gap:16px;margin-bottom:16px;flex-wrap:wrap}.provider-heading h4{font-size:15px;margin:0}.provider-heading p,.provider-note{font-size:12px;line-height:1.7;color:var(--muted);margin-top:7px}.provider-table-shell{max-width:100%;border:1px solid var(--line);border-radius:10px}.provider-table{min-width:850px}.provider-table th{font-size:11px;padding:12px;white-space:nowrap}.provider-note{margin-top:12px}
.oai-markers{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;margin-top:12px}.oai-confirmation-settings textarea{min-height:150px}@media(max-width:650px){.oai-markers{grid-template-columns:1fr}}
.purchase-window{padding:18px 0;border-bottom:1px solid var(--line);margin-bottom:16px}.purchase-window .settings-fields{margin-bottom:12px}

.cmsnav-terms{display:flex;align-items:flex-start;gap:8px;margin:16px 0;font-size:13px;line-height:1.6}.cmsnav-terms input{width:16px;height:16px;margin-top:3px;flex-shrink:0}.cmsnav-terms a{color:var(--green-strong);text-decoration:underline}
.gptpay-panel .field { margin-bottom: 14px; }
.gptpay-panel .settings-fields { margin-top: 16px; }
.catalog-status { display: flex; align-items: center; flex-wrap: wrap; gap: 12px; color: var(--muted); }
.country-picker { display: flex; flex-direction: column; gap: 8px; }
.gptpay-panel table { width: 100%; }
.config-section-title { margin-top: 24px; padding-top: 20px; border-top: 1px solid var(--line); }
.target-summary { display: flex; flex-direction: column; gap: 4px; padding: 12px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); }
.target-summary span { color: var(--muted); font-size: 11px; }
.orders-table { min-width: 1000px; }
.order-id { display: block; color: var(--muted); font-size: 11px; overflow-wrap: anywhere; margin-top: 5px; }
.order-error { display: block; min-width: 180px; max-width: 340px; white-space: normal; overflow-wrap: anywhere; }
.pay-stats { display: grid; grid-template-columns: repeat(4,minmax(0,1fr)); gap: 14px; margin-bottom: 20px; }
.pay-stats div { padding: 15px; background: var(--surface-2); border: 1px solid var(--line); border-radius: 6px; }
.pay-stats small,.pay-stats strong { display: block; overflow-wrap: anywhere; }
.pay-stats small { margin-bottom: 8px; color: var(--muted); }
.filter-row { display: flex; gap: 10px; margin-bottom: 15px; }
.filter-row input { max-width: 360px; }
@media(max-width:800px) { .pay-stats { grid-template-columns:repeat(2,minmax(0,1fr)); } }

.automation-config{min-width:0;border:1px solid var(--line);border-radius:18px;background:var(--surface);overflow:clip}
.config-body .field{align-content:start}
.config-heading{padding:26px 28px;display:flex;justify-content:space-between;align-items:center;gap:18px;border-bottom:1px solid var(--line)}
.config-heading h2{font-size:23px;margin:6px 0}.config-heading p,.config-section-heading p{color:var(--muted);font-size:13px;line-height:1.7}
.config-badge{display:inline-flex;align-items:center;gap:8px;padding:8px 12px;border:1px solid var(--line);border-radius:30px;white-space:nowrap;font-size:12px;color:var(--muted)}.config-badge>span{width:7px;height:7px;background:currentColor;border-radius:50%}.config-badge.enabled{color:var(--green-strong);background:var(--green-bg)}
.config-workspace{display:grid;grid-template-columns:224px minmax(0,1fr);min-height:540px}.config-nav{padding:20px 14px;border-right:1px solid var(--line);background:var(--bg-elevated)}
.config-nav>button{display:flex;align-items:center;gap:12px;width:100%;padding:14px 12px;margin-bottom:6px;border:1px solid transparent;border-radius:10px;text-align:left;color:var(--muted);transition:background .15s}
.config-nav>button:hover{background:var(--surface-2)}.config-nav>button.selected{background:var(--green-bg);color:var(--green-strong);border-color:color-mix(in srgb,var(--green) 20%,transparent)}.config-nav>button>span{flex:1}.config-nav strong{display:block;font-size:13px;color:var(--text)}.config-nav small{display:block;font-size:11px;margin-top:5px;color:var(--muted)}.config-nav-note{display:flex;gap:10px;padding:20px 12px;margin-top:20px;border-top:1px solid var(--line);color:var(--muted)}.config-nav-note svg{flex-shrink:0}.config-nav-note p{font-size:11px;line-height:1.8}
.config-body{padding:28px 32px;min-width:0}.config-section-heading{margin-bottom:24px}.config-section-heading>span{color:var(--muted);font-size:11px}.config-section-heading h3{font-size:20px;margin:8px 0 5px}.config-body .settings-fields{max-width:none;gap:18px 22px}.config-body .field{margin:0}.config-body .field>span,.config-body .country-picker>label{font-size:12px}.config-body .field small{font-size:11px;line-height:1.7}.config-body .toggle-row{padding:16px;border:1px solid var(--line);border-radius:12px;background:var(--bg-elevated)}.config-body .toggle-row small{font-size:11px;line-height:1.7}.config-body .toggle-row input:focus-visible+i{outline:2px solid var(--green);outline-offset:4px}
.config-divider{padding-top:24px;margin-top:26px;border-top:1px solid var(--line)}.config-divider h4{font-size:14px;font-weight:650}.config-divider p{font-size:12px;color:var(--muted);margin:6px 0 18px;line-height:1.7}.config-callout{padding:14px 16px;margin:20px 0;color:var(--text-2);background:var(--blue-bg);border-radius:10px;font-size:12px;line-height:1.8}
.config-savebar{position:sticky;bottom:0;display:flex;justify-content:space-between;align-items:center;gap:18px;padding:16px 24px;border-top:1px solid var(--line);background:var(--surface);z-index:2}.config-savebar>span{font-size:12px;color:var(--muted)}.config-savebar .btn{flex-shrink:0}
.config-runtime{border:1px solid var(--line);border-radius:12px;background:var(--surface)}.config-runtime>summary{display:flex;align-items:center;gap:10px;padding:18px 22px;cursor:pointer;font-size:13px}.config-runtime>summary>span{flex:1}.config-runtime[open]>summary>svg:last-child{transform:rotate(90deg)}.config-runtime :deep(.pro-schedule-panel){box-shadow:none;border-top:1px solid var(--line);border-radius:0 0 12px 12px}
@media(max-width:1000px){.config-workspace{grid-template-columns:190px minmax(0,1fr)}.config-body{padding:24px}.config-body .settings-fields{grid-template-columns:minmax(0,1fr)}}
@media(max-width:720px){.config-workspace{display:block}.config-heading{padding:20px;align-items:flex-start;flex-direction:column}.config-nav{border-right:0;border-bottom:1px solid var(--line);display:flex;gap:6px;overflow-x:auto;padding:10px}.config-nav>button{width:auto;min-width:130px;margin:0;flex-shrink:0;padding:10px}.config-nav small,.config-nav>button>svg:last-child,.config-nav-note{display:none}.config-body{padding:20px}.config-savebar{padding:14px;align-items:flex-end}.config-savebar>span{font-size:11px}.config-body .toggle-row{gap:12px}}
</style>
<style scoped>
.card-filter{display:flex;gap:4px;align-items:center;padding:0 20px 16px}.card-filter button{padding:8px 18px;border:1px solid var(--line);border-radius:7px;font-size:12px;color:var(--muted);background:var(--surface)}.card-filter button.active{background:var(--green-bg);color:var(--green-strong);border-color:var(--green-strong)}.card-filter button:disabled{opacity:.6}.card-filter>span{font-size:11px;color:var(--muted);margin-left:10px}
</style>

<style scoped>
.oai-confirmation-settings{padding:16px;border:1px solid var(--line);border-radius:8px}.oai-confirmation-settings h4{margin:0 0 8px}.oai-rule{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:12px;padding:14px 0;border-top:1px solid var(--line)}.oai-rule-toggle{grid-column:1/-1}.oai-rule-toggle,.oai-delay{display:flex;align-items:center;gap:8px;font-size:12px}.oai-delay input{width:80px;min-width:0;padding:7px 8px;border:1px solid var(--line);border-radius:6px;background:var(--surface)}.oai-rule-toggle input{width:16px;height:16px}@media(max-width:650px){.oai-rule{grid-template-columns:1fr;gap:8px}}
</style>
