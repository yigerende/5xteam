<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import { Check, CheckCircle2, Copy, FileJson, FileKey2, Network, Pencil, RefreshCw, Save, Trash2, X } from 'lucide-vue-next'
import { api } from '../api'
import { decodeJWTPayload, findAccessToken, findRefreshToken, formatTime, shortID } from '../utils'
import IconButton from './IconButton.vue'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'
import Pagination from './Pagination.vue'

const props = defineProps({ accounts: { type: Array, default: () => [] }, proxies: { type: Array, default: () => [] } })
const emit = defineEmits(['reload'])
const form = reactive({ id: '', label: '', session: '', refreshToken: '', teamID: '' })
const parsed = reactive({ accessToken: '', refreshToken: '', preview: null })
const tests = ref(new Map())
const message = reactive({ text: '', type: '' })
const busy = ref(false)
const fileInput = ref(null)
const capacities = ref(new Map())
const capacityBusy = ref(new Set())
const capacityAllBusy = ref(false)
const page = ref(1)
const pageSize = ref(10)
const rows = ref([])
const total = ref(0)
const listSummary = reactive({ all: 0, child_entries: 0, child_total_cost_usd: 0 })
const adminMenu = ref('list')
const proxySaving = ref(new Set())
const credentialDialog = reactive({ open: false, loading: false, error: '', label: '', email: '', accountID: '', teamAccountID: '', accessToken: '', refreshToken: '', copied: '' })
const pagedAccounts = computed(() => rows.value)
async function loadPage() {
  const data = await api(`/api/admin-accounts?page=${page.value}&page_size=${pageSize.value}`)
  rows.value = data.items || []; total.value = Number(data.total || 0); Object.assign(listSummary, data.summary || {})
  const lastPage = Math.max(1, Math.ceil(total.value / pageSize.value))
  if (page.value > lastPage) { page.value = lastPage; return loadPage() }
}
function setPage(value) { page.value = value; loadPage() }
function setPageSize(value) { pageSize.value = value; page.value = 1; loadPage() }

const capacitySummary = computed(() => {
  const summary = { standard: { total: 0, remaining: 0 }, premium: { total: 0, remaining: 0 }, loaded: 0 }
  for (const value of capacities.value.values()) {
	if (value?.standard || value?.premium) summary.loaded += 1
    for (const key of ['standard', 'premium']) {
      summary[key].total += Number(value?.[key]?.total || 0)
      summary[key].remaining += Number(value?.[key]?.remaining || 0)
    }
  }
  return summary
})
const teamRotationChildSummary = computed(() => Number(listSummary.child_entries || 0))

async function loadCapacity(account) {
  const nextBusy = new Set(capacityBusy.value); nextBusy.add(account.id); capacityBusy.value = nextBusy
  try {
    const result = await api(`/api/admin-accounts/${encodeURIComponent(account.id)}/capacity`)
    const next = new Map(capacities.value); next.set(account.id, result); capacities.value = next
  } catch (error) {
    const next = new Map(capacities.value); next.set(account.id, { error: error.message }); capacities.value = next
  } finally { const next = new Set(capacityBusy.value); next.delete(account.id); capacityBusy.value = next }
}

async function loadCapacities() {
  if (capacityAllBusy.value || !props.accounts.length) return
  capacityAllBusy.value = true
  try {
    await Promise.all(props.accounts.map(loadCapacity))
    setMessage('全部母号席位已刷新', 'success')
  } finally { capacityAllBusy.value = false }
}
watch(() => props.accounts.map(account => account.id).join(','), () => {
  const ids = new Set(props.accounts.map(account => account.id))
  capacities.value = new Map([...capacities.value.entries()].filter(([id]) => ids.has(id)))
})
function seatText(account, key) { const bucket = capacities.value.get(account.id)?.[key]; return bucket ? `${bucket.remaining} / ${bucket.total}` : '—' }
function heldText(account) {
  const capacity = capacities.value.get(account.id)
  if (!capacity || capacity.error) return '—'
  return `普通 ${Number(capacity.standard?.held || 0)} / 5x ${Number(capacity.premium?.held || 0)}`
}
function seatError(account) { return capacities.value.get(account.id)?.error || '' }
function seatTitle(account, key) { const bucket = capacities.value.get(account.id)?.[key]; return bucket ? `剩余 ${bucket.remaining}，总计 ${bucket.total}` : seatError(account) || '点击刷新席位数据' }
function proxyName(proxyID) { return props.proxies.find(proxy => proxy.id === proxyID)?.name || '跟随全局代理' }
async function saveProxy(account, proxyID) {
  const nextBusy = new Set(proxySaving.value); nextBusy.add(account.id); proxySaving.value = nextBusy
  try {
    await api(`/api/admin-accounts/${encodeURIComponent(account.id)}/proxy`, { method: 'PUT', body: { proxy_id: proxyID || '' } })
    setMessage(`${account.label} 的专属代理已更新`, 'success')
    emit('reload'); await loadPage()
  } catch (error) { setMessage(error.message, 'error') }
  finally { const next = new Set(proxySaving.value); next.delete(account.id); proxySaving.value = next }
}

