<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import { formatTime } from '../utils'
import { parseCardText, payStatusName, proPlanName } from '../gptpay'
import MessageBar from './MessageBar.vue'
import Pagination from './Pagination.vue'
import ProSchedulePanel from './ProSchedulePanel.vue'

const props = defineProps({ mode: String, defaultPageSize: { type: Number, default: 10 }, adminAccounts: { type: Array, default: () => [] } })
const emit = defineEmits(['saved'])
const settings = reactive({ url: 'https://gptpay.tokenseek.app/api/v1', plan_code: 'pro5', key_present: false, api_key: '', session_timeout_minutes: 30,card_account_limit:3 })
const automation = reactive({ scheduled_enabled:false,max_unmerged:5,schedule_interval_seconds:120,quota_enabled: false, quota_used_threshold: 100, quota_check_interval_seconds: 120, auto_merge_enabled: false, target_admin_id: '', target_seat_type: 'default', concurrency: 2, retry_count: 2, retry_interval_seconds: 3 })
const selectedAdmin = computed(() => props.adminAccounts.find(a => a.id === automation.target_admin_id))
const settingsReady = ref(false)
const busy = ref(false), message = reactive({ text: '', type: '' }), account = ref(null)
const rows = ref([]), page = ref(1), pageSize = ref(props.defaultPageSize), total = ref(0)
const queryIDs = ref(''), queryRows = ref([]), emailFilter = ref('')
const editor = ref(null), rawCard = ref('')
let disposed = false, requestID = 0
const notify = (text = '', type = '') => Object.assign(message, { text, type })
async function action(fn) { if (busy.value) return; busy.value = true; notify(); try { await fn() } catch (e) { notify(e.message, 'error') } finally { busy.value = false } }
async function load() {
  const id = ++requestID
  const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize.value) })
  if (props.mode === 'orders' && emailFilter.value.trim()) params.set('email', emailFilter.value.trim())
  const result = await api(`/api/gptpay/${props.mode === 'cards' ? 'cards' : 'orders'}?${params}`)
  if (disposed || id !== requestID) return
  rows.value = result.items || []; total.value = result.total || 0
  if (page.value > Math.max(1, Math.ceil(total.value / pageSize.value))) { page.value = Math.max(1, Math.ceil(total.value / pageSize.value)); await load() }
}
function changePage(v) { page.value = v; action(load) }
function changeSize(v) { pageSize.value = v; page.value = 1; action(load) }
async function saveSettings() {
  if (!settingsReady.value) return
  await action(async () => {
    const saved = await api('/api/pro-settings/automation', { method: 'PUT', body: { automation, gptpay: settings } })
    Object.assign(settings, saved.gptpay, { api_key: '' }); Object.assign(automation, saved.automation)
    notify('Pro 全自动配置已保存', 'success'); emit('saved')
  })
}
async function queryAccount() { await action(async () => { account.value = await api('/api/gptpay/account'); notify('供应商账户信息已更新', 'success') }) }
function edit(card = null) {
  editor.value = { id: card?.id || '', name: card?.name || '', enabled: card?.enabled ?? true, number: '', cvv: '', exp_month: card?.exp_month || 0, exp_year: card?.exp_year || 0, last4: card?.last4 || '' }
  rawCard.value = ''; notify()
}
function parseCard() { try { Object.assign(editor.value, parseCardText(rawCard.value)); rawCard.value = ''; notify('已解析，请核对有效期后保存', 'success') } catch (e) { notify(e.message, 'error') } }
async function saveCard() {
  await action(async () => {
    const card = editor.value
    if (rawCard.value.trim()) Object.assign(card, parseCardText(rawCard.value))
    await api('/api/gptpay/cards' + (card.id ? '/' + encodeURIComponent(card.id) : ''), { method: card.id ? 'PUT' : 'POST', body: { name: card.name, enabled: card.enabled, number: card.number, cvv: card.cvv, exp_month: card.exp_month, exp_year: card.exp_year } })
    editor.value = null; rawCard.value = ''; await load(); notify('银行卡已保存', 'success')
  })
}
async function toggle(card) { await action(async () => { await api('/api/gptpay/cards/' + encodeURIComponent(card.id), { method: 'PUT', body: { enabled: !card.enabled } }); await load() }) }
async function remove(card) {
  if (!window.confirm(`删除银行卡“${card.name} · 尾号 ${card.last4}”？已提交的订单记录会保留。`)) return
  await action(async () => { await api('/api/gptpay/cards/' + encodeURIComponent(card.id), { method: 'DELETE' }); await load(); notify('银行卡已删除', 'success') })
}
async function refreshOrder(order, retry = false) {
  await action(async () => { const result = await api(`/api/gptpay/orders/${encodeURIComponent(order.id)}/${retry ? 'retry' : 'refresh'}`, { method: 'POST', body: {} }); await load(); notify(`${result.email}：${payStatusName(result.status)}${result.error ? ' · ' + result.error : ''}`, result.status === 'failed' ? 'error' : 'success') })
}
async function queryOrders() {
  await action(async () => {
    const ids = [...new Set(queryIDs.value.split(/[\s,，]+/).filter(Boolean))]
    if (!ids.length || ids.length > 50) throw new Error('请输入 1～50 个供应商订单 ID')
    const result = await api('/api/gptpay/orders/query', { method: 'POST', body: { order_ids: ids } }); queryRows.value = result.orders || []; notify('供应商订单状态已更新', 'success')
  })
}
onMounted(() => action(async () => {
  if (props.mode === 'settings') {
    const [pay, pro] = await Promise.all([api('/api/gptpay/settings'), api('/api/pro-settings')])
    if (disposed) return
    Object.assign(settings, pay); Object.assign(automation, pro); settingsReady.value = true
  } else await load()
}))
onBeforeUnmount(() => { disposed = true; editor.value = null; rawCard.value = ''; settings.api_key = '' })
</script>

