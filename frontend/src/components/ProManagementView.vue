<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { BadgeCheck, CirclePlus, Clock3, Gauge, KeyRound, LoaderCircle, LogIn, RefreshCw, Rocket, RotateCcw, Search, Settings2, ShieldCheck, UsersRound, X } from 'lucide-vue-next'
import { api } from '../api'
import { formatTime } from '../utils'
import MessageBar from './MessageBar.vue'
import Pagination from './Pagination.vue'
import StatusPill from './StatusPill.vue'

const props = defineProps({ accounts: { type: Array, default: () => [] }, adminAccounts: { type: Array, default: () => [] } })
const emit = defineEmits(['reload'])
const activeTab = ref('accounts')
const spaceFilter = ref('all')
const query = ref('')
const page = ref(1)
const pageSize = ref(10)
const selectedEmails = ref(new Set())
const busy = ref('')
const message = reactive({ text: '', type: '' })
const oauth = reactive({ open: false, loading: false, sessionID: '', authURL: '', callbackURL: '', expiresAt: '', targetEmail: '' })
const sub2Groups = ref([])
const cpaGroups = ref([])
const countdown = ref(0)
let countdownTimer

const defaults = () => ({
  provider: 'sub2', quota_enabled: false, quota_check_interval_seconds: 120,
  auto_merge_enabled: false, target_admin_id: '', target_seat_type: 'default', retry_count: 2, retry_interval_seconds: 3, concurrency: 2,
  next_quota_sweep_at: null,
  sub2: { url: '', email: '', password_present: false, group_ids: [], group_names: [], models: [], account_concurrency: 10, priority: 1, cpa_ws: false },
  cpa: { url: '', key_present: false, websockets: false, group_ids: [], group_names: [] },
  sub2_password: '', cpa_key: '',
})
const settings = reactive(defaults())