function setMessage(text = '', type = '') { Object.assign(message, { text, type }) }

function parseSession() {
  const raw = form.session.trim()
  if (!raw) return setMessage('请粘贴 Session JSON 或 Access Token', 'error')
  try {
    let session = null
    let token = raw
    let refreshToken = form.refreshToken.trim()
    if (raw.startsWith('{') || raw.startsWith('[')) {
      session = JSON.parse(raw)
      token = findAccessToken(session)
      refreshToken = findRefreshToken(session) || refreshToken
      if (!token) throw new Error('Session JSON 中没有 accessToken 字段')
    }
    const claims = decodeJWTPayload(token)
    const auth = claims['https://api.openai.com/auth'] || {}
    const profile = claims['https://api.openai.com/profile'] || {}
    parsed.accessToken = token
    parsed.refreshToken = refreshToken
    form.refreshToken = refreshToken
    const accountID = session?.account?.id || auth.chatgpt_account_id || ''
    if (!form.teamID && accountID) form.teamID = accountID
    parsed.preview = {
      email: session?.user?.email || profile.email || '未知账号',
      plan: session?.account?.planType || auth.chatgpt_plan_type || '未知计划',
      accountID,
      expires: session?.expires || (claims.exp ? new Date(claims.exp * 1000).toISOString() : ''),
    }
    setMessage(`凭据解析成功${refreshToken ? '，已读取 Refresh Token' : '，未找到 Refresh Token'}`, 'success')
  } catch (error) {
    Object.assign(parsed, { accessToken: '', refreshToken: '', preview: null })
    setMessage(error.message, 'error')
  }
}

async function importFile(event) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file) return
  try { form.session = await file.text(); parseSession() }
  catch (error) { setMessage(`读取 JSON 文件失败：${error.message}`, 'error') }
}

function reset() {
  Object.assign(form, { id: '', label: '', session: '', refreshToken: '', teamID: '' })
  Object.assign(parsed, { accessToken: '', refreshToken: '', preview: null })
  setMessage()
}