<template>
  <div class="gptpay-panel view-stack">
    <MessageBar :message="message" />
    <ProSchedulePanel v-if="mode==='settings'" compact/>
    <form v-if="mode === 'settings'" class="panel" @submit.prevent="saveSettings">
      <div class="panel-title"><div><span>PRO AUTOMATION</span><h2>Pro 全自动配置</h2><p>登录 → 开通 → 新 RT/AT → 推送 Sub2 → 额度达标 → 空间四步流程。</p></div></div>
      <div class="settings-fields">
        <label class="field"><span>开通套餐</span><select v-model="settings.plan_code" aria-label="开通套餐"><option value="pro5">Pro 5x</option><option value="pro20">Pro 20x</option></select></label>
        <label class="field"><span>每张银行卡最多开通账号数</span><input v-model.number="settings.card_account_limit" type="number" min="1" max="10000" required/><small>成功开通与在途占位共用上限，默认 3 个。同一张卡重复添加也共用计数。</small></label>
        <label class="field"><span>登录及开通会话最长保留（分钟）</span><input v-model.number="settings.session_timeout_minutes" type="number" min="5" max="60" required /><small>出口 IP 变化或检测失败仅记录日志，继续执行；任务超时仍会停止，已提交订单可查询。</small></label>
        <label class="field wide"><span>GPTPay API 地址</span><input v-model="settings.url" required placeholder="https://gptpay.tokenseek.app/api/v1" /></label>
        <label class="field wide"><span>GPTPay API Key</span><input v-model="settings.api_key" type="password" autocomplete="new-password" :placeholder="settings.key_present ? '已保存，留空不修改' : '填写供应商 API Key'" /></label>
      </div>
      <h3 class="config-section-title">定时全自动开通</h3>
      <div class="settings-fields">
        <label class="toggle-row wide"><div><strong>定时执行全自动 Pro</strong><small>从 Pro 管理选择未开通过的新账号，自动选择有名额的银行卡，登录 → 开通 → RT/AT → Sub2 → 刷新额度。</small></div><input v-model="automation.scheduled_enabled" type="checkbox"/><i></i></label>
        <label class="field"><span>开通并在 Sub 最大数（未合并）</span><input v-model.number="automation.max_unmerged" type="number" min="1" max="10000" required/><small>已开通未合并及开通在途共同占位，避免超出上限。</small></label>
        <label class="field"><span>定时开通检查间隔（秒）</span><input v-model.number="automation.schedule_interval_seconds" type="number" min="10" max="86400" required/><small>本批推送成功并完成首次额度刷新后，才允许下一批启动。</small></label>
      </div>
      <h3 class="config-section-title">额度检测与空间合并</h3>
      <p class="muted">以下两个开关管理手动处理的 Pro 账号；全自动任务自身定时查额度，达标后继续合并，并保留启动时的额度配置。</p>
      <div class="settings-fields">
        <label class="field"><span>7 天已用额度达到（%）</span><input v-model.number="automation.quota_used_threshold" type="number" min="0.01" max="100" step="0.01" required /></label>
        <label class="field"><span>额度检测间隔（秒）</span><input v-model.number="automation.quota_check_interval_seconds" type="number" min="10" max="86400" required /></label>
        <label class="toggle-row wide"><div><strong>定时查额度</strong><small>定时查询已推送且未合并的手动账号，并保存最新额度到数据库。</small></div><input v-model="automation.quota_enabled" type="checkbox" /><i></i></label>
        <label class="toggle-row wide"><div><strong>查额度达标后自动合并</strong><small>查额度成功且达到阈值时执行合并；定时触发需同时开启“定时查额度”。401、超时或无 7 天额度数据不触发。</small></div><input v-model="automation.auto_merge_enabled" type="checkbox" /><i></i></label>
        <label class="field wide"><span>目标母号</span><select v-model="automation.target_admin_id"><option value="">请选择母号</option><option v-for="admin in adminAccounts" :key="admin.id" :value="admin.id">{{ admin.label || admin.email }} · {{ admin.team_account_id || '无 Team ID' }}</option></select></label>
        <label class="field"><span>空间席位套餐</span><select v-model="automation.target_seat_type"><option value="default">Standard（普通空间）</option><option value="prolite">Premium（5x）</option></select></label>
        <label class="field"><span>并发账号数</span><input v-model.number="automation.concurrency" type="number" min="1" max="20" required /></label>
        <label class="field"><span>失败重试次数</span><input v-model.number="automation.retry_count" type="number" min="0" max="10" required /></label>
        <label class="field"><span>重试间隔（秒）</span><input v-model.number="automation.retry_interval_seconds" type="number" min="1" required /></label>
        <div class="target-summary wide"><strong>{{ selectedAdmin?.label || selectedAdmin?.email || '尚未选择母号' }}</strong><span>{{ selectedAdmin?.team_account_id || '选择后显示目标 Team ID' }}</span></div>
      </div>
      <div class="panel-actions"><button class="btn primary" :disabled="busy || !settingsReady">保存 Pro 配置</button></div>
    </form>

    <template v-else-if="mode === 'cards'">
      <section class="panel">
        <div class="panel-title"><div><span>PAYMENT CARDS</span><h2>银行卡信息</h2><p>仅启用且未过期的卡可用于新开通订单。</p></div><button class="btn primary" :disabled="busy" @click="edit()">添加银行卡</button></div>
        <div class="table-shell"><table><thead><tr><th>名称</th><th>银行卡</th><th>已开通账号</th><th>有效期</th><th>状态</th><th>操作</th></tr></thead><tbody>
          <tr v-if="!rows.length"><td colspan="6" class="empty-cell">暂无银行卡</td></tr>
          <tr v-for="card in rows" :key="card.id"><td>{{ card.name }}</td><td>•••• {{ card.last4 }}</td><td>{{card.opened_accounts||0}} / {{card.account_limit||3}}<small class="order-id">在途 {{card.pending_accounts||0}} · 剩余 {{card.remaining_accounts??0}}</small></td><td>{{ card.exp_year }}-{{ String(card.exp_month).padStart(2, '0') }}</td><td>{{ card.enabled ? '已启用' : '已禁用' }}</td><td><div class="row-actions"><button class="btn ghost compact" :disabled="busy" @click="toggle(card)">{{ card.enabled ? '禁用' : '启用' }}</button><button class="btn ghost compact" :disabled="busy" @click="edit(card)">编辑</button><button class="btn ghost compact danger-text" :disabled="busy" @click="remove(card)">删除</button></div></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
      </section>
      <form v-if="editor" class="panel card-editor" @submit.prevent="saveCard">
        <h2>{{ editor.id ? '编辑银行卡' : '添加银行卡' }}</h2>
        <label class="field"><span>粘贴银行卡信息</span><input v-model="rawCard" type="password" autocomplete="off" placeholder="银行卡号----202812----CVV" /><small>年月支持 202812、2028-12、12/28；四位连续数字按 YYMM 解析。</small></label>
        <button class="btn ghost compact" type="button" :disabled="!rawCard.trim() || busy" @click="parseCard">解析填入</button>
        <div class="settings-fields">
          <label class="field"><span>名称</span><input v-model="editor.name" maxlength="60" placeholder="可选，默认使用银行卡尾号" /></label>
          <label class="field"><span>卡号</span><input v-model="editor.number" type="password" autocomplete="off" :required="!editor.id" :placeholder="editor.id ? `尾号 ${editor.last4}，留空不修改` : '银行卡号'" /></label>
          <label class="field"><span>到期年份</span><input v-model.number="editor.exp_year" required type="number" min="2000" max="9999" /></label>
          <label class="field"><span>到期月份</span><input v-model.number="editor.exp_month" required type="number" min="1" max="12" /></label>
          <label class="field"><span>CVV</span><input v-model="editor.cvv" type="password" autocomplete="new-password" :required="!editor.id" :placeholder="editor.id ? '已保存，留空不修改' : '3～4 位数字'" /></label>
          <label class="field"><span>启用</span><input v-model="editor.enabled" type="checkbox" /></label>
        </div>
        <div class="panel-actions"><button class="btn ghost" type="button" :disabled="busy" @click="editor = null; rawCard = ''">取消</button><button class="btn primary" :disabled="busy">保存银行卡</button></div>
      </form>
    </template>

    <template v-else>
      <section class="panel">
        <div class="panel-title"><div><span>GPTPAY</span><h2>供应商账户</h2></div><button class="btn ghost" :disabled="busy" @click="queryAccount">查询账户信息</button></div>
        <template v-if="account">
          <div class="pay-stats"><div><small>账户</small><strong>{{ account.user?.email }}</strong></div><div><small>可用 Credits</small><strong>{{ account.wallet?.availableCredits }}</strong></div><div><small>冻结 Credits</small><strong>{{ account.wallet?.reservedCredits }}</strong></div><div><small>等级 / Key 到期</small><strong>{{ account.level?.name }} / {{ account.apiKey?.expiresAt ? formatTime(account.apiKey.expiresAt) : '永久有效' }}</strong></div></div>
          <div class="table-shell"><table><thead><tr><th>可用套餐</th><th>成功费用（Credits）</th><th>失败费用（Credits）</th></tr></thead><tbody><tr v-for="plan in account.plans" :key="plan.planCode"><td>{{ proPlanName(plan.planCode) }}</td><td>{{ plan.successCredits }}</td><td>{{ plan.failureCredits }}</td></tr></tbody></table></div>
        </template><p v-else class="muted">点击查询余额、等级、套餐价格和 Key 到期时间。</p>
      </section>
      <section class="panel">
        <div class="panel-title"><div><h2>开通订单</h2><p>显示本地保存的最新状态；点击每行“查询状态”向供应商刷新。</p></div><button class="btn ghost" :disabled="busy" @click="action(load)">刷新列表</button></div>
        <form class="filter-row" @submit.prevent="page = 1; action(load)"><input v-model="emailFilter" placeholder="按完整账号邮箱筛选" /><button class="btn ghost" :disabled="busy">查询</button></form>
        <div class="table-shell"><table class="orders-table"><thead><tr><th>账号 / 订单 ID</th><th>套餐 / 银行卡</th><th>订单状态</th><th>结算 / 取消续费</th><th>扣除 / 释放 Credits</th><th>更新时间</th><th>操作</th></tr></thead><tbody>
          <tr v-if="!rows.length"><td colspan="7" class="empty-cell">暂无开通订单</td></tr>
          <tr v-for="order in rows" :key="order.id"><td>{{ order.email }}<small class="order-id">{{ order.remote?.orderNo || order.id }}</small><small class="order-id">{{ order.remote?.id || '等待供应商订单 ID' }}</small></td><td>{{ proPlanName(order.plan_code) }}<small class="order-id">{{ order.card_name }} · {{ order.card_last4 }}</small></td><td>{{ payStatusName(order.status) }}<small v-if="order.error" class="danger-text order-error">{{ order.error }}</small><small v-if="order.status_check_error" class="danger-text order-error">同步失败，后台重查：{{ order.status_check_error }}</small></td><td>{{ payStatusName(order.remote?.settlementStatus) }}<small class="order-id">取消续费：{{ payStatusName(order.remote?.cancellationStatus) }}</small></td><td>{{ order.remote?.chargedCredits || 0 }} / {{ order.remote?.releasedCredits || 0 }}</td><td>{{ formatTime(order.updated_at) }}</td><td><button v-if="order.remote?.id" class="btn ghost compact" :disabled="busy" @click="refreshOrder(order)">查询状态</button><button v-else-if="!['success', 'failed'].includes(order.status)" class="btn ghost compact" :disabled="busy" @click="refreshOrder(order, true)">重试原订单</button></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="changePage" @update:page-size="changeSize" />
      </section>
      <form class="panel" @submit.prevent="queryOrders"><h2>按供应商订单 ID 查询</h2><label class="field"><span>订单 ID（每行一个，最多 50 个）</span><textarea v-model="queryIDs" rows="3" placeholder="填写供应商返回的 id，不是订单号 orderNo" required /></label><div class="panel-actions"><button class="btn primary" :disabled="busy">查询订单状态</button></div><div v-if="queryRows.length" class="table-shell"><table><thead><tr><th>订单 ID</th><th>状态</th><th>结算 / 取消续费</th><th>原因</th></tr></thead><tbody><tr v-for="order in queryRows" :key="order.id"><td>{{ order.id }}</td><td>{{ payStatusName(order.status) }}</td><td>{{ payStatusName(order.settlementStatus) }} / {{ payStatusName(order.cancellationStatus) }}</td><td>{{ order.failureReason || '—' }}</td></tr></tbody></table></div></form>
    </template>
  </div>
</template>

<style scoped>
.gptpay-panel .field { margin-bottom: 14px; }
.gptpay-panel .settings-fields { margin-top: 16px; }
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
</style>