const filteredAccounts = computed(() => {
  const needle = query.value.trim().toLowerCase()
  return props.accounts.filter((account) => {
    if (spaceFilter.value === 'unmerged' && account.space_merged_once) return false
    if (spaceFilter.value === 'merged' && !account.space_merged_once) return false
    return !needle || `${account.email} ${account.current_plan_type || ''} ${account.push_provider || ''}`.toLowerCase().includes(needle)
  }).sort((a, b) => Date.parse(b.pro_managed_at || b.updated_at || 0) - Date.parse(a.pro_managed_at || a.updated_at || 0))
})
const displayedAccounts = computed(() => filteredAccounts.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const selectedAccounts = computed(() => props.accounts.filter((account) => selectedEmails.value.has(account.email.toLowerCase())))
const allVisibleSelected = computed(() => displayedAccounts.value.length && displayedAccounts.value.every((account) => selectedEmails.value.has(account.email.toLowerCase())))
const oauthReady = computed(() => props.accounts.filter((a) => a.access_token_present && a.refresh_token_present).length)
const pushed = computed(() => props.accounts.filter((a) => a.push_status === 'completed').length)
const merged = computed(() => props.accounts.filter((a) => a.space_merged_once).length)
const selectedAdmin = computed(() => props.adminAccounts.find((a) => a.id === settings.target_admin_id))

watch([query, spaceFilter], () => { page.value = 1 })
watch(() => props.accounts, (accounts) => {
  const available = new Set(accounts.map((a) => a.email.toLowerCase()))
  selectedEmails.value = new Set([...selectedEmails.value].filter((email) => available.has(email)))
}, { deep: true })

function setMessage(text = '', type = '') { message.text = text; message.type = type }
function toggle(account) { const next = new Set(selectedEmails.value); const email = account.email.toLowerCase(); next.has(email) ? next.delete(email) : next.add(email); selectedEmails.value = next }
function toggleVisible() { const next = new Set(selectedEmails.value); displayedAccounts.value.forEach((a) => allVisibleSelected.value ? next.delete(a.email.toLowerCase()) : next.add(a.email.toLowerCase())); selectedEmails.value = next }
function quotaLabel(window) { return window ? `${Number(window.used_percent || 0).toFixed(1)}%` : '-' }
function statusTone(value) { return value === 'completed' ? 'success' : value === 'failed' || value === 'reauthorize_required' ? 'danger' : value === 'running' ? 'running' : 'pending' }
function updateCountdown() { countdown.value = settings.next_quota_sweep_at ? Math.max(0, Math.ceil((Date.parse(settings.next_quota_sweep_at) - Date.now()) / 1000)) : 0 }
async function reload() { await emit('reload') }

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
  try {
    const account = await api('/api/pro-accounts/oauth/complete', { method: 'POST', body: { session_id: oauth.sessionID, callback_url: oauth.callbackURL } })
    oauth.open = false; await reload(); setMessage(`${account.email} OAuth 授权完成，AT/RT 已加密保存`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { oauth.loading = false }
}
async function copyOAuthURL() {
  try { await navigator.clipboard.writeText(oauth.authURL); setMessage('授权链接已复制', 'success') }
  catch { setMessage('复制失败，请手动选择授权链接', 'error') }
}
async function runOne(account, action) {
  busy.value = `${action}:${account.email}`
  const paths = { refresh: 'oauth/refresh', push: 'push', quota: 'quota', merge: 'merge' }
  try { await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/${paths[action]}`, { method: 'POST' }); await reload(); setMessage(`${account.email} 操作完成`, 'success') }
  catch (error) { await reload(); setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function runBatch(action) {
  if (!selectedAccounts.value.length) return setMessage('请先选择账号', 'error')
  busy.value = `batch-${action}`
  try {
    const result = await api(`/api/pro-accounts/${action}`, { method: 'POST', body: { emails: selectedAccounts.value.map((a) => a.email) } })
    await reload(); setMessage(`批量${action === 'push' ? '推送' : '额度检测'}完成：成功 ${result.succeeded}，失败 ${result.failed}`, result.failed ? 'error' : 'success')
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
  target.group_names = target.group_ids.map((id) => available.find((g) => Number(g.id) === Number(id))?.name).filter(Boolean)
}
async function loadGroups(provider) {
  busy.value = `groups-${provider}`
  try { const groups = await api(`/api/pro-settings/${provider}/groups`); if (provider === 'sub2') sub2Groups.value = groups; else cpaGroups.value = groups; setMessage(`已读取 ${groups.length} 个分组`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function testProvider(provider) {
  busy.value = `test-${provider}`
  try { await api(`/api/pro-settings/${provider}/test`, { method: 'POST' }); setMessage(`${provider.toUpperCase()} 连接成功`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function saveSettings() {
  busy.value = 'save-settings'; syncGroupNames('sub2'); syncGroupNames('cpa')
  try { const saved = await api('/api/pro-settings', { method: 'PUT', body: settings }); Object.assign(settings, saved, { sub2_password: '', cpa_key: '' }); updateCountdown(); setMessage('Pro 配置已保存', 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}

onMounted(async () => { await loadSettings(); countdownTimer = window.setInterval(updateCountdown, 1000) })
onBeforeUnmount(() => window.clearInterval(countdownTimer))
</script>

<template>
  <section class="view-stack">
    <header class="page-heading">
      <div><span class="overline">PRO ACCOUNTS</span><h1>Pro 管理</h1><p>通过 Codex OAuth 管理 Pro 凭证、下游额度与自动空间合并</p></div>
      <div class="heading-actions"><span v-if="settings.quota_enabled" class="countdown"><Clock3 :size="14" />{{ countdown }}s 后检测</span><button v-if="activeTab === 'accounts'" class="btn primary" type="button" :disabled="!!busy" @click="beginOAuth()"><CirclePlus :size="15" />添加账号</button></div>
    </header>
    <nav class="space-merge-tabs" aria-label="Pro 管理子菜单">
      <button :class="{ active: activeTab === 'accounts' }" type="button" @click="activeTab = 'accounts'"><UsersRound :size="15" />Pro 账号</button>
      <button :class="{ active: activeTab === 'push' }" type="button" @click="activeTab = 'push'"><Settings2 :size="15" />推送设置</button>
      <button :class="{ active: activeTab === 'merge' }" type="button" @click="activeTab = 'merge'"><ShieldCheck :size="15" />合并空间选择</button>
    </nav>
    <MessageBar :message="message" />

    <template v-if="activeTab === 'accounts'">
      <div class="metric-grid pro-metrics">
        <article class="metric-card blue"><UsersRound :size="17" /><div><span>Pro 账号</span><strong>{{ accounts.length }}</strong><small>当前账号池</small></div></article>
        <article class="metric-card green"><KeyRound :size="17" /><div><span>OAuth 完整</span><strong>{{ oauthReady }}</strong><small>AT 与 RT 已保存</small></div></article>
        <article class="metric-card amber"><Rocket :size="17" /><div><span>已推送</span><strong>{{ pushed }}</strong><small>当前下游可检测</small></div></article>
        <article class="metric-card slate"><BadgeCheck :size="17" /><div><span>已空间合并</span><strong>{{ merged }}</strong><small>永久成功标记</small></div></article>
      </div>
      <section class="panel list-panel">
        <div class="account-toolbar"><div class="space-filter-tabs"><button :class="{ active: spaceFilter === 'all' }" @click="spaceFilter = 'all'">全部 <span>{{ accounts.length }}</span></button><button :class="{ active: spaceFilter === 'unmerged' }" @click="spaceFilter = 'unmerged'">未空间合并 <span>{{ accounts.length - merged }}</span></button><button :class="{ active: spaceFilter === 'merged' }" @click="spaceFilter = 'merged'">已空间合并 <span>{{ merged }}</span></button></div><div class="heading-actions"><label class="compact-search"><Search :size="14" /><input v-model="query" placeholder="搜索账号" /></label><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="checkPlans"><RefreshCw :size="14" />识别套餐</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="runBatch('push')"><Rocket :size="14" />批量推送</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="runBatch('quota')"><Gauge :size="14" />批量查额度</button></div></div>
        <div class="table-shell"><table class="pro-table"><thead><tr><th><input type="checkbox" :checked="allVisibleSelected" @change="toggleVisible" /></th><th>账号</th><th>OAuth</th><th>套餐</th><th>推送</th><th>5小时</th><th>7天</th><th>空间合并</th><th>四步状态</th><th class="actions-column">操作</th></tr></thead><tbody>
          <tr v-if="!displayedAccounts.length"><td colspan="10" class="empty-cell">暂无符合条件的 Pro 账号</td></tr>
          <tr v-for="account in displayedAccounts" :key="account.email"><td><input type="checkbox" :checked="selectedEmails.has(account.email.toLowerCase())" @change="toggle(account)" /></td><td class="account-cell"><strong>{{ account.email }}</strong><small>{{ formatTime(account.pro_managed_at || account.updated_at) }}</small><small v-if="account.pro_last_error" class="danger-text" :title="account.pro_last_error">{{ account.pro_last_error }}</small></td><td><StatusPill :tone="statusTone(account.oauth_status)">{{ account.access_token_present && account.refresh_token_present ? 'AT / RT 完整' : '待授权' }}</StatusPill><small v-if="account.oauth_expires_at" class="table-note">{{ formatTime(account.oauth_expires_at) }}</small></td><td><StatusPill :tone="account.current_plan_type === 'pro' ? 'success' : 'pending'">{{ account.current_plan_type || '未识别' }}</StatusPill></td><td><StatusPill :tone="statusTone(account.push_status)">{{ account.push_status === 'completed' ? (account.push_provider || '-').toUpperCase() : account.push_status || '未推送' }}</StatusPill></td><td>{{ quotaLabel(account.quota_5h) }}</td><td>{{ quotaLabel(account.quota_7d) }}<small v-if="account.quota_checked_at" class="table-note">{{ formatTime(account.quota_checked_at) }}</small></td><td><StatusPill :tone="account.space_merged_once ? 'success' : 'pending'">{{ account.space_merged_once ? '已成功' : '未合并' }}</StatusPill></td><td class="flow-state"><span v-for="(item, index) in [account.invite_status, account.accept_status, account.transfer_status, account.remove_status]" :key="index" :class="item">{{ ['邀', '进', '合', '移'][index] }}</span></td><td><div class="row-actions"><button class="icon-button" title="重新授权" :disabled="!!busy" @click="beginOAuth(account)"><LogIn :size="15" /></button><button class="icon-button" title="刷新 AT" :disabled="!!busy || !account.refresh_token_present" @click="runOne(account, 'refresh')"><RefreshCw :size="15" /></button><button class="icon-button" title="推送当前下游" :disabled="!!busy || !account.refresh_token_present" @click="runOne(account, 'push')"><Rocket :size="15" /></button><button class="icon-button" title="查询额度" :disabled="!!busy || account.push_status !== 'completed'" @click="runOne(account, 'quota')"><Gauge :size="15" /></button><button class="icon-button" title="执行或续跑空间合并" :disabled="!!busy || !account.refresh_token_present || account.remove_status === 'completed'" @click="runOne(account, 'merge')"><ShieldCheck :size="15" /></button><button class="icon-button" title="移回邮件管理" :disabled="!!busy" @click="returnOne(account)"><RotateCcw :size="15" /></button></div></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="filteredAccounts.length" @update:page="page = $event" @update:page-size="pageSize = $event" />
      </section>
    </template>

    <form v-else-if="activeTab === 'push'" class="push-grid" @submit.prevent="saveSettings">
      <section class="panel provider-panel" :class="{ selected: settings.provider === 'sub2' }"><div class="panel-title"><div><span>SUB2</span><h2>Sub2 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="sub2" />{{ settings.provider === 'sub2' ? '当前启用' : '启用' }}</label></div><div class="settings-fields"><label class="field wide"><span>地址</span><input v-model="settings.sub2.url" placeholder="https://sub2.example.com" /></label><label class="field"><span>管理员邮箱</span><input v-model="settings.sub2.email" /></label><label class="field"><span>管理员密码</span><input v-model="settings.sub2_password" type="password" :placeholder="settings.sub2.password_present ? '已保存，留空不修改' : '请输入密码'" /></label><label class="field"><span>账号并发</span><input v-model.number="settings.sub2.account_concurrency" type="number" min="1" max="100" /></label><label class="field"><span>优先级</span><input v-model.number="settings.sub2.priority" type="number" min="1" max="100" /></label><label class="toggle-row wide"><div><strong>WS 推荐配置</strong><small>推送时携带 cpa_ws=1</small></div><input v-model="settings.sub2.cpa_ws" type="checkbox" /><i></i></label></div><div class="group-box"><div><strong>OpenAI 分组</strong><button class="btn ghost compact" type="button" @click="loadGroups('sub2')">读取分组</button></div><p v-if="!sub2Groups.length">保存连接后读取分组</p><label v-for="group in sub2Groups" :key="group.id"><input v-model="settings.sub2.group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div><div class="panel-actions"><button class="btn ghost" type="button" @click="testProvider('sub2')">测试连接</button></div></section>
      <section class="panel provider-panel" :class="{ selected: settings.provider === 'cpa' }"><div class="panel-title"><div><span>CPA</span><h2>CPA 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="cpa" />{{ settings.provider === 'cpa' ? '当前启用' : '启用' }}</label></div><div class="settings-fields"><label class="field wide"><span>Management API 地址</span><input v-model="settings.cpa.url" placeholder="http://cpa:8317" /></label><label class="field wide"><span>Management Key</span><input v-model="settings.cpa_key" type="password" :placeholder="settings.cpa.key_present ? '已保存，留空不修改' : '请输入 Key'" /></label><label class="toggle-row wide"><div><strong>WS 推荐配置</strong><small>上传 auth 文件时启用 WebSocket</small></div><input v-model="settings.cpa.websockets" type="checkbox" /><i></i></label></div><div class="group-box"><div><strong>账号分组</strong><button class="btn ghost compact" type="button" @click="loadGroups('cpa')">读取分组</button></div><p v-if="!cpaGroups.length">保存连接后读取分组</p><label v-for="group in cpaGroups" :key="group.id"><input v-model="settings.cpa.group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div><div class="panel-actions"><button class="btn ghost" type="button" @click="testProvider('cpa')">测试连接</button></div></section>
      <div class="save-row"><button class="btn primary" type="submit" :disabled="!!busy"><Settings2 :size="15" />保存推送设置</button></div>
    </form>

    <form v-else class="panel merge-settings" @submit.prevent="saveSettings">
      <div class="panel-title"><div><span>AUTO SPACE MERGE</span><h2>额度检测与合并空间</h2><p>7d 额度明确达到 100% 后执行邀请、进入、合并、移出</p></div></div>
      <div class="settings-fields merge-fields"><label class="toggle-row wide"><div><strong>额度自动检测</strong><small>只检测已推送到当前线路且未合并的 Pro 账号</small></div><input v-model="settings.quota_enabled" type="checkbox" /><i></i></label><label class="field"><span>检测间隔（秒）</span><input v-model.number="settings.quota_check_interval_seconds" type="number" min="10" /></label><label class="field"><span>并发账号数</span><input v-model.number="settings.concurrency" type="number" min="1" max="20" /></label><label class="toggle-row wide"><div><strong>7d 用尽自动合并</strong><small>401、超时或无 7d 数据不会触发</small></div><input v-model="settings.auto_merge_enabled" type="checkbox" /><i></i></label><label class="field wide"><span>目标母号</span><select v-model="settings.target_admin_id"><option value="">请选择母号</option><option v-for="admin in adminAccounts" :key="admin.id" :value="admin.id">{{ admin.label || admin.email }} · {{ admin.team_account_id || '无 Team ID' }}</option></select></label><label class="field"><span>空间席位套餐</span><select v-model="settings.target_seat_type"><option value="default">Standard（普通空间）</option><option value="prolite">Premium（5x）</option></select></label><label class="field"><span>失败重试次数</span><input v-model.number="settings.retry_count" type="number" min="0" max="10" /></label><label class="field"><span>重试间隔（秒）</span><input v-model.number="settings.retry_interval_seconds" type="number" min="1" /></label><div class="target-summary wide"><strong>{{ selectedAdmin?.label || selectedAdmin?.email || '尚未选择母号' }}</strong><span>{{ selectedAdmin?.team_account_id || '选择后显示目标 Team ID' }}</span></div></div>
      <div class="panel-actions"><button class="btn primary" type="submit" :disabled="!!busy"><Settings2 :size="15" />保存合并设置</button></div>
    </form>

    <div v-if="oauth.open" class="modal-backdrop" @click.self="oauth.open = false"><form class="modal oauth-modal" @submit.prevent="finishOAuth"><div class="modal-heading"><div><span class="overline">OPENAI CODEX OAUTH</span><h2>{{ oauth.targetEmail ? '重新授权 Pro 账号' : '添加 Pro 账号' }}</h2></div><button class="icon-button" type="button" title="关闭" @click="oauth.open = false"><X :size="17" /></button></div><p>复制授权链接到需要登录的浏览器。完成 Google/OpenAI 登录后，页面会停在无法访问的 localhost，把地址栏中的完整 URL 粘贴到下方。</p><label class="field"><span>授权链接</span><div class="url-row"><input :value="oauth.authURL" readonly /><button class="btn ghost" type="button" @click="copyOAuthURL">复制链接</button></div></label><label class="field"><span>完整回调 URL</span><textarea v-model="oauth.callbackURL" rows="4" required placeholder="http://localhost:1455/auth/callback?code=...&state=..." /></label><small>会话有效期至 {{ formatTime(oauth.expiresAt) }}，成功后本地加密保存 AT、RT 与 ID Token。</small><div class="panel-actions"><button class="btn ghost" type="button" @click="oauth.open = false">取消</button><button class="btn primary" type="submit" :disabled="oauth.loading"><LoaderCircle v-if="oauth.loading" class="spin" :size="15" /><LogIn v-else :size="15" />完成授权</button></div></form></div>
  </section>
</template>

<style scoped>
.pro-metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.account-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 14px; }
.space-filter-tabs { display: flex; flex-wrap: wrap; gap: 8px; }
.space-filter-tabs button { min-height: 32px; padding: 0 11px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 10px; font-weight: 650; }
.space-filter-tabs button.active { border-color: rgba(37,143,97,.35); background: var(--green-bg); color: var(--green-strong); }
.pro-table { min-width: 1500px; }
.pro-table th:first-child, .pro-table td:first-child { width: 42px; text-align: center; }
.pro-table th:nth-child(2), .pro-table td:nth-child(2) { width: 240px; }
.flow-state { white-space: nowrap; }
.flow-state span { display: inline-grid; width: 23px; height: 23px; margin-right: 3px; place-items: center; border: 1px solid var(--line); border-radius: 4px; color: var(--muted); font-size: 9px; }
.flow-state span.completed { border-color: rgba(37,143,97,.35); background: var(--green-bg); color: var(--green-strong); }
.flow-state span.running { color: var(--amber); } .flow-state span.failed { color: var(--danger); }
.push-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; }
.provider-panel { border-top: 2px solid transparent; } .provider-panel.selected { border-top-color: var(--green); }
.provider-choice { display: flex; align-items: center; gap: 6px; color: var(--text-2); font-size: 11px; }
.group-box { margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--line); }
.group-box > div { display: flex; justify-content: space-between; align-items: center; }
.group-box p { color: var(--muted); font-size: 11px; }
.group-box label { display: inline-flex; align-items: center; gap: 5px; margin: 10px 12px 0 0; font-size: 11px; }
.save-row { grid-column: 1 / -1; display: flex; justify-content: flex-end; }
.merge-settings { max-width: 900px; } .merge-fields { max-width: none; }
.target-summary { display: flex; flex-direction: column; gap: 4px; padding: 12px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); }
.target-summary span { color: var(--muted); font-size: 11px; }
.countdown { display: inline-flex; align-items: center; gap: 5px; color: var(--muted); font-size: 11px; }
.oauth-modal { width: min(650px, 100%); } .oauth-modal .field { margin-top: 16px; } .oauth-modal textarea { resize: vertical; }
.url-row { display: grid; grid-template-columns: 1fr auto; gap: 8px; } .spin { animation: spin .9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 1000px) { .pro-metrics, .push-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .account-toolbar { align-items: stretch; flex-direction: column; } }
@media (max-width: 700px) { .pro-metrics, .push-grid { grid-template-columns: 1fr; } .save-row { grid-column: auto; } }
</style>