function edit(account) {
  adminMenu.value = 'add'
  Object.assign(form, { id: account.id, label: account.label, session: '', refreshToken: '', teamID: account.team_account_id })
  Object.assign(parsed, { accessToken: '', refreshToken: '', preview: { email: account.email, plan: account.plan_type, accountID: account.team_account_id, saved: true } })
  setMessage('正在编辑已保存的母号，凭据留空将保持不变')
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function save() {
  if (!form.label.trim() || (!form.id && !form.session.trim())) return setMessage('母号名称和 Access Token 不能为空', 'error')
  if (!parsed.accessToken && (form.session.trim().startsWith('{') || form.session.trim().startsWith('['))) {
    parseSession()
    if (!parsed.accessToken) return
  }
  busy.value = true
  try {
    const account = await api(form.id ? `/api/admin-accounts/${encodeURIComponent(form.id)}` : '/api/admin-accounts', {
      method: form.id ? 'PUT' : 'POST',
      body: { label: form.label.trim(), access_token: parsed.accessToken || form.session.trim(), refresh_token: parsed.refreshToken || form.refreshToken.trim(), team_account_id: form.teamID.trim() },
    })
    reset()
    emit('reload'); await loadPage()
    adminMenu.value = 'list'
    setMessage(`${account.label} 已加密保存`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}

async function test(account = null) {
  const useSaved = Boolean(account)
  if (!useSaved && !parsed.accessToken && form.session.trim()) parseSession()
  const payload = { id: useSaved ? account.id : '', access_token: useSaved ? '' : parsed.accessToken, team_account_id: useSaved ? '' : form.teamID.trim() }
  if (!payload.id && !payload.access_token) return setMessage('请先解析 Access Token', 'error')
  busy.value = true
  try {
    const result = await api('/api/admin-accounts/test', { method: 'POST', body: payload })
    if (account) { const next = new Map(tests.value); next.set(account.id, result); tests.value = next }
    setMessage(`${result.message}，耗时 ${result.latency_ms}ms`, result.valid ? 'success' : 'error')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}

async function refresh(account) {
  busy.value = true
  try {
    const result = await api(`/api/admin-accounts/${encodeURIComponent(account.id)}/refresh`, { method: 'POST', body: {} })
    emit('reload'); await loadPage()
    setMessage(result.message, 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}

async function openCredentialDialog(account) {
  Object.assign(credentialDialog, { open: true, loading: true, error: '', label: account.label || '', email: account.email || '', accountID: '', teamAccountID: account.team_account_id || '', accessToken: '', refreshToken: '', copied: '' })
  try {
    const result = await api(`/api/admin-accounts/${encodeURIComponent(account.id)}/credentials`)
    Object.assign(credentialDialog, {
      label: result.label || account.label || '', email: result.email || account.email || '', accountID: result.account_id || '',
      teamAccountID: result.team_account_id || account.team_account_id || '', accessToken: result.access_token || '', refreshToken: result.refresh_token || '',
    })
  } catch (error) { credentialDialog.error = error.message }
  finally { credentialDialog.loading = false }
}
function closeCredentialDialog() {
  Object.assign(credentialDialog, { open: false, loading: false, error: '', label: '', email: '', accountID: '', teamAccountID: '', accessToken: '', refreshToken: '', copied: '' })
}
async function copyCredential(kind) {
  const value = kind === 'at' ? credentialDialog.accessToken : credentialDialog.refreshToken
  if (!value) return
  try {
    await navigator.clipboard.writeText(value)
    credentialDialog.copied = kind
    window.setTimeout(() => { if (credentialDialog.copied === kind) credentialDialog.copied = '' }, 1600)
  } catch { credentialDialog.error = '复制失败，请选中凭证后手动复制' }
}

async function remove(account) {
  if (!window.confirm(`确认删除母号“${account.label}”？`)) return
  try {
    await api(`/api/admin-accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' })
    if (form.id === account.id) reset()
    emit('reload'); await loadPage()
    setMessage(`${account.label} 已删除`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
}
onMounted(loadPage)
</script>

<template>
  <section class="view-stack">
    <header class="page-heading"><div><span class="overline">CREDENTIAL VAULT</span><h1>母号管理</h1><p>维护团队管理员凭据与自动续期状态</p></div><StatusPill tone="success">{{ total }} 个母号</StatusPill></header>
    <nav class="admin-subnav" aria-label="母号管理菜单"><button type="button" :class="{ active: adminMenu === 'add' }" @click="adminMenu = 'add'"><FileJson :size="14" />添加母号</button><button type="button" :class="{ active: adminMenu === 'list' }" @click="adminMenu = 'list'"><CheckCircle2 :size="14" />母号列表 <span>{{ total }}</span></button><button type="button" :class="{ active: adminMenu === 'proxy' }" @click="adminMenu = 'proxy'"><Network :size="14" />母号代理 <span>{{ props.proxies.length }}</span></button></nav>
    <section class="metric-grid seat-summary"><article class="metric-card"><span>普通席位总量</span><strong>{{ capacitySummary.loaded ? capacitySummary.standard.total : '—' }}</strong><small>剩余 {{ capacitySummary.loaded ? capacitySummary.standard.remaining : '—' }}</small></article><article class="metric-card"><span>高级席位 5x 总量</span><strong>{{ capacitySummary.loaded ? capacitySummary.premium.total : '—' }}</strong><small>剩余 {{ capacitySummary.loaded ? capacitySummary.premium.remaining : '—' }}</small></article><article class="metric-card"><span>普通席位剩余</span><strong>{{ capacitySummary.loaded ? capacitySummary.standard.remaining : '—' }}</strong><small>跨母号汇总</small></article><article class="metric-card"><span>高级席位 5x 剩余</span><strong>{{ capacitySummary.loaded ? capacitySummary.premium.remaining : '—' }}</strong><small>跨母号汇总</small></article><article class="metric-card"><span>累计进入子号总数</span><strong>{{ teamRotationChildSummary }}</strong><small>所有母号累计汇总</small></article></section>
    <div :class="['management-grid', { 'list-only': adminMenu === 'list' || adminMenu === 'proxy' }]">
      <form v-if="adminMenu === 'add'" class="panel editor-panel" @submit.prevent="save">
        <div class="panel-title"><div><span>{{ form.id ? 'EDIT' : 'NEW' }}</span><h2>{{ form.id ? '编辑母号' : '添加母号' }}</h2></div><button v-if="form.id" class="btn ghost" type="button" @click="reset"><X :size="15" />取消</button></div>
        <label class="field"><span>配置名称</span><input v-model="form.label" maxlength="40" placeholder="例如：主团队管理员" required /></label>
        <label class="field"><span>Session JSON / Access Token <small>{{ form.id ? '留空保留已保存凭据' : '' }}</small></span><textarea v-model="form.session" rows="7" spellcheck="false" placeholder="粘贴 Session JSON 或 Access Token"></textarea></label>
        <div class="field-row"><label class="field"><span>Refresh Token <small>可选</small></span><input v-model="form.refreshToken" type="password" autocomplete="off" /></label><label class="field"><span>团队 Account ID <small>可自动读取</small></span><input v-model="form.teamID" placeholder="account-..." /></label></div>
        <input ref="fileInput" hidden type="file" accept=".json,application/json" @change="importFile" />
        <div class="compact-actions"><button class="btn ghost" type="button" @click="fileInput.click()"><FileJson :size="15" />导入 JSON</button><button class="btn ghost" type="button" @click="parseSession"><CheckCircle2 :size="15" />解析凭据</button><button class="btn ghost" type="button" :disabled="busy" @click="test()"><CheckCircle2 :size="15" />校验</button></div>
        <div class="parse-preview" :class="{ empty: !parsed.preview }"><template v-if="parsed.preview"><strong>{{ parsed.preview.email }}</strong><span>{{ parsed.preview.plan || '-' }}</span><span>{{ shortID(parsed.preview.accountID) }}</span><small>{{ parsed.preview.saved ? '已保存加密凭据' : (parsed.preview.expires ? `到期 ${formatTime(parsed.preview.expires)}` : '未提供到期时间') }}</small></template><span v-else>解析后的账号信息会显示在这里</span></div>
        <MessageBar :message="message" />
        <div class="panel-actions"><button class="btn primary" type="submit" :disabled="busy"><Save :size="15" />{{ form.id ? '保存修改' : '保存母号' }}</button></div>
      </form>

      <section v-if="adminMenu === 'list'" class="panel list-panel">
        <div class="panel-title"><div><span>ACCOUNTS</span><h2>已保存母号</h2></div><div class="heading-actions"><span class="muted-count">{{ total }} 条记录</span><button class="btn ghost" type="button" :disabled="capacityAllBusy || !accounts.length" @click="loadCapacities"><RefreshCw :class="{ spin: capacityAllBusy }" :size="15" />刷新全部席位</button></div></div>
        <div class="table-shell"><table><thead><tr><th>名称 / 邮箱</th><th>计划</th><th>团队</th><th>专属代理</th><th>当前在空间</th><th>普通席位</th><th>高级席位 5x</th><th>临停席位</th><th>累计进入子号次数</th><th>子号累计消耗</th><th>续期</th><th>校验</th><th class="actions-column">操作</th></tr></thead><tbody>
          <tr v-if="!accounts.length"><td colspan="13" class="empty-cell">暂无母号配置</td></tr>
          <tr v-for="account in pagedAccounts" :key="account.id"><td class="account-cell"><strong>{{ account.label }}</strong><small>{{ account.email }}</small></td><td>{{ account.plan_type || '-' }}</td><td class="mono" :title="account.team_account_id">{{ shortID(account.team_account_id) }}</td><td><select class="proxy-select compact" :value="account.proxy_id || ''" :disabled="proxySaving.has(account.id)" @change="saveProxy(account, $event.target.value)"><option value="">跟随全局</option><option v-for="proxy in props.proxies" :key="proxy.id" :value="proxy.id">{{ proxy.name }}</option></select></td><td :title="account.current_space_count ? `当前在空间：\n${(account.current_space_emails || []).join('\n')}` : '当前没有子号在空间中'"><strong>{{ account.current_space_count || 0 }}</strong><small class="table-note">个子号</small></td><td class="seat-cell" :title="seatTitle(account, 'standard')"><strong>{{ seatText(account, 'standard') }}</strong><small>{{ seatError(account) ? '读取失败' : '剩余 / 总量' }}</small></td><td class="seat-cell" :title="seatTitle(account, 'premium')"><strong>{{ seatText(account, 'premium') }}</strong><small>{{ seatError(account) ? '读取失败' : '剩余 / 总量' }}</small></td><td class="seat-cell"><strong>{{ heldText(account) }}</strong><small>{{ seatError(account) ? '读取失败' : 'On hold' }}</small></td><td><strong>{{ account.team_rotation_child_count || 0 }}</strong><small class="table-note">次</small></td><td><strong>${{ Number(account.team_rotation_child_cost_usd || 0).toFixed(4) }}</strong><small class="table-note">Sub2</small></td><td><StatusPill :tone="account.refresh_token_present ? 'success' : 'pending'">{{ account.refresh_token_present ? 'RT 已保存' : '无 RT' }}</StatusPill><small class="table-note">{{ account.access_token_expires_at ? `AT ${formatTime(account.access_token_expires_at)} 到期` : '未记录到期时间' }}</small></td><td><template v-if="tests.get(account.id)"><StatusPill :tone="tests.get(account.id).valid ? 'success' : 'danger'">{{ tests.get(account.id).valid ? '有效' : '无效' }}</StatusPill><small class="table-note">{{ tests.get(account.id).latency_ms }}ms</small></template><StatusPill v-else tone="pending">未校验</StatusPill></td><td><div class="row-actions"><IconButton label="查看 AT / RT" :disabled="busy" @click="openCredentialDialog(account)"><FileKey2 :size="15" /></IconButton><IconButton label="刷新席位" :disabled="capacityBusy.has(account.id)" @click="loadCapacity(account)"><RefreshCw :size="15" /></IconButton><IconButton label="校验凭据" :disabled="busy" @click="test(account)"><CheckCircle2 :size="15" /></IconButton><IconButton label="刷新 AT/RT" :disabled="busy" @click="refresh(account)"><RefreshCw :size="15" /></IconButton><IconButton label="编辑母号" @click="edit(account)"><Pencil :size="15" /></IconButton><IconButton label="删除母号" danger @click="remove(account)"><Trash2 :size="15" /></IconButton></div></td></tr>
        </tbody></table></div><Pagination :page="page" :page-size="pageSize" :total="total" @update:page="setPage" @update:page-size="setPageSize" />
      </section>
      <section v-if="adminMenu === 'proxy'" class="panel list-panel">
        <div class="panel-title"><div><span>DEDICATED PROXY</span><h2>母号代理</h2><p class="panel-subtitle">母号访问 OpenAI / Team 接口时使用这里绑定的专属代理；留空则跟随全局代理。</p></div><span class="muted-count">{{ accounts.length }} 个母号 · {{ props.proxies.length }} 条代理</span></div>
        <div class="table-shell"><table><thead><tr><th>母号</th><th>团队</th><th>专属代理</th><th>状态</th></tr></thead><tbody>
          <tr v-if="!accounts.length"><td colspan="4" class="empty-cell">暂无母号配置</td></tr>
          <tr v-for="account in accounts" :key="account.id"><td class="account-cell"><strong>{{ account.label }}</strong><small>{{ account.email }}</small></td><td class="mono">{{ shortID(account.team_account_id) }}</td><td><select class="proxy-select" :value="account.proxy_id || ''" :disabled="proxySaving.has(account.id)" @change="saveProxy(account, $event.target.value)"><option value="">跟随全局代理</option><option v-for="proxy in props.proxies" :key="proxy.id" :value="proxy.id">{{ proxy.name }}</option></select></td><td><StatusPill :tone="proxySaving.has(account.id) ? 'running' : 'success'">{{ proxySaving.has(account.id) ? '保存中' : proxyName(account.proxy_id) }}</StatusPill></td></tr>
        </tbody></table></div>
      </section>
    </div>
    <div v-if="credentialDialog.open" class="modal-backdrop credential-dialog-backdrop" @click.self="closeCredentialDialog"><section class="modal credential-dialog" role="dialog" aria-modal="true" aria-labelledby="admin-credential-dialog-title"><header class="credential-dialog-header"><div><span class="overline">TEAM ADMIN CREDENTIALS</span><h2 id="admin-credential-dialog-title">查看母号 AT / RT</h2><p>{{ credentialDialog.label }} · {{ credentialDialog.email }}</p></div><IconButton label="关闭凭证窗口" @click="closeCredentialDialog"><X :size="16" /></IconButton></header><div v-if="credentialDialog.loading" class="credential-loading"><RefreshCw class="spin" :size="20" /><span>正在读取加密凭证</span></div><template v-else><div v-if="credentialDialog.error" class="credential-error">{{ credentialDialog.error }}</div><label class="credential-token-field"><span><strong>Access Token (AT)</strong><button type="button" :disabled="!credentialDialog.accessToken" @click="copyCredential('at')"><Check v-if="credentialDialog.copied === 'at'" :size="14" /><Copy v-else :size="14" />{{ credentialDialog.copied === 'at' ? '已复制' : '复制 AT' }}</button></span><textarea :value="credentialDialog.accessToken || '未保存'" readonly rows="5" spellcheck="false" @focus="$event.target.select()"></textarea></label><label class="credential-token-field"><span><strong>Refresh Token (RT)</strong><button type="button" :disabled="!credentialDialog.refreshToken" @click="copyCredential('rt')"><Check v-if="credentialDialog.copied === 'rt'" :size="14" /><Copy v-else :size="14" />{{ credentialDialog.copied === 'rt' ? '已复制' : '复制 RT' }}</button></span><textarea :value="credentialDialog.refreshToken || '未保存'" readonly rows="4" spellcheck="false" @focus="$event.target.select()"></textarea></label><small v-if="credentialDialog.accountID || credentialDialog.teamAccountID" class="credential-account-id mono">Account ID: {{ credentialDialog.accountID || '-' }} · Team: {{ credentialDialog.teamAccountID || '-' }}</small></template></section></div>
  </section>
</template>

<style scoped>
.admin-subnav { display: flex; align-items: center; gap: 6px; padding: 4px; border-bottom: 1px solid var(--line); }
.admin-subnav button { display: inline-flex; align-items: center; gap: 7px; min-height: 34px; padding: 0 12px; border: 1px solid transparent; border-radius: 5px; background: transparent; color: var(--muted); font-size: 11px; font-weight: 700; cursor: pointer; }
.admin-subnav button:hover, .admin-subnav button.active { border-color: rgba(37, 143, 97, .25); background: var(--green-bg); color: var(--green-strong); }
.admin-subnav button span { min-width: 18px; padding: 2px 5px; border-radius: 9px; background: var(--surface-3); font-size: 9px; text-align: center; }
.management-grid.list-only { grid-template-columns: 1fr; }
.panel-subtitle { margin: 4px 0 0; color: var(--muted); font-size: 11px; }
.proxy-select { min-width: 220px; max-width: 340px; }
.proxy-select.compact { min-width: 128px; max-width: 170px; }
.credential-dialog-backdrop { z-index: 120; }
.credential-dialog { width: min(680px, calc(100vw - 32px)); max-height: min(760px, calc(100vh - 32px)); overflow: auto; }
.credential-dialog-header { display: flex; align-items: flex-start; justify-content: space-between; gap: 18px; margin-bottom: 18px; }
.credential-dialog-header h2 { margin: 4px 0 3px; }
.credential-dialog-header p { margin: 0; color: var(--muted); font-size: 12px; }
.credential-loading { display: flex; align-items: center; gap: 8px; min-height: 120px; color: var(--muted); }
.credential-error { margin-bottom: 12px; padding: 9px 11px; border: 1px solid rgba(190, 64, 64, .35); border-radius: 5px; color: var(--red); background: rgba(190, 64, 64, .08); font-size: 12px; }
.credential-token-field { display: grid; gap: 7px; margin: 12px 0; }
.credential-token-field > span { display: flex; align-items: center; justify-content: space-between; gap: 8px; color: var(--muted); font-size: 11px; }
.credential-token-field button { display: inline-flex; align-items: center; gap: 5px; padding: 4px 7px; border: 1px solid var(--line); border-radius: 4px; background: var(--surface-2); color: var(--text-2); cursor: pointer; font-size: 10px; }
.credential-token-field button:disabled { opacity: .45; cursor: not-allowed; }
.credential-token-field textarea { width: 100%; resize: vertical; box-sizing: border-box; border: 1px solid var(--line); border-radius: 5px; padding: 9px; background: var(--surface-2); color: var(--text); font: 11px/1.5 ui-monospace, SFMono-Regular, Consolas, monospace; }
.credential-account-id { display: block; margin-top: 14px; color: var(--muted); font-size: 10px; }
.spin { animation: admin-spin .9s linear infinite; }
@keyframes admin-spin { to { transform: rotate(360deg); } }
</style>
