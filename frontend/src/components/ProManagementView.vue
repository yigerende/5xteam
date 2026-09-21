<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { BadgeCheck, CirclePlus, Clock3, FileKey2, Gauge, History, KeyRound, LoaderCircle, LogIn, RefreshCw, Rocket, RotateCcw, Search, Settings2, ShieldCheck, Trash2, UsersRound, X } from 'lucide-vue-next'
import { api } from '../api'
import { copyText } from '../clipboard'
import { formatTime } from '../utils'
import MessageBar from './MessageBar.vue'
import Pagination from './Pagination.vue'
import StatusPill from './StatusPill.vue'
import ProActionMenu from './ProActionMenu.vue'
import AccountCredentialsDialog from './AccountCredentialsDialog.vue'
import TemporaryATDialog from './TemporaryATDialog.vue'
import ProAccountLogDialog from './ProAccountLogDialog.vue'
import ProGPTPayPanel from './ProGPTPayPanel.vue'
import ProSchedulePanel from './ProSchedulePanel.vue'
import ProRechargeDialog from './ProRechargeDialog.vue'
import ProAutoDialog from './ProAutoDialog.vue'
import ProAutoStages from './ProAutoStages.vue'
import ProTransferDialog from './ProTransferDialog.vue'

const props = defineProps({ accounts: { type: Array, default: () => [] }, adminAccounts: { type: Array, default: () => [] }, defaultPageSize: { type: Number, default: 10 } })
const emit = defineEmits(['reload'])
const activeTab = ref('accounts')
const spaceFilter = ref('all')
const query = ref('')
const page = ref(1)
const pageSize = ref(props.defaultPageSize)
const rows = ref([])
const total = ref(0)
const listSummary = reactive({ all: 0, oauth_ready: 0, pushed: 0, merged: 0 })
const selectedEmails = ref(new Set())
const busy = ref('')
const credentialViewer = ref(null)
const temporaryAT = ref(null)
const accountLogs = ref(null)
const recharge = ref(null)
const autoPro = ref(null)
const transferDialog = ref(null)
function transferBusy(running) { busy.value = running ? 'transfer-json' : ''; schedulePoll(0) }
async function resumeImported(account) {
  if (busy.value) return
  busy.value = 'resume-import:' + account.email
  try { await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/resume-import`, {method:'POST',body:{}}); await reload(); setMessage('已从保存的进度继续执行','success') }
  catch(e) { setMessage(e.message,'error') }
  finally {busy.value='';schedulePoll(0)}
}
const message = reactive({ text: '', type: '' })
const activeManualStages = reactive({})
function setManualStage(email, stage, running) {
  const key = email.toLowerCase() + ':' + stage
  if (running) activeManualStages[key] = true
  else delete activeManualStages[key]
  schedulePoll(0)
}
function phaseState(account) {
  const persisted = account.pro_stage_progress || account.pro_auto || {}
  const state = { ...persisted, steps: { ...persisted.steps }, errors: { ...persisted.errors } }
  for (const stage of ['login', 'recharge', 'oauth', 'push', 'quota', 'merge']) {
    if (activeManualStages[account.email.toLowerCase() + ':' + stage]) { state.steps[stage] = 'running'; delete state.errors[stage] }
  }
  return state
}
function manualTaskProgress({ email, mode, running }) { setManualStage(email, mode === 'oauth' ? 'oauth' : 'login', running) }
function rechargeProgress({ email, running }) { setManualStage(email, 'recharge', running) }
const oauth = reactive({ open: false, loading: false, sessionID: '', authURL: '', callbackURL: '', expiresAt: '', targetEmail: '' })
const sub2Groups = ref([])
const cpaGroups = ref([])
const countdown = ref(0)
let countdownTimer
let searchTimer
let pollTimer
let disposed = false
let listRequestID = 0
let pendingPageLoads = 0

const defaults = () => ({
  provider: 'sub2', quota_enabled: false, quota_used_threshold: 100, quota_check_interval_seconds: 120,
  auto_merge_enabled: false, target_admin_id: '', target_seat_type: 'default', retry_count: 2, retry_interval_seconds: 3, concurrency: 2,
  next_quota_sweep_at: null,
  sub2: { url: '', email: '', password_present: false, group_ids: [], group_names: [], models: [], account_concurrency: 10, priority: 1, cpa_ws: false },
  cpa: { url: '', key_present: false, websockets: false, group_ids: [], group_names: [] },
  sub2_password: '', cpa_key: '',
})
const settings = reactive(defaults())

const displayedAccounts = computed(() => rows.value)
const selectedAccounts = computed(() => rows.value.filter((account) => selectedEmails.value.has(account.email.toLowerCase())))
const allVisibleSelected = computed(() => displayedAccounts.value.length && displayedAccounts.value.every((account) => selectedEmails.value.has(account.email.toLowerCase())))
const oauthReady = computed(() => Number(listSummary.oauth_ready || 0))
const pushed = computed(() => Number(listSummary.pushed || 0))
const merged = computed(() => Number(listSummary.merged || 0))

async function loadPage() {
  const requestID = ++listRequestID
  pendingPageLoads++
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize.value), query: query.value.trim(), merge_state: spaceFilter.value === 'all' ? '' : spaceFilter.value })
    const data = await api(`/api/pro-accounts?${params}`)
    if (disposed || requestID !== listRequestID) return
    const previous = new Map(rows.value.map(account => [account.email, account]))
    rows.value = data.items || []; total.value = Number(data.total || 0); Object.assign(listSummary, data.summary || {})
    for (const account of rows.value) {
      if (previous.get(account.email)?.pro_workflow_running && !account.pro_workflow_running) {
        if (account.pro_last_error) setMessage(account.email + '：' + account.pro_last_error, 'error')
        else if (account.pro_remove_status === 'completed') setMessage(account.email + ' 空间四步流程完成', 'success')
      }
    }
    const lastPage = Math.max(1, Math.ceil(total.value / pageSize.value))
    if (page.value > lastPage) { page.value = lastPage; return loadPage() }
    const visible = new Set(rows.value.map((account) => account.email.toLowerCase()))
    selectedEmails.value = new Set([...selectedEmails.value].filter((email) => visible.has(email)))
  } finally { pendingPageLoads-- }
}
function schedulePoll(delay = 1500) {
  window.clearTimeout(pollTimer)
  if (disposed) return
  pollTimer = window.setTimeout(async () => {
    try {
      if (activeTab.value === 'accounts' && !document.hidden && !pendingPageLoads) await loadPage()
    } catch { /* Keep last known states and retry; do not mask a business error. */ }
    finally {
      schedulePoll(busy.value || rows.value.some(account => account.pro_workflow_running || Object.values(phaseState(account).steps || {}).includes('running')) ? 1500 : 5000)
    }
  }, delay)
}
watch(activeTab, tab => { if (tab === 'accounts') schedulePoll(0) })
function mergeStages(account) {
  const labels = { invite: '邀请空间', accept: '进入空间', transfer: '合并空间', remove: '移出空间' }
  const states = { not_started: '未开始', pending: '待处理', running: '执行中', completed: '成功', failed: '失败', unknown: '待确认', team_removed: '已移出' }
  return Object.entries(labels).map(([key, label]) => {
    const status = account['pro_' + key + '_status'] || 'pending'
    return { key, label, status, text: states[status] || '待处理' }
  })
}
function mergeActivity(account) {
  const step = mergeStages(account).find(item => item.status === 'running')
  if (step) return step.label + '中'
  if (busy.value === 'merge:' + account.email) return '正在启动或续跑'
  return account.pro_workflow_running ? '流程执行中' : ''
}
async function saveMergeStage(account, step, event) {
  const status = event.target.value
  event.target.value = step.status
  if (busy.value || account.pro_workflow_running) return
  const labels = { not_started: '未开始', pending: '待处理', completed: '成功', failed: '失败' }
  if (!window.confirm(`确认将 ${account.email} 的${step.label}修正为“${labels[status]}”？此操作仅修改本地状态，不会执行远程操作。请先确认实际结果，续跑时会跳过已成功步骤。`)) return
  busy.value = `stage:${account.email}`
  try {
    await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/stage`, { method: 'PUT', body: { stage: step.key, status, expected_status: account['pro_' + step.key + '_status'] || '' } })
    await reload()
    setMessage(`${step.label}已修正为${labels[status]}`, 'success')
  } catch (error) { await loadPage().catch(() => {}); setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
function setPage(value) { page.value = value; selectedEmails.value = new Set(); loadPage() }
function setPageSize(value) { pageSize.value = value; page.value = 1; selectedEmails.value = new Set(); loadPage() }
function setSpaceFilter(value) { spaceFilter.value = value; page.value = 1; selectedEmails.value = new Set(); loadPage() }
watch(query, () => { window.clearTimeout(searchTimer); page.value = 1; selectedEmails.value = new Set(); searchTimer = window.setTimeout(loadPage, 250) })

function setMessage(text = '', type = '') { message.text = text; message.type = type }
function toggle(account) { const next = new Set(selectedEmails.value); const email = account.email.toLowerCase(); next.has(email) ? next.delete(email) : next.add(email); selectedEmails.value = next }
function toggleVisible() { const next = new Set(selectedEmails.value); displayedAccounts.value.forEach((a) => allVisibleSelected.value ? next.delete(a.email.toLowerCase()) : next.add(a.email.toLowerCase())); selectedEmails.value = next }
function quotaLabel(window) { return window ? `${Number(window.used_percent || 0).toFixed(1)}%` : '-' }
function statusTone(value) { return value === 'completed' ? 'success' : value === 'failed' || value === 'reauthorize_required' ? 'danger' : value === 'running' ? 'running' : 'pending' }
function updateCountdown() { countdown.value = settings.next_quota_sweep_at ? Math.max(0, Math.ceil((Date.parse(settings.next_quota_sweep_at) - Date.now()) / 1000)) : 0 }
async function reload() { await loadPage(); emit('reload') }

async function loadSettings() {
  try {
    const data = await api('/api/pro-settings')
    Object.assign(settings, defaults(), data, { sub2: { ...defaults().sub2, ...(data.sub2 || {}) }, cpa: { ...defaults().cpa, ...(data.cpa || {}) }, sub2_password: '', cpa_key: '' })
    updateCountdown()
  } catch (error) { setMessage(error.message, 'error') }
}
async function beginOAuth(account = null) {
  busy.value = 'oauth-start'
  try {
    const data = await api('/api/pro-accounts/oauth/start', { method: 'POST', body: { email: account?.email || '' } })
    Object.assign(oauth, { open: true, loading: false, sessionID: data.session_id, authURL: data.auth_url, callbackURL: '', expiresAt: data.expires_at, targetEmail: account?.email || '' })
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function finishOAuth() {
  if (!oauth.callbackURL.trim()) return setMessage('请粘贴浏览器地址栏中的完整 localhost 回调 URL', 'error')
  oauth.loading = true
  if (oauth.targetEmail) setManualStage(oauth.targetEmail, 'oauth', true)
  try {
    const account = await api('/api/pro-accounts/oauth/complete', { method: 'POST', body: { session_id: oauth.sessionID, callback_url: oauth.callbackURL } })
    oauth.open = false; await reload(); setMessage(`${account.email} OAuth 授权完成，AT/RT 已加密保存`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { oauth.loading = false; if (oauth.targetEmail) setManualStage(oauth.targetEmail, 'oauth', false) }
}
async function copyOAuthURL() {
  try { await copyText(oauth.authURL); setMessage('授权链接已复制', 'success') }
  catch { setMessage('复制失败，请手动选择授权链接', 'error') }
}
function requestTemporaryAT(accounts, batch = false) {
  if (busy.value) return
  if (!accounts.length) return setMessage('请先选择账号', 'error')
  if (accounts.some(account => account.pro_workflow_running)) return setMessage('选中的账号正在执行空间合并，请完成后再获取临时 AT', 'error')
  temporaryAT.value?.start(accounts, batch)
}
function temporaryATBusy(running) { busy.value = running ? 'temporary-at' : ''; schedulePoll(0) }
function requestOAuthLogin(account) {
  if (busy.value) return
  if (account.pro_workflow_running) return setMessage('该账号正在执行空间合并，请完成后再登录获取 RT / AT', 'error')
  temporaryAT.value?.start([account], false, 'oauth')
}
async function temporaryATFinished(result) {
  await reload().catch(error => setMessage(error.message, 'error'))
  const label = result.mode === 'oauth' ? 'OAuth RT / AT 获取' : '临时 AT 获取'
  setMessage(`${label}完成：成功 ${result.ok}，失败 ${result.fail}`, result.fail ? 'error' : 'success')
}
async function runOne(account, action) {
  if (busy.value || account.pro_workflow_running) return
  busy.value = `${action}:${account.email}`
  const stage = action === 'refresh' ? 'oauth' : action
  setManualStage(account.email, stage, true)
  setMessage()
  schedulePoll(0)
  const paths = { refresh: 'oauth/refresh', push: 'push', quota: 'quota', merge: 'merge' }
  let accepted = false
  try {
    const result = await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/${paths[action]}`, { method: 'POST' })
    accepted = action === 'merge'
    if (accepted) {
      rows.value = rows.value.map(item => item.email === account.email ? { ...item, ...result } : item)
      setMessage(account.email + ' 已提交后台执行，刷新或关闭页面不影响流程', 'success')
    }
    await reload()
    if (!accepted) setMessage(`${account.email} 操作完成`, 'success')
  }
  catch (error) {
    await loadPage().catch(() => {})
    const latest = rows.value.find(item => item.email === account.email)
    if (action === 'merge' && latest?.pro_workflow_running) setMessage(account.email + ' 正在后台执行，请等待状态更新', 'success')
    else if (action === 'merge' && latest?.pro_remove_status === 'completed') setMessage(account.email + ' 空间四步流程完成', 'success')
    else setMessage(accepted ? '任务已提交后台，暂时无法刷新列表：' + error.message : error.message, 'error')
  }
  finally { busy.value = ''; setManualStage(account.email, stage, false) }
}
async function runBatch(action) {
  if (!selectedAccounts.value.length) return setMessage('请先选择账号', 'error')
  busy.value = `batch-${action}`
  const emails = selectedAccounts.value.map(account => account.email)
  emails.forEach(email => setManualStage(email, action, true))
  try {
    const result = await api(`/api/pro-accounts/${action}`, { method: 'POST', body: { emails: selectedAccounts.value.map((a) => a.email) } })
    await reload(); setMessage(`批量${action === 'push' ? '推送' : '额度检测'}完成：成功 ${result.succeeded}，失败 ${result.failed}`, result.failed ? 'error' : 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = ''; emails.forEach(email => setManualStage(email, action, false)) }
}
async function deleteSelectedAccounts() {
  if (busy.value) return
  const emails = selectedAccounts.value.map(account => account.email)
  if (!emails.length) return setMessage('请先勾选要删除的 Pro 账号', 'error')
  if (!window.confirm(`确认删除已勾选的 ${emails.length} 个 Pro 账号？将删除本地账号、凭证及关联的 Team 轮转记录，不会移回邮件管理。不会操作 OpenAI、Sub2 或 CPA；开通订单和银行卡使用记录保留。此操作不可撤销。`)) return
  busy.value = 'delete-batch'
  try {
    const result = await api('/api/pro-accounts/delete', { method: 'POST', body: { emails } })
    const failures = result.items.filter(item => !item.deleted)
    selectedEmails.value = new Set(failures.map(item => item.email.toLowerCase()))
    await reload()
    const detail = failures.slice(0, 5).map(item => `${item.email}：${item.error}`).join('；')
    setMessage(`批量删除完成：成功 ${result.succeeded}，失败 ${result.failed}${detail ? '。' + detail : ''}${failures.length > 5 ? '；其余失败账号已保留勾选' : ''}`, result.failed ? 'error' : 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}

async function checkPlans() {
  if (!selectedAccounts.value.length) return setMessage('请先选择账号', 'error')
  busy.value = 'plans'
  try { const result = await api('/api/pro-accounts/check-plan', { method: 'POST', body: { emails: selectedAccounts.value.map((a) => a.email) } }); await reload(); setMessage(`套餐识别完成：成功 ${result.succeeded}，失败 ${result.failed}`, result.failed ? 'error' : 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function returnOne(account) {
  if (!window.confirm(`确认将“${account.email}”移回邮件管理？`)) return
  busy.value = 'return'
  try { await api(`/api/mail/accounts/${encodeURIComponent(account.email)}/management-scope`, { method: 'PUT', body: { scope: 'mail' } }); await reload(); setMessage('已移回邮件管理', 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
function syncGroupNames(provider) {
  const target = provider === 'sub2' ? settings.sub2 : settings.cpa
  const available = provider === 'sub2' ? sub2Groups.value : cpaGroups.value
  target.group_names = target.group_ids.map((id, index) => available.find((g) => Number(g.id) === Number(id))?.name || target.group_names[index] || `#${id}`)
}
function connectionPayload(provider) {
  return provider === 'sub2'
    ? { sub2: { url: settings.sub2.url.trim(), email: settings.sub2.email.trim() }, sub2_password: settings.sub2_password }
    : { cpa: { url: settings.cpa.url.trim() }, cpa_key: settings.cpa_key }
}
async function loadGroups(provider) {
  if (busy.value) return
  busy.value = `groups-${provider}`
  try { const groups = await api(`/api/pro-settings/${provider}/groups`, { method: 'POST', body: connectionPayload(provider) }); if (provider === 'sub2') sub2Groups.value = groups; else cpaGroups.value = groups; setMessage(`已读取 ${groups.length} 个分组`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function testProvider(provider) {
  if (busy.value) return
  busy.value = `test-${provider}`
  try { const result = await api(`/api/pro-settings/${provider}/test`, { method: 'POST', body: connectionPayload(provider) }); if (provider === 'sub2' && result.groups) sub2Groups.value = result.groups; setMessage(`${provider.toUpperCase()} 连接成功`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function saveSettings() {
  busy.value = 'save-settings'; syncGroupNames('sub2'); syncGroupNames('cpa')
  try { const saved = await api('/api/pro-settings', { method: 'PUT', body: settings }); Object.assign(settings, saved, { sub2_password: '', cpa_key: '' }); updateCountdown(); setMessage('Pro 配置已保存', 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}

onMounted(async () => {
  countdownTimer = window.setInterval(updateCountdown, 1000)
  try { await Promise.all([loadSettings(), loadPage()]) }
  catch (error) { setMessage(error.message, 'error') }
  finally { schedulePoll() }
})
onBeforeUnmount(() => { disposed = true; listRequestID++; window.clearInterval(countdownTimer); window.clearTimeout(searchTimer); window.clearTimeout(pollTimer) })
</script>

<template>
  <section class="view-stack">
    <ProAccountLogDialog ref="accountLogs" />
    <header class="page-heading">
      <div><span class="overline">PRO ACCOUNTS</span><h1>Pro 管理</h1><p>通过 Codex OAuth 管理 Pro 凭证、下游额度与自动空间合并</p></div>
      <div class="heading-actions"><button v-if="activeTab === 'accounts'" class="btn ghost" :disabled="!!busy" @click="transferDialog?.open()"><FileKey2 :size="15" />导入 / 导出</button><span v-if="settings.quota_enabled" class="countdown"><Clock3 :size="14" />{{ countdown }}s 后检测</span><button v-if="activeTab === 'accounts'" class="btn primary" type="button" :disabled="!!busy" @click="beginOAuth()"><CirclePlus :size="15" />添加账号</button></div>
    </header>
    <nav class="space-merge-tabs" aria-label="Pro 管理子菜单">
      <button :class="{ active: activeTab === 'accounts' }" type="button" @click="activeTab = 'accounts'"><UsersRound :size="15" />Pro 账号</button>
      <button :class="{ active: activeTab === 'push' }" type="button" @click="activeTab = 'push'"><Settings2 :size="15" />推送设置</button>
      <button :class="{ active: activeTab === 'gptpay' }" type="button" @click="activeTab = 'gptpay'"><Rocket :size="15" />GPTPay 供应商</button>
      <button :class="{ active: activeTab === 'cards' }" type="button" @click="activeTab = 'cards'"><FileKey2 :size="15" />银行卡信息</button>
      <button :class="{ active: activeTab === 'auto' }" type="button" @click="activeTab = 'auto'"><Settings2 :size="15" />Pro 全自动配置</button>
      <button :class="{active:activeTab==='runs'}" type="button" @click="activeTab='runs'"><Clock3 :size="15"/>Pro 执行记录</button>
    </nav>
    <MessageBar :message="message" />

    <template v-if="activeTab === 'accounts'">
      <div class="metric-grid pro-metrics">
        <article class="metric-card blue"><UsersRound :size="17" /><div><span>Pro 账号</span><strong>{{ listSummary.all }}</strong><small>当前账号池</small></div></article>
        <article class="metric-card green"><KeyRound :size="17" /><div><span>OAuth 完整</span><strong>{{ oauthReady }}</strong><small>AT 与 RT 已保存</small></div></article>
        <article class="metric-card amber"><Rocket :size="17" /><div><span>已推送</span><strong>{{ pushed }}</strong><small>当前下游可检测</small></div></article>
        <article class="metric-card slate"><BadgeCheck :size="17" /><div><span>已空间合并</span><strong>{{ merged }}</strong><small>永久成功标记</small></div></article>
      </div>
      <section class="panel list-panel">
        <div class="account-toolbar"><div class="space-filter-tabs"><button :class="{ active: spaceFilter === 'all' }" @click="setSpaceFilter('all')">全部 <span>{{ listSummary.all }}</span></button><button :class="{ active: spaceFilter === 'unmerged' }" @click="setSpaceFilter('unmerged')">未空间合并 <span>{{ Math.max(0, listSummary.all - merged) }}</span></button><button :class="{ active: spaceFilter === 'merged' }" @click="setSpaceFilter('merged')">已空间合并 <span>{{ merged }}</span></button></div><div class="heading-actions"><label class="compact-search"><Search :size="14" /><input v-model="query" placeholder="搜索账号" /></label><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length || selectedAccounts.some(account => account.pro_workflow_running || account.pro_auto?.status === 'running')" @click="requestTemporaryAT(selectedAccounts, true)"><KeyRound :size="14" />批量获取临时 AT</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="checkPlans"><RefreshCw :size="14" />识别套餐</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="runBatch('push')"><Rocket :size="14" />批量推送</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="runBatch('quota')"><Gauge :size="14" />批量查额度</button><button class="btn ghost danger-text" :disabled="!!busy || !selectedAccounts.length" @click="deleteSelectedAccounts"><LoaderCircle v-if="busy === 'delete-batch'" :size="14" class="spin" /><Trash2 v-else :size="14" />{{ busy === 'delete-batch' ? '删除中…' : '批量删除' }}<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span></button></div></div>
        <div class="table-shell"><table class="pro-table"><thead><tr><th><input type="checkbox" :checked="allVisibleSelected" @change="toggleVisible" /></th><th>账号</th><th>OAuth</th><th>套餐</th><th>开通银行卡</th><th>推送</th><th>5小时</th><th>7天</th><th>空间合并</th><th>全自动阶段</th><th>四步状态</th><th class="actions-column">操作</th></tr></thead><tbody>
          <tr v-if="!displayedAccounts.length"><td colspan="12" class="empty-cell">暂无符合条件的 Pro 账号</td></tr>
<tr v-for="account in displayedAccounts" :key="account.email"><td><input type="checkbox" :checked="selectedEmails.has(account.email.toLowerCase())" @change="toggle(account)" /></td><td class="account-cell"><strong>{{ account.email }}</strong><small>{{ formatTime(account.pro_managed_at || account.updated_at) }}</small><small v-if="account.pro_migration?.paused" class="table-note" :title="account.pro_migration.notice">迁移待续跑</small><small v-if="account.pro_last_error" class="danger-text" :title="account.pro_last_error">{{ account.pro_last_error }}</small></td><td><StatusPill :tone="statusTone(account.oauth_status)">{{ account.access_token_present && account.refresh_token_present ? 'AT / RT 完整' : '待授权' }}</StatusPill><small v-if="account.oauth_expires_at" class="table-note">{{ formatTime(account.oauth_expires_at) }}</small></td><td><StatusPill :tone="account.current_plan_type === 'pro' ? 'success' : 'pending'">{{ account.current_plan_type || '未识别' }}</StatusPill></td><td class="activation-card-cell"><template v-if="account.activation_card"><strong>{{ account.activation_card.card_name || '银行卡' }}</strong><small class="table-note">{{ account.activation_card.card_last4 ? '尾号 ' + account.activation_card.card_last4 : '尾号未记录' }}</small></template><span v-else class="table-note">未记录</span></td><td><StatusPill :tone="statusTone(account.push_status)">{{ account.push_status === 'completed' ? (account.push_provider || '-').toUpperCase() : account.push_status || '未推送' }}</StatusPill></td><td>{{ quotaLabel(account.quota_5h) }}</td><td>{{ quotaLabel(account.quota_7d) }}<small v-if="account.quota_checked_at" class="table-note">{{ formatTime(account.quota_checked_at) }}</small></td><td><StatusPill :tone="account.space_merged_once ? 'success' : 'pending'">{{ account.space_merged_once ? '已成功' : '未合并' }}</StatusPill></td><td class="auto-flow-cell"><button class="auto-flow-open" :title="account.pro_auto?.id ? '查看全自动进度' : '查看账号流程日志'" @click="account.pro_auto?.id ? autoPro?.open(account) : accountLogs?.open(account)"><ProAutoStages :state="phaseState(account)" /></button><small v-for="(error, stage) in phaseState(account).errors" :key="stage" class="table-note danger-text" :title="error">{{ error }}</small><small v-if="!account.pro_stage_progress && account.pro_auto?.error" class="table-note danger-text">{{ account.pro_auto.error }}</small></td><td class="flow-state"><div class="merge-stage-strip"><label v-for="step in mergeStages(account)" :key="step.key" class="merge-stage" :class="step.status" :data-stage="step.key" :title="`${step.label}：${step.text}`"><LoaderCircle v-if="step.status === 'running'" class="spin" :size="11" /><b>{{ { invite: '邀', accept: '进', transfer: '合', remove: '移' }[step.key] }}</b><select :value="step.status" :aria-label="`${step.label}阶段状态`" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @change="saveMergeStage(account, step, $event)"><option value="not_started">未开始</option><option value="pending">待处理</option><option v-if="step.status === 'running'" value="running" disabled>执行中</option><option v-if="step.status === 'team_removed'" value="team_removed" disabled>已移出</option><option v-if="step.status === 'unknown'" value="unknown" disabled>待确认</option><option value="completed">成功</option><option value="failed">失败</option></select></label></div><small v-if="mergeActivity(account)" class="merge-activity"><LoaderCircle class="spin" :size="11" />{{ mergeActivity(account) }}</small></td><td class="actions-cell"><ProActionMenu :email="account.email"><button v-if="account.pro_migration" type="button" title="继续迁移流程" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="resumeImported(account)"><Rocket :size="15" />继续迁移流程</button><button type="button" title="全自动开通 Pro" :disabled="!!busy || account.pro_workflow_running" @click="autoPro?.open(account)"><Rocket :size="15" />全自动开通 Pro</button><button type="button" title="查看账号全流程日志" @click="accountLogs?.open(account)"><History :size="15" />查看账号全流程日志</button><button type="button" title="查看、复制和导出 AT / RT" @click="credentialViewer?.open(account)"><FileKey2 :size="15" />查看、复制和导出 AT / RT</button><button type="button" title="手动授权" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="beginOAuth(account)"><LogIn :size="15" />手动授权</button><button type="button" title="刷新 AT" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || !account.refresh_token_present" @click="runOne(account, 'refresh')"><RefreshCw :size="15" />刷新 AT</button><button type="button" title="获取临时 AT" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="requestTemporaryAT([account])"><KeyRound :size="15" />获取临时 AT</button><button type="button" title="开通 Pro" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || !account.access_token_present" @click="recharge?.open(account)"><CirclePlus :size="15" />开通 Pro</button><button type="button" title="登录获取 RT / AT" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="requestOAuthLogin(account)"><BadgeCheck :size="15" />登录获取 RT / AT</button><button type="button" title="推送当前下游" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || !account.refresh_token_present" @click="runOne(account, 'push')"><Rocket :size="15" />推送当前下游</button><button type="button" title="查询额度" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || account.push_status !== 'completed'" @click="runOne(account, 'quota')"><Gauge :size="15" />查询额度</button><button type="button" title="执行或续跑空间合并" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || ((!account.refresh_token_present) && (account.pro_accept_status !== 'completed' || account.pro_transfer_status !== 'completed')) || account.pro_remove_status === 'completed' || account.pro_transfer_status === 'unknown'" @click="runOne(account, 'merge')"><LoaderCircle v-if="busy === `merge:${account.email}` || account.pro_workflow_running" class="spin" :size="15" /><ShieldCheck v-else :size="15" />执行或续跑空间合并</button><button type="button" title="移回邮件管理" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="returnOne(account)"><RotateCcw :size="15" />移回邮件管理</button></ProActionMenu></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="setPage" @update:page-size="setPageSize" />
      </section>
    </template>

    <form v-else-if="activeTab === 'push'" class="push-grid" @submit.prevent="saveSettings">
      <section class="panel provider-panel" :class="{ selected: settings.provider === 'sub2' }"><div class="panel-title"><div><span>SUB2</span><h2>Sub2 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="sub2" />{{ settings.provider === 'sub2' ? '当前启用' : '启用' }}</label></div><div class="settings-fields"><label class="field wide"><span>地址</span><input v-model="settings.sub2.url" placeholder="https://sub2.example.com" /></label><label class="field"><span>管理员邮箱</span><input v-model="settings.sub2.email" /></label><label class="field"><span>管理员密码</span><input v-model="settings.sub2_password" type="password" :placeholder="settings.sub2.password_present ? '已保存，留空不修改' : '请输入密码'" /></label><label class="field"><span>账号并发</span><input v-model.number="settings.sub2.account_concurrency" type="number" min="1" max="100" /></label><label class="field"><span>优先级</span><input v-model.number="settings.sub2.priority" type="number" min="1" max="100" /></label><label class="toggle-row wide"><div><strong>WS 推荐配置</strong><small>推送时携带 cpa_ws=1</small></div><input v-model="settings.sub2.cpa_ws" type="checkbox" /><i></i></label></div><div class="group-box"><div><strong>OpenAI 分组</strong><button class="btn ghost compact" type="button" :disabled="!!busy" @click="loadGroups('sub2')"><RefreshCw v-if="busy === 'groups-sub2'" class="spin" :size="14" />{{ busy === 'groups-sub2' ? '读取中' : '读取分组' }}</button></div><p v-if="!sub2Groups.length">暂无分组</p><label v-for="group in sub2Groups" :key="group.id"><input v-model="settings.sub2.group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div><div class="panel-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="testProvider('sub2')"><RefreshCw v-if="busy === 'test-sub2'" class="spin" :size="14" />{{ busy === 'test-sub2' ? '连接中' : '测试连接' }}</button></div></section>
      <section class="panel provider-panel" :class="{ selected: settings.provider === 'cpa' }"><div class="panel-title"><div><span>CPA</span><h2>CPA 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="cpa" />{{ settings.provider === 'cpa' ? '当前启用' : '启用' }}</label></div><div class="settings-fields"><label class="field wide"><span>Management API 地址</span><input v-model="settings.cpa.url" placeholder="http://cpa:8317" /></label><label class="field wide"><span>Management Key</span><input v-model="settings.cpa_key" type="password" :placeholder="settings.cpa.key_present ? '已保存，留空不修改' : '请输入 Key'" /></label><label class="toggle-row wide"><div><strong>WS 推荐配置</strong><small>上传 auth 文件时启用 WebSocket</small></div><input v-model="settings.cpa.websockets" type="checkbox" /><i></i></label></div><div class="group-box"><div><strong>账号分组</strong><button class="btn ghost compact" type="button" :disabled="!!busy" @click="loadGroups('cpa')"><RefreshCw v-if="busy === 'groups-cpa'" class="spin" :size="14" />{{ busy === 'groups-cpa' ? '读取中' : '读取分组' }}</button></div><p v-if="!cpaGroups.length">暂无分组</p><label v-for="group in cpaGroups" :key="group.id"><input v-model="settings.cpa.group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div><div class="panel-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="testProvider('cpa')"><RefreshCw v-if="busy === 'test-cpa'" class="spin" :size="14" />{{ busy === 'test-cpa' ? '连接中' : '测试连接' }}</button></div></section>
      <div class="save-row"><button class="btn primary" type="submit" :disabled="!!busy"><Settings2 :size="15" />保存推送设置</button></div>
    </form>

    <div v-if="oauth.open" class="modal-backdrop" @click.self="oauth.open = false"><form class="modal oauth-modal" @submit.prevent="finishOAuth"><div class="modal-heading"><div><span class="overline">OPENAI CODEX OAUTH</span><h2>{{ oauth.targetEmail ? '手动授权 Pro 账号' : '添加 Pro 账号' }}</h2></div><button class="icon-button" type="button" title="关闭" @click="oauth.open = false"><X :size="17" /></button></div><p>复制授权链接到需要登录的浏览器。完成 Google/OpenAI 登录后，页面会停在无法访问的 localhost，把地址栏中的完整 URL 粘贴到下方。</p><label class="field"><span>授权链接</span><div class="url-row"><input :value="oauth.authURL" readonly /><button class="btn ghost" type="button" @click="copyOAuthURL">复制链接</button></div></label><label class="field"><span>完整回调 URL</span><textarea v-model="oauth.callbackURL" rows="4" required placeholder="http://localhost:1455/auth/callback?code=...&state=..." /></label><small>会话有效期至 {{ formatTime(oauth.expiresAt) }}，成功后本地加密保存 AT、RT 与 ID Token。</small><div class="panel-actions"><button class="btn ghost" type="button" @click="oauth.open = false">取消</button><button class="btn primary" type="submit" :disabled="oauth.loading"><LoaderCircle v-if="oauth.loading" class="spin" :size="15" /><LogIn v-else :size="15" />完成授权</button></div></form></div>
    <ProGPTPayPanel v-if="['auto', 'cards', 'gptpay'].includes(activeTab)" :key="activeTab" :mode="activeTab === 'auto' ? 'settings' : activeTab === 'gptpay' ? 'orders' : 'cards'" :default-page-size="defaultPageSize" :admin-accounts="adminAccounts" @saved="loadSettings" />
    <ProSchedulePanel v-if="activeTab==='runs'" :default-page-size="defaultPageSize"/>
    <ProTransferDialog ref="transferDialog" :selected="selectedAccounts" :query="query" :merge-state="spaceFilter" @busy="transferBusy" @updated="reload" />
    <ProAutoDialog ref="autoPro" @updated="reload" />
    <ProRechargeDialog ref="recharge" @updated="loadPage" @progress="rechargeProgress" />
    <TemporaryATDialog ref="temporaryAT" @busy="temporaryATBusy" @progress="manualTaskProgress" @finished="temporaryATFinished" />
    <AccountCredentialsDialog ref="credentialViewer" @exported="setMessage($event, 'success')" @saved="loadPage" />
  </section>
</template>

<style scoped>
.space-merge-tabs { display:flex;flex-wrap:wrap;gap:3px;border-bottom:1px solid var(--line);margin-bottom:20px; }
.space-merge-tabs button { display:flex;align-items:center;gap:6px;padding:10px 14px;border-bottom:2px solid transparent;color:var(--muted);font-size:12px; }
.space-merge-tabs button.active {color:var(--green-strong);border-bottom-color:var(--green);}
.auto-flow-cell {min-width:280px;max-width:320px;} .auto-flow-open{display:block;text-align:left;} .auto-flow-cell .table-note {max-width:300px;white-space:normal;overflow-wrap:anywhere;}
.pro-metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.account-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 14px; }
.space-filter-tabs { display: flex; flex-wrap: wrap; gap: 8px; }
.space-filter-tabs button { min-height: 32px; padding: 0 11px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 10px; font-weight: 650; }
.space-filter-tabs button.active { border-color: rgba(37,143,97,.35); background: var(--green-bg); color: var(--green-strong); }
.pro-table { min-width: 1440px; }
.activation-card-cell { min-width:120px;max-width:180px; }
.activation-card-cell strong { display:block;white-space:normal;overflow-wrap:anywhere; }
.pro-table .actions-column, .pro-table .actions-cell { width:64px;min-width:64px;text-align:center; }
.pro-table th:first-child, .pro-table td:first-child { width: 42px; text-align: center; }
.pro-table th:nth-child(2), .pro-table td:nth-child(2) { width: 240px; }
.flow-state { white-space: nowrap; }
.merge-stage-strip { display: flex; gap: 5px; }
.merge-stage { display: inline-flex; gap: 4px; align-items: center; padding: 5px 6px; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font-size: 10px; }
.merge-stage select { width: 54px; min-width: 0; border: 0; padding: 0; background: transparent; color: inherit; font-size: 10px; cursor: pointer; }
.merge-stage select:disabled { cursor: default; }
.merge-stage.completed, .merge-stage.team_removed { border-color: rgba(37,143,97,.35); background: var(--green-bg); color: var(--green-strong); }
.merge-stage.running, .merge-stage.unknown { border-color: var(--amber); background: var(--amber-bg); color: var(--amber); }
.merge-stage.failed { border-color: var(--red); background: var(--red-bg); color: var(--red); }
.merge-activity { display: flex; gap: 5px; align-items: center; margin-top: 6px; color: var(--amber); font-size: 10px; }
.push-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.provider-panel { border-top: 2px solid transparent; } .provider-panel.selected { border-top-color: var(--green); }
.provider-choice { display: flex; align-items: center; gap: 6px; color: var(--text-2); font-size: 11px; }
.group-box { margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--line); }
.group-box > div { display: flex; justify-content: space-between; align-items: center; }
.group-box p { color: var(--muted); font-size: 11px; }
.group-box label { display: inline-flex; align-items: center; gap: 5px; margin: 10px 12px 0 0; font-size: 11px; }
.save-row { grid-column: 1 / -1; display: flex; justify-content: flex-end; }
.countdown { display: inline-flex; align-items: center; gap: 5px; color: var(--muted); font-size: 11px; }
.oauth-modal { width: min(650px, 100%); } .oauth-modal .field { margin-top: 16px; } .oauth-modal textarea { resize: vertical; }
.url-row { display: grid; grid-template-columns: 1fr auto; gap: 8px; } .spin { animation: spin .9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 1000px) { .pro-metrics, .push-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .account-toolbar { align-items: stretch; flex-direction: column; } }
@media (max-width: 700px) { .pro-metrics, .push-grid { grid-template-columns: 1fr; } .save-row { grid-column: auto; } }
</style>
