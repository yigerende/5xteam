<script setup>
import { computed, reactive, ref } from 'vue'
import { BadgeCheck, FileJson, FolderOpen, Gauge, RefreshCw, Save, Trash2 } from 'lucide-vue-next'
import { api } from '../api'
import { extractAccessTokens, extractTokensFromFiles, findRefreshToken, formatTime } from '../utils'
import IconButton from './IconButton.vue'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'
import Pagination from './Pagination.vue'

const props = defineProps({ accounts: { type: Array, default: () => [] } })
const emit = defineEmits(['reload'])
const form = reactive({ tokens: '', refreshToken: '' })
const selected = ref(new Set())
const message = reactive({ text: '', type: '' })
const busy = ref(false)
const fileInput = ref(null)
const folderInput = ref(null)
const page = ref(1)
const pageSize = ref(10)
const pages = computed(() => Math.max(1, Math.ceil(props.accounts.length / pageSize.value)))
const pagedAccounts = computed(() => props.accounts.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const allSelected = computed(() => props.accounts.length > 0 && props.accounts.every((item) => selected.value.has(item.id)))

function setMessage(text = '', type = '') { Object.assign(message, { text, type }) }
function inputTokens(showMessage = true) {
  const raw = form.tokens.trim()
  if (!raw) { if (showMessage) setMessage('请先粘贴 Access Token 或 Session JSON', 'error'); return [] }
  if (raw.startsWith('{') || raw.startsWith('[')) {
    try {
      const parsed = JSON.parse(raw)
      const tokens = extractAccessTokens(parsed)
      if (!tokens.length) throw new Error('JSON 中没有找到 accessToken/access_token')
      form.tokens = tokens.join('\n')
      form.refreshToken = findRefreshToken(parsed) || form.refreshToken
      if (showMessage) setMessage(`已解析出 ${tokens.length} 个 Access Token`, 'success')
      return tokens
    } catch (error) { if (showMessage) setMessage(`JSON 解析失败：${error.message}`, 'error'); return [] }
  }
  return [...new Set(raw.split(/\s+/).filter(Boolean))]
}
async function importFile(event) {
  const file = event.target.files?.[0]; event.target.value = ''; if (!file) return
  try { form.tokens = await file.text(); inputTokens() } catch (error) { setMessage(`读取 JSON 文件失败：${error.message}`, 'error') }
}
async function importFolder(event) {
  const result = await extractTokensFromFiles(event.target.files); event.target.value = ''
  if (!result.files.length) return setMessage('所选文件夹中没有 JSON 文件', 'error')
  if (!result.tokens.length) return setMessage('JSON 文件中没有找到 Access Token', 'error')
  form.tokens = result.tokens.join('\n')
  setMessage(`已从 ${result.files.length} 个文件解析出 ${result.tokens.length} 个 Token${result.invalid ? `，跳过 ${result.invalid} 个无效文件` : ''}`, 'success')
}
async function save() {
  const tokens = inputTokens(false)
  if (!tokens.length) return setMessage('没有找到可保存的 Access Token', 'error')
  busy.value = true
  let saved = 0; const errors = []
  for (const token of tokens) {
    try { await api('/api/openai-accounts', { method: 'POST', body: { access_token: token, refresh_token: form.refreshToken.trim() } }); saved++ }
    catch (error) { errors.push(error.message) }
  }
  form.tokens = ''; form.refreshToken = ''; busy.value = false; emit('reload')
  setMessage(`已保存 ${saved} 个账号${errors.length ? `，失败 ${errors.length} 个：${errors[0]}` : ''}`, errors.length ? 'error' : 'success')
}
function toggleAll() {
  selected.value = allSelected.value ? new Set() : new Set(props.accounts.map((item) => item.id))
}
function toggle(id) {
  const next = new Set(selected.value); next.has(id) ? next.delete(id) : next.add(id); selected.value = next
}
function targetIDs() { return selected.value.size ? [...selected.value] : props.accounts.map((item) => item.id) }
async function runOne(account, kind) {
  busy.value = true
  const labels = { check: '检测', refresh: '刷新', quota: '查询额度' }
  setMessage(`正在${labels[kind]} ${account.email || account.label}...`)
  try {
    const result = await api(`/api/openai-accounts/${encodeURIComponent(account.id)}/${kind}`, { method: 'POST', body: {} })
    emit('reload')
    if (kind === 'check') setMessage(result.result.valid ? 'AT 有效' : 'AT 无效', result.result.valid ? 'success' : 'error')
    else if (kind === 'quota') setMessage(`查询完成：剩余 ${result.quota.available_count} 次`, 'success')
    else setMessage('AT 刷新成功', 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}
async function runBatch(kind) {
  const ids = targetIDs(); if (!ids.length) return setMessage('暂无 OpenAI 账号', 'error')
  busy.value = true
  const config = { check: ['检测', '/api/openai-accounts/check/batch'], refresh: ['刷新', '/api/openai-accounts/refresh/batch'], quota: ['查询', '/api/openai-accounts/quota/batch'] }[kind]
  setMessage(`正在批量${config[0]} ${ids.length} 个账号...`)
  try {
    const result = await api(config[1], { method: 'POST', body: { ids } })
    emit('reload')
    const failed = Object.keys(result.errors || {}).length
    setMessage(`批量${config[0]}完成：成功 ${result.items.length} 个${failed ? `，失败 ${failed} 个` : ''}`, failed ? 'error' : 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}
async function remove(account) {
  if (!window.confirm(`确认删除 OpenAI 账号“${account.label}”？`)) return
  try { await api(`/api/openai-accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' }); selected.value.delete(account.id); emit('reload'); setMessage(`${account.label} 已删除`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
}
</script>

<template>
  <section class="view-stack">
    <header class="page-heading"><div><span class="overline">ACCOUNT OPERATIONS</span><h1>OpenAI 账号</h1><p>集中检测凭据状态、刷新令牌并查询可用次数</p></div><StatusPill tone="success">{{ accounts.length }} 个账号</StatusPill></header>
    <section class="panel account-import">
      <div class="panel-title"><div><span>IMPORT</span><h2>导入账号凭据</h2></div><span class="muted-count">支持单条、批量与 JSON 文件夹</span></div>
      <form class="import-grid" @submit.prevent="save"><label class="field grow"><span>Access Token / Session JSON</span><textarea v-model="form.tokens" rows="5" spellcheck="false" placeholder="每行一个 Token，或粘贴 Session JSON"></textarea></label><div class="import-side"><label class="field"><span>Refresh Token <small>可选</small></span><input v-model="form.refreshToken" type="password" autocomplete="off" /></label><input ref="fileInput" hidden type="file" accept=".json,application/json" @change="importFile" /><input ref="folderInput" hidden type="file" accept=".json,application/json" webkitdirectory directory multiple @change="importFolder" /><div class="compact-actions"><button class="btn ghost" type="button" @click="fileInput.click()"><FileJson :size="15" />JSON</button><button class="btn ghost" type="button" @click="folderInput.click()"><FolderOpen :size="15" />文件夹</button><button class="btn ghost" type="button" @click="inputTokens()"><BadgeCheck :size="15" />解析</button></div><button class="btn primary fill" type="submit" :disabled="busy"><Save :size="15" />保存账号</button></div></form>
      <MessageBar :message="message" />
    </section>
    <section class="panel list-panel">
      <div class="panel-title responsive"><div><span>ACCOUNTS</span><h2>账号状态</h2></div><div class="heading-actions"><button class="btn ghost" type="button" :disabled="busy" @click="runBatch('check')"><BadgeCheck :size="15" />批量检测</button><button class="btn ghost" type="button" :disabled="busy" @click="runBatch('refresh')"><RefreshCw :size="15" />批量刷新</button><button class="btn primary" type="button" :disabled="busy" @click="runBatch('quota')"><Gauge :size="15" />批量查询</button></div></div>
      <div class="table-shell"><table><thead><tr><th class="check-column"><input type="checkbox" :checked="allSelected" aria-label="全选账号" @change="toggleAll" /></th><th>账号</th><th>计划</th><th>剩余次数</th><th>AT 状态</th><th>到期时间</th><th class="actions-column">操作</th></tr></thead><tbody>
        <tr v-if="!accounts.length"><td colspan="7" class="empty-cell">暂无 OpenAI 账号</td></tr>
        <tr v-for="account in pagedAccounts" :key="account.id"><td><input type="checkbox" :checked="selected.has(account.id)" :aria-label="`选择 ${account.label}`" @change="toggle(account.id)" /></td><td class="account-cell"><strong>{{ account.label }}</strong><small>{{ account.email }}</small></td><td>{{ account.plan_type || '-' }}</td><td><StatusPill :tone="account.reset_credits == null ? 'pending' : 'success'">{{ account.reset_credits == null ? '未查询' : `${account.reset_credits} 次` }}</StatusPill><small v-if="account.reset_credits_fetched_at" class="table-note">{{ formatTime(account.reset_credits_fetched_at) }}</small></td><td><StatusPill :tone="!account.last_checked_at ? 'pending' : account.last_check_valid ? 'success' : 'danger'">{{ !account.last_checked_at ? '未检测' : account.last_check_valid ? '有效' : '无效' }}</StatusPill><small v-if="account.last_check_message" class="table-note">{{ account.last_check_message }}</small></td><td>{{ account.access_token_expires_at ? formatTime(account.access_token_expires_at) : '-' }}</td><td><div class="row-actions"><IconButton label="检测 AT" :disabled="busy" @click="runOne(account, 'check')"><BadgeCheck :size="15" /></IconButton><IconButton label="刷新 AT" :disabled="busy || !account.refresh_token_present" @click="runOne(account, 'refresh')"><RefreshCw :size="15" /></IconButton><IconButton label="查询次数" :disabled="busy" @click="runOne(account, 'quota')"><Gauge :size="15" /></IconButton><IconButton label="删除账号" danger @click="remove(account)"><Trash2 :size="15" /></IconButton></div></td></tr>
      </tbody></table></div><Pagination :page="page" :page-size="pageSize" :total="accounts.length" @update:page="page = $event" @update:page-size="pageSize = $event" />
    </section>
  </section>
</template>
