<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import {
  BadgeCheck, Cable, DoorOpen, Download, FileJson, FolderOpen, Gauge, KeyRound, Link2,
  History, LoaderCircle, MoreHorizontal, RefreshCw, Save, Send, Trash2, Unplug, Upload,
  Waypoints,
} from 'lucide-vue-next'
import { api, downloadFile } from '../api'
import { extractAccessTokens, extractTokensFromFiles, formatTime, shortID } from '../utils'
import { latestOAuthLoginSummary, oauthLoginSummary } from '../oauthLoginLog'
import IconButton from './IconButton.vue'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'
import Pagination from './Pagination.vue'
import AutoRotationView from './AutoRotationView.vue'
import ExecutionHistoryView from './ExecutionHistoryView.vue'

const props = defineProps({
  accounts: { type: Array, default: () => [] },
  adminAccounts: { type: Array, default: () => [] },
  entryEmail: { type: String, default: '' },
  active: { type: Boolean, default: false },
  defaultPageSize: { type: Number, default: 10 },
})
const emit = defineEmits(['reload', 'auto-rotation'])

const importForm = reactive({ tokens: '' })
const sub2Form = reactive({ url: '', email: '', password: '', groupIDs: [], groupNames: [], models: '', accountConcurrency: 10, priority: 1, cpaWs: false, enable401Check: true, statusCheckIntervalSeconds: 120, reloginFailureLimit: 2, quotaEnabled: true, quotaCheckIntervalSeconds: 120, quotaRemainingThresholdPercent: 0, passwordPresent: false })
const cpaForm = reactive({ url: '', key: '', keyPresent: false, websockets: false, enable401Check: true, statusCheckIntervalSeconds: 120, reloginFailureLimit: 2, quotaEnabled: true, quotaCheckIntervalSeconds: 120, quotaRemainingThresholdPercent: 0, groupIDs: [], groupNames: [] })
const cpaGroups = ref([])
const pushProvider = ref('sub2')
const activeProviderLabel = computed(() => pushProvider.value === 'cpa' ? 'CPA' : 'Sub2')
const activeReloginFailureLimit = computed(() => pushProvider.value === 'cpa' ? cpaForm.reloginFailureLimit : sub2Form.reloginFailureLimit)
const joinForm = reactive({ account: null, adminAccountID: '', seatType: 'default' })
const manualPushForm = reactive({ account: null, sub2AccountID: '' })
const groups = ref([])
const message = reactive({ text: '', type: '' })
const busy = ref('')
const liveAccounts = ref([])
const activities = reactive({})
const clock = ref(Date.now())
const joinOpen = ref(false)
const fileInput = ref(null)
const folderInput = ref(null)
const teamMenu = ref('accounts')
const pipelineMenu = reactive({ account: null, top: 0, left: 0, open: false })
let pipelineMenuCloseTimer
const capacityCacheKey = 'space-console-admin-capacities-v1'
function readCapacityCache() {
  try {
    const cached = JSON.parse(window.localStorage.getItem(capacityCacheKey) || '{}')
    const entries = cached?.entries && typeof cached.entries === 'object' ? Object.entries(cached.entries).map(([key, value]) => [
      /^\d+$/.test(key) ? Number(key) : key,
      value,
    ]) : []
    return { map: new Map(entries), fetchedAt: Number(cached.fetchedAt) || 0 }
  } catch {
    return { map: new Map(), fetchedAt: 0 }
  }
}
const initialCapacityCache = readCapacityCache()
const adminCapacities = ref(initialCapacityCache.map)
const snapshotCapacities = ref(new Map())
const adminCapacityLoading = ref(new Set())
const capacityRefreshing = ref(false)
const lastCapacityFetchAt = ref(initialCapacityCache.fetchedAt)
const joinCapacityRefreshing = ref(false)
const page = ref(1)
const pageSize = ref(props.defaultPageSize)
const accountTotal = ref(0)
const accountSummary = reactive({ all: 0, outside: 0, inside: 0, removed: 0, oauth_ready: 0, invite_pending: 0, monitoring: 0, inside_premium: 0, quota_7d_remaining_total: 0, quota_7d_count: 0, oldest_status_checked_at: '', oldest_quota_checked_at: '', pending_seats_by_admin: {} })
const teamSpaceFilter = ref('')
const selectedAccountIDs = ref(new Set())
const lifecycleView = reactive({ account: null, events: [], task: null, loading: false, error: '' })
const lifecycleLoginSummary = computed(() => latestOAuthLoginSummary(lifecycleView.events))

function teamSpaceStatus(account) {
  if (account?.remove_status === 'completed') return 'removed'
  if (account?.accept_status === 'completed') return 'inside'
  return 'outside'
}
function isPremiumSeatType(seatType) {
  return ['prolite', 'premium', '5x'].includes(String(seatType || '').trim().toLowerCase())
}
const allDisplayedAccounts = computed(() => liveAccounts.value)
const displayedAccounts = computed(() => liveAccounts.value)
const selectedPipelineAccounts = computed(() => liveAccounts.value.filter((item) => selectedAccountIDs.value.has(String(item.id))))
const allDisplayedSelected = computed(() => displayedAccounts.value.length > 0 && displayedAccounts.value.every((item) => selectedAccountIDs.value.has(String(item.id))))
watch(() => liveAccounts.value.map((item) => String(item.id)).join(','), () => {
  const available = new Set(liveAccounts.value.map((item) => String(item.id)))
  selectedAccountIDs.value = new Set([...selectedAccountIDs.value].filter((id) => available.has(id)))
})
const activeActivities = computed(() => Object.values(activities))
const joinedCount = computed(() => Number(accountSummary.inside || 0))
const invitePendingCount = computed(() => Number(accountSummary.invite_pending || 0))
const monitoringCount = computed(() => Number(accountSummary.monitoring || 0))
const removedCount = computed(() => Number(accountSummary.removed || 0))
const premiumSeatSummary = computed(() => {
  let total = 0
  for (const value of snapshotCapacities.value.values()) {
    total += Number(value?.premium?.total || 0)
  }
  // The total comes from the cached Team capacity snapshot, while the
  // remaining count must follow the current pipeline state immediately.
  const insidePremium = Number(accountSummary.inside_premium || 0)
  const remaining = Math.max(0, total - insidePremium)
  return { total, remaining }
})
const average7DRemaining = computed(() => {
  if (!Number(accountSummary.inside || 0)) return '0%'
  const seatTotal = premiumSeatSummary.value.total
  if (!seatTotal) return '未查询'
  if (!Number(accountSummary.quota_7d_count || 0)) return '未查询'
  const remainingTotal = Number(accountSummary.quota_7d_remaining_total || 0)
  const average = remainingTotal / seatTotal
  return `${average.toFixed(average % 1 ? 1 : 0)}%`
})
function nextCheckSeconds(timestampKey, intervalSeconds, enabled = true) {
  if (!enabled) return null
  const interval = Math.max(10, Number(intervalSeconds) || 120)
  if (!Number(accountSummary.monitoring || 0)) return null
  const summaryKey = timestampKey === 'status_checked_at' ? 'oldest_status_checked_at' : 'oldest_quota_checked_at'
  const value = accountSummary[summaryKey]
  if (!value) return 0
  const checkedAt = new Date(value).getTime()
  if (!Number.isFinite(checkedAt)) return 0
  return Math.max(0, Math.ceil(interval - (clock.value - checkedAt) / 1000))
}
const statusCountdown = computed(() => nextCheckSeconds('status_checked_at', pushProvider.value === 'cpa' ? cpaForm.statusCheckIntervalSeconds : sub2Form.statusCheckIntervalSeconds, pushProvider.value === 'cpa' ? cpaForm.enable401Check : sub2Form.enable401Check))
const activeQuotaEnabled = computed(() => pushProvider.value === 'cpa' ? cpaForm.quotaEnabled : sub2Form.quotaEnabled)
const quotaCountdown = computed(() => nextCheckSeconds('quota_checked_at', pushProvider.value === 'cpa' ? cpaForm.quotaCheckIntervalSeconds : sub2Form.quotaCheckIntervalSeconds, activeQuotaEnabled.value))
function countdownText(value, disabled = false) {
  if (disabled) return '已关闭'
  if (value == null) return '暂无账号'
  return `${value}s`
}

const stageLabels = { invite: '邀请', accept: '进入', oauth: '授权', push: '推送', quota: '额度', remove: '移出' }
const stateLabels = { not_started: '未开始', pending: '待处理', running: '处理中', completed: '已完成', failed: '失败' }
const operationMeta = {
  join: { label: '邀请并进入空间', stage: 'invite' },
  oauth: { label: '获取 Codex AT / RT', stage: 'oauth' },
  relogin: { label: '重登并重新推送', stage: 'oauth' },
  push: { label: '推送到当前下游', stage: 'push' },
  quota: { label: '刷新额度', stage: 'quota' },
  remove: { label: '移出空间', stage: 'remove' },
}
function costText(account) {
  if (String(account?.push_provider || '').toLowerCase() === 'cpa') return '—'
  if (!account?.cost_checked_at) return '未查询'
  return `$${Number(account?.total_cost_usd || 0).toFixed(4)}`
}
function userCostText(account) {
  if (account?.total_user_cost_usd == null) return '用户 --'
  return `用户 $${Number(account.total_user_cost_usd).toFixed(4)}`
}
function removeMethodText(account) {
  if (account?.remove_method === 'child_leave') return '实际：子号自己退出'
  if (account?.remove_method === 'mother_kick') return '实际：母号踢出'
  return '尚未执行'
}
// A record may retain a legacy identifier from the other downstream after a
// provider switch.  All actions must follow only the currently enabled line.
const hasDownstream = (account) => pushProvider.value === 'cpa'
  ? !!account?.cpa_auth_file_name
  : Number(account?.sub2_account_id) > 0

async function loadAdminCapacity(account) {
  if (!account?.id) return
  const loading = new Set(adminCapacityLoading.value)
  loading.add(account.id)
  adminCapacityLoading.value = loading
  try {
    const result = await api(`/api/admin-accounts/${encodeURIComponent(account.id)}/capacity`, { cache: 'no-store' })
    const next = new Map(adminCapacities.value)
    next.set(account.id, result)
    adminCapacities.value = next
  } catch (error) {
    const next = new Map(adminCapacities.value)
    next.set(account.id, { error: error.message })
    adminCapacities.value = next
  } finally {
    const next = new Set(adminCapacityLoading.value)
    next.delete(account.id)
    adminCapacityLoading.value = next
  }
}

async function loadCapacitySnapshots() {
  const data = await api('/api/admin-capacity-snapshots', { cache: 'no-store' })
  const entries = Object.entries(data || {})
  const next = new Map(entries.map(([id, value]) => [/^\d+$/.test(id) ? Number(id) : id, value]))
  snapshotCapacities.value = next
  adminCapacities.value = new Map(next)
  const fetchedAt = entries.reduce((latest, [, value]) => {
    const timestamp = new Date(value?.fetched_at || 0).getTime()
    return Number.isFinite(timestamp) && timestamp > latest ? timestamp : latest
  }, 0)
  if (fetchedAt) lastCapacityFetchAt.value = fetchedAt
}

async function loadAdminCapacities({ force = false } = {}) {
  if (!props.active || teamMenu.value !== 'accounts') return
  const now = Date.now()
  if (!force && lastCapacityFetchAt.value && now - lastCapacityFetchAt.value < 30 * 60 * 1000) return
  if (capacityRefreshing.value) return
  capacityRefreshing.value = true
  try {
    // Keep local capacity data aligned with the current mother list. This
    // also removes entries left by a deleted mother from browser storage.
    const currentIDs = new Set(props.adminAccounts.map((account) => account.id))
    adminCapacities.value = new Map([...adminCapacities.value.entries()].filter(([id]) => currentIDs.has(id)))
    await Promise.all(props.adminAccounts.map(loadAdminCapacity))
    lastCapacityFetchAt.value = Date.now()
    const entries = Object.fromEntries(adminCapacities.value.entries())
    window.localStorage.setItem(capacityCacheKey, JSON.stringify({ entries, fetchedAt: lastCapacityFetchAt.value }))
    await loadCapacitySnapshots()
  } finally {
    capacityRefreshing.value = false
  }
}

let capacityRefreshTimer
function startCapacityRefreshTimer() {
  window.clearInterval(capacityRefreshTimer)
  // Refresh the upstream capacity every 30 minutes, persist the result, and
  // let the card continue to render from the saved snapshot.
  capacityRefreshTimer = window.setInterval(() => loadAdminCapacities().catch(() => {}), 30 * 60 * 1000)
}
function stopCapacityRefreshTimer() {
  window.clearInterval(capacityRefreshTimer)
  capacityRefreshTimer = undefined
}
watch(() => props.adminAccounts.map((account) => account.id).join(','), () => {
  if (props.active && teamMenu.value === 'accounts') loadCapacitySnapshots().catch(() => {})
})
watch([teamMenu, () => props.active], ([value, active]) => {
  if (active && value === 'accounts') {
    loadCapacitySnapshots().catch(() => {})
    startCapacityRefreshTimer()
  } else {
    stopCapacityRefreshTimer()
  }
}, { immediate: true })

// An invitation reserves a seat before the invited account finishes the
// second (accept/enter-space) step.  Depending on the upstream response, that
// pending invitation may not be reflected in `available` immediately.  Keep
// a local view of those reservations so the invitation picker never advertises
// a seat that is already in use by this pipeline.
const pendingSeatUsage = computed(() => {
  const usage = new Map()
  for (const [adminID, counts] of Object.entries(accountSummary.pending_seats_by_admin || {})) {
    usage.set(adminID, { standard: Number(counts?.standard || 0), premium: Number(counts?.premium || 0) })
  }
  return usage
})

function adjustedAdminCapacity(admin) {
  const capacity = snapshotCapacities.value.get(admin.id) || adminCapacities.value.get(admin.id)
  if (!capacity || capacity.error) return capacity
  return capacity
}

function adminOptionLabel(admin) {
  const capacity = adjustedAdminCapacity(admin)
  if (adminCapacityLoading.value.has(admin.id)) return `${admin.label} · ${admin.email}（席位读取中…）`
  if (capacity?.error) return `${admin.label} · ${admin.email}（席位读取失败）`
  const standard = capacity?.standard?.remaining
  const premium = capacity?.premium?.remaining
  if (standard == null && premium == null) return `${admin.label} · ${admin.email}`
  return `${admin.label} · ${admin.email}（普通剩余 ${Number(standard || 0)}，高级 5x 剩余 ${Number(premium || 0)}）`
}

watch(() => props.entryEmail, (value) => {
  if (value) {
    teamSpaceFilter.value = ''
    page.value = 1
    refreshLiveAccounts().catch(() => {})
    setMessage(`${value} 已从邮件管理进入流水线，可继续邀请并进入空间`, 'success')
  }
}, { immediate: true })

function setMessage(text = '', type = '') { Object.assign(message, { text, type }) }
function stageTone(status) { return status === 'completed' ? 'success' : status === 'running' ? 'running' : status === 'failed' ? 'danger' : 'pending' }
function activityFor(account) { return activities[account.id] }
function isAccountBusy(account) { return busy.value === account.id || !!activityFor(account) }
function isPipelineSelected(account) { return selectedAccountIDs.value.has(String(account.id)) }
function togglePipelineSelected(account) {
  const id = String(account.id)
  const next = new Set(selectedAccountIDs.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  selectedAccountIDs.value = next
}
function toggleAllDisplayed() {
  const next = new Set(selectedAccountIDs.value)
  if (allDisplayedSelected.value) displayedAccounts.value.forEach((item) => next.delete(String(item.id)))
  else displayedAccounts.value.forEach((item) => next.add(String(item.id)))
  selectedAccountIDs.value = next
}
function elapsedSeconds(activity) { return Math.max(0, Math.floor((clock.value - activity.startedAt) / 1000)) }
function activityText(activity) {
  if (activity.detail) return activity.detail
  if (activity.action === 'join' && activity.stage === 'accept') return '正在接受团队邀请'
  if (activity.action === 'join') return '正在发送团队邀请'
  const label = activity.action === 'push' ? `推送到${activeProviderLabel.value}` : operationMeta[activity.action]?.label
  return `正在${label || '执行操作'}`
}
function wait(milliseconds) { return new Promise((resolve) => window.setTimeout(resolve, milliseconds)) }
function latestJobMessage(job) {
  return [...(job?.logs || [])].reverse().find((item) => item?.message)?.message || '正在建立 Codex OAuth 授权会话'
}
function visibleStageStatus(account, key) {
  return activityFor(account)?.stage === key ? 'running' : account[`${key}_status`]
}
function syncActivityStage(account) {
  const activity = activityFor(account)
  if (!activity) return
  const previousStage = activity.stage
  if (activity.action === 'join') {
    if (account.accept_status === 'running') activity.stage = 'accept'
    else if (account.invite_status === 'running') activity.stage = 'invite'
  }
  if (activity.stage !== previousStage) setMessage(`${account.email}：${activityText(activity)}`)
}
async function refreshLiveAccounts() {
  const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize.value), space_state: teamSpaceFilter.value })
  const data = await api(`/api/free-accounts?${params}`)
  liveAccounts.value = data.items || []
  accountTotal.value = Number(data.total || 0)
  Object.assign(accountSummary, data.summary || {})
  const lastPage = Math.max(1, Math.ceil(accountTotal.value / pageSize.value))
  if (page.value > lastPage) {
    page.value = lastPage
    return refreshLiveAccounts()
  }
  liveAccounts.value.forEach(syncActivityStage)
  return liveAccounts.value
}
function setAccountPage(value) { page.value = value; selectedAccountIDs.value = new Set(); refreshLiveAccounts() }
function setAccountPageSize(value) { pageSize.value = value; page.value = 1; selectedAccountIDs.value = new Set(); refreshLiveAccounts() }
function setTeamSpaceFilter(value) { teamSpaceFilter.value = teamSpaceFilter.value === value ? '' : value; page.value = 1; selectedAccountIDs.value = new Set(); refreshLiveAccounts() }
async function openLifecycle(account) {
  lifecycleView.account = account
  lifecycleView.events = []
  lifecycleView.task = null
  lifecycleView.error = ''
  lifecycleView.loading = true
  try {
    const result = await api(`/api/free-accounts/${encodeURIComponent(account.id)}/events`)
    lifecycleView.account = result.account || account
    lifecycleView.task = result.lifecycle_task || null
    lifecycleView.events = result.events || []
  } catch (error) {
    lifecycleView.error = error.message
  } finally {
    lifecycleView.loading = false
  }
}
async function exportAccountLogs(account = lifecycleView.account) {
  if (!account?.id) return
  try {
    await downloadFile(`/api/free-accounts/${encodeURIComponent(account.id)}/events/export`, `account-${account.email || account.id}-logs.json`)
    setMessage(`${account.email}：账号日志已导出`, 'success')
  } catch (error) {
    setMessage(`${account.email}：日志导出失败：${error.message}`, 'error')
  }
}
async function runTracked(account, action, request) {
  const meta = operationMeta[action]
  const initialStage = action === 'join' && account.invite_status === 'completed' ? 'accept' : meta.stage
  activities[account.id] = { id: account.id, email: account.email, action, stage: initialStage, startedAt: Date.now() }
  setMessage(`${account.email}：${activityText(activities[account.id])}`)
  const pollTimer = window.setInterval(() => refreshLiveAccounts().catch(() => {}), 800)
  try {
    const result = await request()
    await refreshLiveAccounts().catch(() => {})
    return result
  } finally {
    window.clearInterval(pollTimer)
    delete activities[account.id]
    emit('reload')
  }
}
function tokenList(showMessage = true) {
  const raw = importForm.tokens.trim()
  if (!raw) { if (showMessage) setMessage('请先输入 Free 账号 Access Token', 'error'); return [] }
  if (raw.startsWith('{') || raw.startsWith('[')) {
    try {
      const tokens = extractAccessTokens(JSON.parse(raw))
      if (!tokens.length) throw new Error('JSON 中没有找到 Access Token')
      importForm.tokens = tokens.join('\n')
      if (showMessage) setMessage(`已解析 ${tokens.length} 个账号`, 'success')
      return tokens
    } catch (error) { if (showMessage) setMessage(`JSON 解析失败：${error.message}`, 'error'); return [] }
  }
  return [...new Set(raw.split(/\s+/).filter(Boolean))]
}
async function importFile(event) {
  const file = event.target.files?.[0]; event.target.value = ''; if (!file) return
  try { importForm.tokens = await file.text(); tokenList() } catch (error) { setMessage(`读取文件失败：${error.message}`, 'error') }
}
async function importFolder(event) {
  const result = await extractTokensFromFiles(event.target.files); event.target.value = ''
  if (!result.tokens.length) return setMessage('文件夹中没有找到 Access Token', 'error')
  importForm.tokens = result.tokens.join('\n')
  setMessage(`已从 ${result.files.length} 个文件解析 ${result.tokens.length} 个账号`, 'success')
}
async function importAccounts() {
  const accessTokens = tokenList(false)
  if (!accessTokens.length) return setMessage('没有可导入的 Access Token', 'error')
  busy.value = 'import'
  try {
    const result = await api('/api/free-accounts/import', { method: 'POST', body: { access_tokens: accessTokens } })
    importForm.tokens = ''
    emit('reload')
    const failed = Object.keys(result.errors || {}).length
    setMessage(`导入完成：新增 ${result.created}，更新 ${result.updated}${failed ? `，失败 ${failed}` : ''}`, failed ? 'error' : 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}

async function loadSub2() {
  try {
    const settings = await api('/api/sub2-settings')
    const groupIDs = settings.group_ids?.length ? settings.group_ids : settings.group_id ? [settings.group_id] : []
    const groupNames = settings.group_names?.length ? settings.group_names : settings.group_name ? [settings.group_name] : []
    Object.assign(sub2Form, {
      url: settings.url || '', email: settings.email || '', password: '', groupIDs, groupNames,
      models: (settings.models || []).join('\n'), accountConcurrency: settings.account_concurrency || 10, priority: settings.priority || 1,
      cpaWs: settings.cpa_ws === true || Number(settings.cpa_ws) === 1,
      enable401Check: settings.enable_401_check !== false,
      statusCheckIntervalSeconds: settings.status_check_interval_seconds || settings.quota_check_interval_seconds || 120,
      reloginFailureLimit: settings.relogin_failure_limit || 2,
      quotaEnabled: settings.quota_enabled !== false,
      quotaCheckIntervalSeconds: settings.quota_check_interval_seconds || 120,
      quotaRemainingThresholdPercent: Number(settings.quota_remaining_threshold_percent ?? 0),
      passwordPresent: !!settings.password_present,
    })
    if (pushProvider.value === 'sub2' && settings.url && settings.password_present) await testSub2(false)
  } catch (error) { setMessage(error.message, 'error') }
}
async function loadPushSettings() {
  try {
    const result = await api('/api/push-settings')
    pushProvider.value = result.provider === 'cpa' ? 'cpa' : 'sub2'
    const s = result.sub2 || {}; Object.assign(sub2Form, { url: s.url || '', email: s.email || '', groupIDs: s.group_ids || [], groupNames: s.group_names || [], models: (s.models || []).join('\n'), accountConcurrency: s.account_concurrency || 10, priority: s.priority || 1, cpaWs: s.cpa_ws === true || Number(s.cpa_ws) === 1, enable401Check: s.enable_401_check !== false, statusCheckIntervalSeconds: s.status_check_interval_seconds || 120, reloginFailureLimit: s.relogin_failure_limit || 2, quotaEnabled: s.quota_enabled !== false, quotaCheckIntervalSeconds: s.quota_check_interval_seconds || 120, quotaRemainingThresholdPercent: Number(s.quota_remaining_threshold_percent ?? 0), passwordPresent: !!s.password_present })
    const c = result.cpa || {}; Object.assign(cpaForm, { url: c.url || '', keyPresent: !!c.key_present, websockets: !!c.websockets, enable401Check: c.enable_401_check !== false, statusCheckIntervalSeconds: c.status_check_interval_seconds || 120, reloginFailureLimit: c.relogin_failure_limit || 2, quotaEnabled: c.quota_enabled !== false, quotaCheckIntervalSeconds: c.quota_check_interval_seconds || 120, quotaRemainingThresholdPercent: Number(c.quota_remaining_threshold_percent ?? 0), groupIDs: c.group_ids || [], groupNames: c.group_names || [] })
    if (cpaForm.url && cpaForm.keyPresent) await loadCPAGroups()
  } catch (error) { setMessage(error.message, 'error') }
}
async function savePushSettings() {
  busy.value = 'push-settings'
  try {
    const payload = { provider: pushProvider.value, sub2: { url: sub2Form.url.trim(), email: sub2Form.email.trim(), password: sub2Form.password, group_ids: sub2Form.groupIDs.map(Number), group_names: selectedGroupNames(), models: selectedModels(), account_concurrency: Number(sub2Form.accountConcurrency) || 10, priority: Number(sub2Form.priority) || 1, cpa_ws: !!sub2Form.cpaWs, enable_401_check: !!sub2Form.enable401Check, status_check_interval_seconds: Number(sub2Form.statusCheckIntervalSeconds) || 120, relogin_failure_limit: Number(sub2Form.reloginFailureLimit) || 2, quota_enabled: !!sub2Form.quotaEnabled, quota_check_interval_seconds: Number(sub2Form.quotaCheckIntervalSeconds) || 120, quota_remaining_threshold_percent: Number(sub2Form.quotaRemainingThresholdPercent) }, cpa: { url: cpaForm.url.trim(), key: cpaForm.key, websockets: !!cpaForm.websockets, enable_401_check: !!cpaForm.enable401Check, status_check_interval_seconds: Number(cpaForm.statusCheckIntervalSeconds) || 120, relogin_failure_limit: Number(cpaForm.reloginFailureLimit) || 2, quota_enabled: !!cpaForm.quotaEnabled, quota_check_interval_seconds: Number(cpaForm.quotaCheckIntervalSeconds) || 120, quota_remaining_threshold_percent: Number(cpaForm.quotaRemainingThresholdPercent), group_ids: cpaForm.groupIDs.map(Number), group_names: selectedCPAGroupNames() } }
    const result = await api('/api/push-settings', { method: 'PUT', body: payload }); cpaForm.key = ''; cpaForm.keyPresent = !!result.cpa?.key_present; sub2Form.password = ''; sub2Form.passwordPresent = !!result.sub2?.password_present; setMessage(`推送设置已保存，当前使用${pushProvider.value === 'cpa' ? ' CPA' : ' Sub2'}`, 'success')
  } catch (error) { setMessage(error.message, 'error') } finally { busy.value = '' }
}
async function testCPA() { busy.value = 'cpa-test'; try { await api('/api/push-settings/cpa/test', { method: 'POST', body: {} }); await loadCPAGroups(); setMessage('CPA 已连接', 'success') } catch (error) { setMessage(error.message, 'error') } finally { busy.value = '' } }
async function loadCPAGroups() { try { const result = await api('/api/push-settings/cpa/groups'); cpaGroups.value = result || [] } catch (error) { setMessage(error.message, 'error') } }
function selectedCPAGroupNames() { const loaded = new Map(cpaGroups.value.map((group) => [Number(group.id), group.name])); return cpaForm.groupIDs.map((id, index) => loaded.get(Number(id)) || cpaForm.groupNames[index] || `#${id}`) }
function selectedGroupNames() {
  const loaded = new Map(groups.value.map((group) => [Number(group.id), group.name]))
  return sub2Form.groupIDs.map((id, index) => loaded.get(Number(id)) || sub2Form.groupNames[index] || `#${id}`)
}
function selectedModels() {
  return [...new Set(sub2Form.models.split(/[\n,]+/).map((item) => item.trim()).filter(Boolean))]
}
async function saveSub2(showMessage = true) {
  const groupIDs = [...new Set(sub2Form.groupIDs.map(Number).filter((id) => id > 0))]
  const saved = await api('/api/sub2-settings', {
    method: 'PUT', body: {
      url: sub2Form.url.trim(), email: sub2Form.email.trim(), password: sub2Form.password,
      group_ids: groupIDs, group_names: selectedGroupNames(), models: selectedModels(),
      account_concurrency: Number(sub2Form.accountConcurrency) || 10, priority: Number(sub2Form.priority) || 1,
      cpa_ws: !!sub2Form.cpaWs,
      enable_401_check: !!sub2Form.enable401Check,
      status_check_interval_seconds: Number(sub2Form.statusCheckIntervalSeconds) || 120,
      relogin_failure_limit: Number(sub2Form.reloginFailureLimit) || 2,
      quota_enabled: !!sub2Form.quotaEnabled,
      quota_check_interval_seconds: Number(sub2Form.quotaCheckIntervalSeconds) || 120,
      quota_remaining_threshold_percent: Number(sub2Form.quotaRemainingThresholdPercent),
    },
  })
  sub2Form.password = ''; sub2Form.passwordPresent = saved.password_present
  sub2Form.groupIDs = saved.group_ids || groupIDs
  sub2Form.groupNames = saved.group_names || selectedGroupNames()
  sub2Form.models = (saved.models || selectedModels()).join('\n')
  if (showMessage) setMessage('Sub2 配置已保存', 'success')
  return saved
}
async function connectSub2() {
  busy.value = 'sub2'
  try { await saveSub2(false); await testSub2(true) }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function testSub2(showMessage = true) {
  const result = await api('/api/sub2-settings/test', { method: 'POST', body: {} })
  groups.value = result.groups || []
  const available = new Set(groups.value.map((group) => Number(group.id)))
  sub2Form.groupIDs = sub2Form.groupIDs.map(Number).filter((id) => available.has(id))
  sub2Form.groupNames = selectedGroupNames()
  if (showMessage) setMessage(`Sub2 已连接，读取到 ${groups.value.length} 个 OpenAI 分组`, 'success')
}
async function persistSub2() {
  busy.value = 'sub2'
  try { await saveSub2(true) } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}

async function saveStageValue(account, stage, status) {
  if (stage === 'push' && status === 'completed' && !account.sub2_account_id) {
    manualPushForm.account = account
    manualPushForm.sub2AccountID = ''
    return
  }
  await persistStageValue(account, stage, status, account.sub2_account_id || 0)
}
async function saveManualPushStage() {
  const account = manualPushForm.account
  const sub2AccountID = Number(manualPushForm.sub2AccountID)
  if (!account || !Number.isInteger(sub2AccountID) || sub2AccountID < 1) return setMessage('请填写有效的 Sub2 账号 ID', 'error')
  await persistStageValue(account, 'push', 'completed', sub2AccountID)
  manualPushForm.account = null
}
async function persistStageValue(account, stage, status, sub2AccountID = 0) {
  busy.value = account.id
  try {
    const updated = await api(`/api/free-accounts/${encodeURIComponent(account.id)}/stage`, {
      method: 'PUT', body: { stage, status, sub2_account_id: sub2AccountID },
    })
    liveAccounts.value = liveAccounts.value.map((item) => item.id === updated.id ? updated : item)
    emit('reload')
    setMessage(`${account.email}：${stageLabels[stage]}已手动标记为${stateLabels[status]}`, status === 'failed' ? 'error' : 'success')
  } catch (error) { await refreshLiveAccounts().catch(() => {}); setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
function joinActionLabel(account) {
  if (account?.accept_status === 'completed') return '更新团队关联'
  return account?.invite_status === 'completed' ? '接受团队邀请' : '邀请并进入空间'
}
function linkedAdmin(account) {
  return props.adminAccounts.find((item) => item.id === account.admin_account_id)
}
function adminSpaceName(account) {
  const admin = linkedAdmin(account)
  const label = admin?.label || ''
  const email = account.admin_email || admin?.email || ''
  if (label && email && label !== email) return `${label} · ${email}`
  return label || email || '未关联'
}
function adminSpaceID(account) {
  return account.team_account_id || linkedAdmin(account)?.team_account_id || ''
}

async function openJoin(account) {
  // Invitation selection uses the same persisted snapshot and local usage
  // calculation as automatic rotation; opening the picker must not call
  // OpenAI for every mother account.
  await Promise.all([refreshLiveAccounts().catch(() => {}), loadCapacitySnapshots().catch(() => {})])
  joinForm.account = account
  joinForm.adminAccountID = account.admin_account_id || props.adminAccounts[0]?.id || ''
  joinForm.seatType = account.seat_type || 'default'
  joinOpen.value = true
}
async function joinAccount() {
  if (!joinForm.adminAccountID) return setMessage('请选择邀请母号', 'error')
  const account = joinForm.account
  const associationOnly = account.accept_status === 'completed'
  const id = account.id
  const payload = { admin_account_id: joinForm.adminAccountID, seat_type: joinForm.seatType }
  joinOpen.value = false
  try {
    await runTracked(account, 'join', () => api(`/api/free-accounts/${encodeURIComponent(id)}/join`, { method: 'POST', body: payload }))
    setMessage(associationOnly ? `${account.email} 的团队关联已更新` : `${account.email} 已进入空间`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
}
async function acquireOAuth(account) {
  if (account.accept_status !== 'completed') return setMessage('请先完成邀请并进入空间', 'error')
  try {
    await runTracked(account, 'oauth', async () => {
      const started = await api(`/api/free-accounts/${encodeURIComponent(account.id)}/oauth/start`, { method: 'POST', body: {} })
      let job = started.job || started
      if (!job.job_id) throw new Error('Codex OAuth 任务没有返回任务 ID')
      activities[account.id].detail = latestJobMessage(job)
      while (!['success', 'failed', 'cancelled', 'challenge'].includes(job.status)) {
        await wait(1800)
        const result = await api(`/api/free-accounts/${encodeURIComponent(account.id)}/oauth/status/${encodeURIComponent(job.job_id)}`)
        job = result.job || result
        if (activities[account.id]) activities[account.id].detail = latestJobMessage(job)
      }
      if (job.status !== 'success') throw new Error(job.error || job.error_hint || 'Codex OAuth 获取失败')
    })
    setMessage(`${account.email} 的 Codex AT / RT 已获取并持久化保存`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
}
async function runOAuthSilently(account) {
  return runTracked(account, 'oauth', async () => {
    const started = await api(`/api/free-accounts/${encodeURIComponent(account.id)}/oauth/start`, { method: 'POST', body: {} })
    let job = started.job || started
    if (!job.job_id) throw new Error('Codex OAuth 任务没有返回任务 ID')
    while (!['success', 'failed', 'cancelled', 'challenge'].includes(job.status)) {
      await wait(1800)
      const result = await api(`/api/free-accounts/${encodeURIComponent(account.id)}/oauth/status/${encodeURIComponent(job.job_id)}`)
      job = result.job || result
      if (activities[account.id]) activities[account.id].detail = latestJobMessage(job)
    }
    if (job.status !== 'success') throw new Error(job.error || job.error_hint || 'Codex OAuth 获取失败')
    return job
  })
}
async function runSelectedPipelineTask(action) {
  const selected = [...selectedPipelineAccounts.value]
  if (!selected.length) return setMessage('请先勾选要执行的账号', 'error')
  const requirements = {
    oauth: (account) => account.accept_status === 'completed' && account.remove_status !== 'completed',
    relogin: (account) => account.accept_status === 'completed' && account.remove_status !== 'completed' && hasDownstream(account) && !account.dead,
    push: (account) => account.oauth_status === 'completed' && !hasDownstream(account),
    quota: (account) => hasDownstream(account),
    remove: (account) => account.accept_status === 'completed' && account.remove_status !== 'completed' && !!account.admin_account_id,
  }
  const eligible = selected.filter(requirements[action])
  const skipped = selected.length - eligible.length
  if (!eligible.length) return setMessage(`没有符合条件的账号（已跳过 ${skipped} 个）`, 'error')
  if (action === 'remove' && !window.confirm(`确认将已勾选的 ${eligible.length} 个账号移出空间？同一母号下的账号将依次执行。`)) return
  selectedAccountIDs.value = new Set()
  busy.value = `batch:${action}`
  const labels = { oauth: '授权', relogin: '重登', push: `推送${activeProviderLabel.value}`, quota: '查额度', remove: '移出空间' }
  setMessage(`正在批量${labels[action]}：0/${eligible.length}`)
  let completed = 0
  const execute = async (account) => {
    try {
      if (action === 'oauth') await runOAuthSilently(account)
      else if (action === 'relogin') await runTracked(account, action, () => api(`/api/free-accounts/${encodeURIComponent(account.id)}/relogin`, { method: 'POST', body: {} }))
      else await runTracked(account, action, () => api(`/api/free-accounts/${encodeURIComponent(account.id)}/${action}`, { method: 'POST', body: {} }))
      completed += 1
      setMessage(`正在批量${labels[action]}：${completed}/${eligible.length}`)
      return true
    } catch (error) {
      setMessage(`${account.email}：${error.message}`, 'error')
      return false
    }
  }
  let results
  if (action === 'remove') {
    const groups = new Map()
    eligible.forEach((account) => {
      const key = String(account.team_account_id || account.admin_account_id || account.id)
      if (!groups.has(key)) groups.set(key, [])
      groups.get(key).push(account)
    })
    const groupedResults = await Promise.all([...groups.values()].map(async (accounts) => {
      const values = []
      for (const account of accounts) values.push(await execute(account))
      return values
    }))
    results = groupedResults.flat()
  } else {
    results = await Promise.all(eligible.map(execute))
  }
  busy.value = ''
  await refreshLiveAccounts().catch(() => {})
  emit('reload')
  const succeeded = results.filter(Boolean).length
  const failed = eligible.length - succeeded
  setMessage(`批量${labels[action]}完成：成功 ${succeeded}，失败 ${failed}${skipped ? `，跳过 ${skipped}` : ''}`, failed ? 'error' : 'success')
}
async function runAction(account, action) {
  const label = { relogin: '重登并重新推送', push: `推送${activeProviderLabel.value}`, quota: '刷新额度', remove: '移出空间' }[action]
  try {
    const result = await runTracked(account, action, () => api(`/api/free-accounts/${encodeURIComponent(account.id)}/${action}`, { method: 'POST', body: {} }))
    setMessage(action === 'quota' && result.auto_removed ? `${account.email} 剩余额度已达到移出阈值并自动移出` : `${account.email}：${label}完成`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
}
function closePipelineMenu(delay = 160) {
  window.clearTimeout(pipelineMenuCloseTimer)
  pipelineMenuCloseTimer = window.setTimeout(() => {
    pipelineMenu.open = false
    pipelineMenu.account = null
  }, delay)
}
function keepPipelineMenuOpen() {
  window.clearTimeout(pipelineMenuCloseTimer)
}
function showPipelineMenu(account, event) {
  window.clearTimeout(pipelineMenuCloseTimer)
  const rect = event.currentTarget.getBoundingClientRect()
  const menuWidth = 218
  const menuHeight = 250
  pipelineMenu.account = account
  pipelineMenu.left = Math.max(8, Math.min(window.innerWidth - menuWidth - 8, rect.right - menuWidth))
  pipelineMenu.top = rect.bottom + menuHeight > window.innerHeight - 8
    ? Math.max(8, rect.top - menuHeight - 6)
    : rect.bottom + 6
  pipelineMenu.open = true
}
function runPipelineMenuAction(action) {
  const account = pipelineMenu.account
  pipelineMenu.open = false
  pipelineMenu.account = null
  if (!account) return
  if (action === 'lifecycle') return openLifecycle(account)
  if (action === 'oauth') return acquireOAuth(account)
  if (action === 'join') return openJoin(account)
  return runAction(account, action)
}
async function savePolicy(account, values = {}) {
  busy.value = account.id
  try {
    await api(`/api/free-accounts/${encodeURIComponent(account.id)}/policy`, {
      method: 'PUT', body: { exhaustion_policy: values.policy || account.exhaustion_policy || '7d', auto_remove: values.autoRemove ?? account.auto_remove },
    })
    emit('reload'); setMessage(`${account.email} 的移出策略已更新`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function removeRecord(account) {
  if (!window.confirm(`确认删除 Free 账号“${account.email}”的流水线记录？此操作不会移出空间或删除 Sub2 账号。`)) return
  try { await api(`/api/free-accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' }); emit('reload'); setMessage('流水线记录已删除', 'success') }
  catch (error) { setMessage(error.message, 'error') }
}
async function removeSelectedRecords() {
  const targets = [...selectedPipelineAccounts.value]
  if (!targets.length) return setMessage('请先勾选要删除的账号流程记录', 'error')
  if (!window.confirm(`确认删除已勾选的 ${targets.length} 条账号流程记录？此操作不会移出空间或删除 Sub2 账号。`)) return
  busy.value = 'remove-batch'
  try {
    const results = await Promise.allSettled(targets.map((account) => api(`/api/free-accounts/${encodeURIComponent(account.id)}`, { method: 'DELETE' })))
    const failed = results.filter((item) => item.status === 'rejected').length
    selectedAccountIDs.value = new Set()
    await refreshLiveAccounts()
    emit('reload')
    setMessage(`批量删除完成：成功 ${targets.length - failed}，失败 ${failed}`, failed ? 'error' : 'success')
  } catch (error) {
    setMessage(error.message, 'error')
  } finally {
    busy.value = ''
  }
}
function quotaText(window) {
  if (!window) return '未查询'
  const remaining = Math.max(0, 100 - window.used_percent)
  return `${remaining.toFixed(remaining % 1 ? 1 : 0)}%`
}

let clockTimer
let liveRefreshTimer
onMounted(async () => {
  await Promise.all([loadPushSettings(), loadSub2(), refreshLiveAccounts()])
  clockTimer = window.setInterval(() => { clock.value = Date.now() }, 1000)
  liveRefreshTimer = window.setInterval(() => { if (props.active) refreshLiveAccounts().catch(() => {}) }, 10000)
})
onBeforeUnmount(() => window.clearInterval(clockTimer))
onBeforeUnmount(() => window.clearInterval(liveRefreshTimer))
onBeforeUnmount(stopCapacityRefreshTimer)
onBeforeUnmount(() => window.clearTimeout(pipelineMenuCloseTimer))
</script>

<template>
  <section class="view-stack">
    <header class="page-heading">
      <div><span class="overline">TEAM ROTATION</span><h1>Team 轮转</h1><p>Free 账号入队、加入团队、Codex 授权、Sub2 推送与额度回收</p></div>
      <div class="heading-actions"><StatusPill :tone="activeActivities.length ? 'running' : 'success'">{{ activeActivities.length ? `${activeActivities.length} 个操作执行中` : `${accountSummary.all} 个账号` }}</StatusPill><button class="btn ghost" type="button" @click="refreshLiveAccounts"><RefreshCw :size="15" />刷新</button></div>
    </header>

    <nav class="team-subnav" aria-label="Team 轮转菜单">
      <button type="button" :class="{ active: teamMenu === 'accounts' }" @click="teamMenu = 'accounts'"><Waypoints :size="14" />账号管理</button>
      <button type="button" :class="{ active: teamMenu === 'import' }" @click="teamMenu = 'import'"><Upload :size="14" />导入 Free 账号</button>
      <button type="button" :class="{ active: teamMenu === 'sub2' }" @click="teamMenu = 'sub2'"><Send :size="14" />推送设置</button>
      <button type="button" :class="{ active: teamMenu === 'auto' }" @click="teamMenu = 'auto'"><RefreshCw :size="14" />全自动轮转</button>
      <button type="button" :class="{ active: teamMenu === 'history' }" @click="teamMenu = 'history'"><History :size="14" />执行历史</button>
    </nav>

    <div v-if="teamMenu === 'accounts'" class="metric-grid team-metrics">
      <article class="metric-card green"><DoorOpen :size="17" /><div><span>空间内</span><strong>{{ joinedCount }}</strong><small>已接受团队邀请</small></div></article>
      <article class="metric-card slate"><Gauge :size="17" /><div><span>监控 / 已移出</span><strong>{{ monitoringCount }} / {{ removedCount }}</strong><small>Sub2 额度状态</small></div></article>
      <article class="metric-card blue"><Gauge :size="17" /><div><span>7天平均剩余额度</span><strong>{{ average7DRemaining }}</strong><small>按 5x 席位总数归一化 · 已查询 {{ accountSummary.quota_7d_count }} 个账号</small></div></article>
      <article class="metric-card blue"><DoorOpen :size="17" /><div><span>邀请在途</span><strong>{{ invitePendingCount }}</strong><small>已发邀请但尚未进入空间</small></div></article>
      <article class="metric-card amber"><Gauge :size="17" /><div><span>母号 5x 席位快照</span><strong>{{ premiumSeatSummary.total }}</strong><small>30 分钟缓存 · {{ lastCapacityFetchAt ? `更新于 ${formatTime(lastCapacityFetchAt)}` : '尚未读取' }}</small></div></article>
      <article class="metric-card amber capacity-metric-card"><Gauge :size="17" /><div><span>5x 席位总数 / 剩余</span><strong>{{ premiumSeatSummary.total }} / {{ premiumSeatSummary.remaining }}</strong><small>剩余按当前空间账号实时计算</small></div><button class="metric-refresh-button" type="button" title="刷新 5x 席位" :disabled="capacityRefreshing" @click="loadAdminCapacities({ force: true })"><RefreshCw :class="{ spin: capacityRefreshing }" :size="14" /></button></article>
    </div>

    <div v-if="teamMenu === 'import'" class="free-config-grid account-management-panel">
      <form class="panel" @submit.prevent="importAccounts">
        <div class="panel-title"><div><span>IMPORT</span><h2>导入 Free 账号</h2></div><span class="muted-count">每行一个 AT</span></div>
        <label class="field"><span>Access Token / Session JSON</span><textarea v-model="importForm.tokens" rows="5" spellcheck="false" placeholder="粘贴 Token 或 JSON"></textarea></label>
        <input ref="fileInput" hidden type="file" accept=".json,application/json" @change="importFile" />
        <input ref="folderInput" hidden type="file" accept=".json,application/json" webkitdirectory directory multiple @change="importFolder" />
        <div class="split-actions"><div class="compact-actions"><button class="btn ghost" type="button" @click="fileInput.click()"><FileJson :size="15" />JSON</button><button class="btn ghost" type="button" @click="folderInput.click()"><FolderOpen :size="15" />文件夹</button><button class="btn ghost" type="button" @click="tokenList()"><BadgeCheck :size="15" />解析</button></div><button class="btn primary" type="submit" :disabled="!!busy"><Upload :size="15" />导入账号</button></div>
      </form>
    </div>

    <form v-if="teamMenu === 'sub2'" class="push-settings-grid" @submit.prevent="savePushSettings">
      <div class="panel sub2-config-panel">
      <div class="panel-title"><div><span>SUB2 CONNECTION</span><h2>Sub2 设置</h2><p class="panel-description">配置 Sub2 地址、管理员账号、分组和推送参数</p></div><label class="mini-toggle"><input v-model="pushProvider" value="sub2" type="radio" name="push-provider" /><i></i><span>{{ pushProvider === 'sub2' ? '当前启用' : '启用 Sub2' }}</span></label></div>
      <div class="sub2-fields">
        <label class="field wide"><span>Sub2 地址</span><input v-model="sub2Form.url" type="url" placeholder="https://sub2.example.com" :required="pushProvider === 'sub2'" /></label>
        <label class="field"><span>管理员邮箱</span><input v-model="sub2Form.email" type="email" :required="pushProvider === 'sub2'" /></label>
        <label class="field"><span>管理员密码 <small>{{ sub2Form.passwordPresent ? '已保存，留空不修改' : '' }}</small></span><input v-model="sub2Form.password" type="password" autocomplete="new-password" :required="pushProvider === 'sub2' && !sub2Form.passwordPresent" /></label>
        <div class="field wide"><span>OpenAI 分组 <small>已选 {{ sub2Form.groupIDs.length }} 个</small></span><div class="group-picker"><label v-for="group in groups" :key="group.id"><input v-model="sub2Form.groupIDs" type="checkbox" :value="Number(group.id)" /><span>{{ group.name }}</span><small>#{{ group.id }}</small></label><p v-if="!groups.length">连接 Sub2 后读取可选分组</p></div></div>
        <label class="field wide"><span>可用模型 <small>每行或逗号分隔，留空表示不限制</small></span><textarea v-model="sub2Form.models" rows="4" spellcheck="false" placeholder="gpt-5.2-codex&#10;gpt-5.1-codex-mini"></textarea></label>
        <label class="field"><span>账号并发数</span><input v-model.number="sub2Form.accountConcurrency" type="number" min="1" max="100" required /></label>
        <label class="field"><span>优先级 <small>数值越小优先级越高</small></span><input v-model.number="sub2Form.priority" type="number" min="1" max="100" required /></label>
        <div class="field checkbox-field"><span>WS 推荐配置 <small>推送 Sub2 时启用 cpa_ws=1</small></span><label class="mini-toggle"><input v-model="sub2Form.cpaWs" type="checkbox" /><i></i><span>{{ sub2Form.cpaWs ? '已开启' : '已关闭' }}</span></label></div>
        <div class="field checkbox-field"><span>401 状态检测 <small>关闭后不自动查询 401，也不会触发重登</small></span><label class="mini-toggle"><input v-model="sub2Form.enable401Check" type="checkbox" /><i></i><span>{{ sub2Form.enable401Check ? '已开启' : '已关闭' }}</span></label></div>
        <label class="field"><span>401 状态查询间隔（秒） <small>检测到 401 后自动重登并重新推送</small></span><input v-model.number="sub2Form.statusCheckIntervalSeconds" type="number" min="10" max="86400" required /></label>
        <label class="field"><span>重登连续失败清退次数 <small>成功一次立即清零并重新计数</small></span><input v-model.number="sub2Form.reloginFailureLimit" type="number" min="1" max="20" required /></label>
        <div class="field checkbox-field"><span>额度检测 / 自动移出 <small>关闭后停止后台额度检测和阈值自动移出</small></span><label class="mini-toggle"><input v-model="sub2Form.quotaEnabled" type="checkbox" /><i></i><span>{{ sub2Form.quotaEnabled ? '已开启' : '已关闭' }}</span></label></div>
        <label class="field"><span>额度检测 / 自动移出间隔（秒） <small>检查 5 小时/7 天额度并按策略移出</small></span><input v-model.number="sub2Form.quotaCheckIntervalSeconds" type="number" min="10" max="86400" :disabled="!sub2Form.quotaEnabled" required /></label>
        <label class="field"><span>剩余额度移出阈值（%） <small>剩余小于或等于该百分比时自动移出</small></span><input v-model.number="sub2Form.quotaRemainingThresholdPercent" type="number" min="0" max="100" step="0.1" :disabled="!sub2Form.quotaEnabled" required /></label>
      </div>
      <div class="split-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="connectSub2"><Cable :size="15" />连接并读取分组</button></div>
      </div>
      <div class="panel cpa-config-panel">
        <div class="panel-title"><div><span>CPA MANAGEMENT</span><h2>CPA 设置</h2><p class="panel-description">配置 CPA Management API 和 Codex auth 文件推送</p></div><label class="mini-toggle"><input v-model="pushProvider" value="cpa" type="radio" name="push-provider" /><i></i><span>{{ pushProvider === 'cpa' ? '当前启用' : '启用 CPA' }}</span></label></div>
        <div class="sub2-fields">
          <label class="field wide"><span>CPA 地址</span><input v-model="cpaForm.url" type="url" placeholder="http://127.0.0.1:8317" /></label>
          <label class="field wide"><span>Management Key <small>{{ cpaForm.keyPresent ? '已保存，留空不修改' : '' }}</small></span><input v-model="cpaForm.key" type="password" autocomplete="new-password" /></label>
          <div class="field wide"><span>CPA 账号分组 <small>已选 {{ cpaForm.groupIDs.length }} 个</small></span><div class="group-picker"><label v-for="group in cpaGroups" :key="group.id"><input v-model="cpaForm.groupIDs" type="checkbox" :value="Number(group.id)" /><span>{{ group.name }}</span><small>#{{ group.id }}</small></label><p v-if="!cpaGroups.length">测试 CPA 连接后读取可选分组</p></div></div>
          <div class="field checkbox-field"><span>WS 推荐配置</span><label class="mini-toggle"><input v-model="cpaForm.websockets" type="checkbox" /><i></i><span>{{ cpaForm.websockets ? '已开启' : '已关闭' }}</span></label></div>
          <div class="field checkbox-field"><span>401 状态检测</span><label class="mini-toggle"><input v-model="cpaForm.enable401Check" type="checkbox" /><i></i><span>{{ cpaForm.enable401Check ? '已开启' : '已关闭' }}</span></label></div>
          <label class="field"><span>401 检测间隔（秒）</span><input v-model.number="cpaForm.statusCheckIntervalSeconds" type="number" min="10" max="86400" /></label>
          <label class="field"><span>重登连续失败清退次数 <small>成功一次立即清零并重新计数</small></span><input v-model.number="cpaForm.reloginFailureLimit" type="number" min="1" max="20" /></label>
          <div class="field checkbox-field"><span>额度检测 / 自动移出 <small>关闭后停止后台额度检测和阈值自动移出</small></span><label class="mini-toggle"><input v-model="cpaForm.quotaEnabled" type="checkbox" /><i></i><span>{{ cpaForm.quotaEnabled ? '已开启' : '已关闭' }}</span></label></div>
          <label class="field"><span>额度检测 / 自动移出间隔（秒）</span><input v-model.number="cpaForm.quotaCheckIntervalSeconds" type="number" min="10" max="86400" :disabled="!cpaForm.quotaEnabled" /></label>
          <label class="field"><span>剩余额度移出阈值（%） <small>剩余小于或等于该百分比时自动移出</small></span><input v-model.number="cpaForm.quotaRemainingThresholdPercent" type="number" min="0" max="100" step="0.1" :disabled="!cpaForm.quotaEnabled" /></label>
        </div>
        <div class="split-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="testCPA"><Cable :size="15" />测试 CPA 连接</button></div>
      </div>
      <div class="push-settings-actions"><button class="btn primary" type="submit" :disabled="!!busy"><Save :size="15" />保存推送设置（仅启用一套）</button></div>
    </form>

    <AutoRotationView v-if="teamMenu === 'auto'" :admin-accounts="adminAccounts" :default-page-size="defaultPageSize" />
    <ExecutionHistoryView v-if="teamMenu === 'history'" :default-page-size="defaultPageSize" />

    <MessageBar :message="message" />

    <section v-if="teamMenu === 'accounts'" class="panel list-panel free-list">
      <div class="panel-title responsive"><div><span>PIPELINE</span><h2>账号流程状态</h2></div><div class="heading-actions"><StatusPill v-if="activeActivities.length" tone="running"><LoaderCircle class="spin" :size="12" />执行中 {{ activeActivities.length }}</StatusPill><div class="monitor-countdowns" title="后台监控任务倒计时"><span class="countdown-pill status-countdown">401 检测 <strong>{{ countdownText(statusCountdown, pushProvider === 'cpa' ? !cpaForm.enable401Check : !sub2Form.enable401Check) }}</strong></span><span class="countdown-pill quota-countdown">额度 / 移出 <strong>{{ countdownText(quotaCountdown, !activeQuotaEnabled) }}</strong></span></div><button class="btn ghost" type="button" :disabled="!!busy || !selectedPipelineAccounts.length" @click="runSelectedPipelineTask('relogin')"><RefreshCw :size="15" />批量重登<span v-if="selectedPipelineAccounts.length">（{{ selectedPipelineAccounts.length }}）</span></button><button class="btn ghost" type="button" :disabled="!!busy || !selectedPipelineAccounts.length" @click="runSelectedPipelineTask('oauth')"><KeyRound :size="15" />批量授权<span v-if="selectedPipelineAccounts.length">（{{ selectedPipelineAccounts.length }}）</span></button><button class="btn ghost" type="button" :disabled="!!busy || !selectedPipelineAccounts.length" @click="runSelectedPipelineTask('push')"><Send :size="15" />批量推送 {{ activeProviderLabel }}<span v-if="selectedPipelineAccounts.length">（{{ selectedPipelineAccounts.length }}）</span></button><button class="btn ghost" type="button" :disabled="!!busy || !selectedPipelineAccounts.length" @click="runSelectedPipelineTask('quota')"><Gauge :size="15" />批量查额度<span v-if="selectedPipelineAccounts.length">（{{ selectedPipelineAccounts.length }}）</span></button><button class="btn danger" type="button" :disabled="!!busy || !selectedPipelineAccounts.length" @click="runSelectedPipelineTask('remove')"><Unplug :size="15" />批量移出<span v-if="selectedPipelineAccounts.length">（{{ selectedPipelineAccounts.length }}）</span></button><button class="btn danger" type="button" :disabled="!!busy || !selectedPipelineAccounts.length" @click="removeSelectedRecords"><Trash2 :size="15" />批量删除<span v-if="selectedPipelineAccounts.length">（{{ selectedPipelineAccounts.length }}）</span></button></div></div>
      <div v-if="activeActivities.length" class="execution-list" aria-live="polite">
        <div v-for="activity in activeActivities" :key="activity.id" class="execution-item">
          <LoaderCircle class="spin" :size="18" />
          <div><strong>{{ activity.email }}</strong><span>{{ activityText(activity) }} · 已用 {{ elapsedSeconds(activity) }} 秒</span></div>
          <StatusPill tone="running">{{ stageLabels[activity.stage] }}处理中</StatusPill>
          <div class="execution-progress"><i></i></div>
        </div>
      </div>
      <div class="space-filter-tabs" role="tablist" aria-label="空间状态筛选">
        <button type="button" :class="{ active: teamSpaceFilter === 'outside' }" @click="setTeamSpaceFilter('outside')">未进入空间 <span>{{ accountSummary.outside }}</span></button>
        <button type="button" :class="{ active: teamSpaceFilter === 'inside' }" @click="setTeamSpaceFilter('inside')">在空间里面 <span>{{ accountSummary.inside }}</span></button>
        <button type="button" :class="{ active: teamSpaceFilter === 'removed' }" @click="setTeamSpaceFilter('removed')">已移出空间 <span>{{ accountSummary.removed }}</span></button>
      </div>
      <div class="table-shell"><table><thead><tr><th class="check-column"><input type="checkbox" :checked="allDisplayedSelected" :disabled="!displayedAccounts.length || !!busy" aria-label="选择当前页账号" @change="toggleAllDisplayed" /></th><th>账号</th><th>进入列表</th><th>六步状态</th><th>累计消耗</th><th>5小时</th><th>7天</th><th>移出策略</th><th>重登成功 / 连续失败</th><th class="actions-column">操作</th></tr></thead><tbody>
        <tr v-if="!displayedAccounts.length"><td colspan="10" class="empty-cell">暂无 Free 账号</td></tr>
        <tr v-for="account in displayedAccounts" :key="account.id" :class="{ 'row-running': activityFor(account), 'row-highlighted': entryEmail && account.email === entryEmail }">
          <td class="check-column"><input type="checkbox" :checked="isPipelineSelected(account)" :disabled="!!busy" :aria-label="`选择 ${account.email}`" @change="togglePipelineSelected(account)" /></td><td class="account-cell"><strong>{{ account.email }}</strong><small>{{ account.plan_type || 'free' }} · {{ shortID(account.user_id) }}</small><small class="space-link" :title="adminSpaceID(account)">母号：{{ adminSpaceName(account) }} · 空间：{{ adminSpaceID(account) ? shortID(adminSpaceID(account)) : '未关联' }}</small><small class="credential-state">源 AT {{ account.source_token_present ? '已保存' : '缺失' }} · OAuth AT {{ account.oauth_access_token_present ? '已保存' : '未保存' }} · RT {{ account.oauth_refresh_token_present ? '已保存' : '未保存' }}</small><small v-if="account.dead" class="danger-text" :title="account.dead_reason">死号{{ account.remove_status === 'completed' ? ' · 已自动移出空间' : ' · 自动移出失败' }}</small><small v-else-if="activityFor(account)" class="running-text"><LoaderCircle class="spin" :size="10" />{{ activityText(activityFor(account)) }} · {{ elapsedSeconds(activityFor(account)) }} 秒</small><small v-else-if="account.last_error" class="danger-text" :title="account.last_error">{{ account.last_error }}</small></td>
          <td><small class="table-note">{{ account.imported_at ? formatTime(account.imported_at) : '未知' }}</small></td>
          <td><div class="stage-strip"><label v-for="key in ['invite','accept','oauth','push','quota','remove']" :key="key" :class="['stage-select', `tone-${stageTone(visibleStageStatus(account, key))}`]" :title="`${stageLabels[key]}：${stateLabels[visibleStageStatus(account, key)] || '未开始'}${visibleStageStatus(account, key) === 'running' ? '（可手动修正）' : ''}`"><LoaderCircle v-if="visibleStageStatus(account, key) === 'running'" class="spin" :size="10" /><span v-else>{{ stageLabels[key] }}</span><select :value="visibleStageStatus(account, key)" :aria-label="`${stageLabels[key]}阶段状态`" :disabled="isAccountBusy(account)" @change="saveStageValue(account, key, $event.target.value)"><option value="not_started">未开始</option><option value="pending">待处理</option><option value="completed">成功</option><option value="failed">失败</option></select></label></div></td>
          <td class="cost-cell"><strong>{{ costText(account) }}</strong><small v-if="String(account.push_provider || '').toLowerCase() !== 'cpa'" class="table-note user-cost" title="累计用户消耗额度">{{ userCostText(account) }}</small><small v-if="account.cost_checked_at && account.push_provider !== 'cpa'" class="table-note">{{ formatTime(account.cost_checked_at) }}</small><small v-else-if="account.push_provider === 'cpa'" class="table-note">CPA 不统计</small></td>
          <td><strong>{{ quotaText(account.quota_5h) }}</strong><small v-if="account.quota_5h" class="table-note">剩余</small></td>
          <td><strong>{{ quotaText(account.quota_7d) }}</strong><small v-if="account.quota_7d" class="table-note">剩余</small></td>
          <td><div class="policy-control"><select :value="account.exhaustion_policy || '7d'" :disabled="isAccountBusy(account)" @change="savePolicy(account, { policy: $event.target.value })"><option value="5h">5小时耗尽</option><option value="7d">7天耗尽</option></select><label class="mini-toggle" title="自动移出"><input type="checkbox" :checked="account.auto_remove" :disabled="isAccountBusy(account) || account.remove_status === 'completed'" @change="savePolicy(account, { autoRemove: $event.target.checked })" /><i></i><span>自动</span></label></div><small class="table-note">{{ removeMethodText(account) }}</small><small v-if="account.quota_checked_at" class="table-note">{{ formatTime(account.quota_checked_at) }}</small></td>
          <td><strong>{{ account.relogin_count || 0 }} / {{ account.relogin_failure_count || 0 }}</strong><small class="table-note">成功 / 失败阈值 {{ activeReloginFailureLimit }}</small></td>
          <td><div class="row-actions"><IconButton label="更多操作" @mouseenter="showPipelineMenu(account, $event)" @mouseleave="closePipelineMenu()"><MoreHorizontal :size="15" /></IconButton><IconButton label="刷新 5小时/7天额度" :disabled="account.dead || isAccountBusy(account) || !hasDownstream(account)" @click="runAction(account, 'quota')"><LoaderCircle v-if="activityFor(account)?.action === 'quota'" class="spin" :size="15" /><Gauge v-else :size="15" /></IconButton><IconButton label="删除流水线记录" danger :disabled="isAccountBusy(account)" @click="removeRecord(account)"><Trash2 :size="15" /></IconButton></div></td>
        </tr>
      </tbody></table></div><Pagination :page="page" :page-size="pageSize" :total="accountTotal" @update:page="setAccountPage" @update:page-size="setAccountPageSize" />
    </section>

    <Teleport to="body">
      <div v-if="pipelineMenu.open && pipelineMenu.account" class="pipeline-action-menu" :style="{ top: `${pipelineMenu.top}px`, left: `${pipelineMenu.left}px` }" @mouseenter="keepPipelineMenuOpen" @mouseleave="closePipelineMenu()">
        <button type="button" @click="runPipelineMenuAction('lifecycle')"><History :size="14" />查看账号全流程</button>
        <button type="button" :disabled="pipelineMenu.account.dead || isAccountBusy(pipelineMenu.account) || joinCapacityRefreshing || !adminAccounts.length || pipelineMenu.account.remove_status === 'completed'" @click="runPipelineMenuAction('join')"><DoorOpen :size="14" />{{ joinActionLabel(pipelineMenu.account) }}</button>
        <button type="button" :disabled="pipelineMenu.account.dead || isAccountBusy(pipelineMenu.account) || pipelineMenu.account.accept_status !== 'completed' || pipelineMenu.account.remove_status === 'completed'" @click="runPipelineMenuAction('oauth')"><KeyRound :size="14" />获取 Codex AT / RT</button>
        <button type="button" :disabled="pipelineMenu.account.dead || isAccountBusy(pipelineMenu.account) || pipelineMenu.account.accept_status !== 'completed' || !hasDownstream(pipelineMenu.account) || pipelineMenu.account.remove_status === 'completed'" @click="runPipelineMenuAction('relogin')"><RefreshCw :size="14" />重登并重新推送</button>
        <button type="button" :disabled="pipelineMenu.account.dead || isAccountBusy(pipelineMenu.account) || pipelineMenu.account.oauth_status !== 'completed' || hasDownstream(pipelineMenu.account)" @click="runPipelineMenuAction('push')"><Send :size="14" />推送到{{ activeProviderLabel }}</button>
        <button type="button" class="danger-action" :disabled="isAccountBusy(pipelineMenu.account) || pipelineMenu.account.accept_status !== 'completed' || pipelineMenu.account.remove_status === 'completed'" @click="runPipelineMenuAction('remove')"><Unplug :size="14" />立即移出空间</button>
      </div>
    </Teleport>

    <div v-if="joinOpen" class="modal-backdrop" @click.self="joinOpen = false"><form class="modal" @submit.prevent="joinAccount"><span class="overline">JOIN TEAM SPACE</span><h2>{{ joinActionLabel(joinForm.account) }}</h2><p>{{ joinForm.account?.email }}</p><label class="field"><span>邀请母号</span><select v-model="joinForm.adminAccountID" required><option value="">请选择母号</option><option v-for="admin in adminAccounts" :key="admin.id" :value="admin.id">{{ adminOptionLabel(admin) }}</option></select></label><label class="field"><span>本次邀请席位</span><select v-model="joinForm.seatType"><option value="default">Standard（标准）</option><option value="prolite">Premium（5x）</option></select></label><div class="panel-actions"><button class="btn ghost" type="button" @click="joinOpen = false">取消</button><button class="btn primary" type="submit"><DoorOpen :size="15" />确认执行</button></div></form></div>

    <div v-if="manualPushForm.account" class="modal-backdrop" @click.self="manualPushForm.account = null"><form class="modal" @submit.prevent="saveManualPushStage"><span class="overline">LINK SUB2 ACCOUNT</span><h2>标记推送成功</h2><p>{{ manualPushForm.account?.email }}</p><label class="field"><span>Sub2 账号 ID</span><input v-model="manualPushForm.sub2AccountID" type="number" min="1" step="1" required placeholder="例如 1024" /></label><div class="panel-actions"><button class="btn ghost" type="button" @click="manualPushForm.account = null">取消</button><button class="btn primary" type="submit"><Link2 :size="15" />关联并标记成功</button></div></form></div>

    <div v-if="lifecycleView.account" class="modal-backdrop" @click.self="lifecycleView.account = null">
      <section class="modal lifecycle-modal">
        <div class="modal-heading"><div><span class="overline">ACCOUNT LIFECYCLE</span><h2>{{ lifecycleView.account.email }}</h2><p>从进入 Team 轮转到最终移出的完整流程</p></div><div class="heading-actions"><button class="btn ghost" type="button" @click="exportAccountLogs()"><Download :size="15" />导出账号日志</button><button class="icon-button" type="button" title="关闭" @click="lifecycleView.account = null">×</button></div></div>
        <div v-if="lifecycleView.loading" class="lifecycle-loading">正在加载执行记录…</div>
        <div v-else-if="lifecycleView.error" class="danger-text">{{ lifecycleView.error }}</div>
        <template v-else>
          <div class="lifecycle-summary"><span>进入时间：{{ formatTime(lifecycleView.account.imported_at) }}</span><span>当前状态：{{ lifecycleView.account.status || '-' }}</span><span>当前线路：{{ lifecycleView.account.push_provider || activeProviderLabel }}</span><span>重登成功：{{ lifecycleView.account.relogin_count || 0 }} 次</span><span>连续失败：{{ lifecycleView.account.relogin_failure_count || 0 }} / {{ activeReloginFailureLimit }}</span><span>最近 OAuth 登录：{{ lifecycleLoginSummary }}</span></div>
          <div class="lifecycle-timeline">
            <div v-if="!lifecycleView.events.length" class="empty-cell">暂无详细事件</div>
            <article v-for="event in lifecycleView.events" :key="event.id" class="lifecycle-event"><i></i><div>
              <time>{{ formatTime(event.created_at) }}</time><strong>{{ event.stage || event.operation || event.type || '系统事件' }}</strong><span>{{ event.message || '-' }}</span>
              <small v-if="oauthLoginSummary(event.details)">{{ oauthLoginSummary(event.details) }}</small>
              <small v-if="event.details?.proxy?.name || event.details?.proxy_name">代理：{{ event.details?.proxy?.name || event.details?.proxy_name }}</small>
              <small v-if="event.attempt || event.duration_ms">{{ event.attempt ? `第 ${event.attempt} 次` : '' }} {{ event.duration_ms ? `· ${event.duration_ms} ms` : '' }}</small>
              <details v-if="event.request || event.response || event.details"><summary>查看请求/返回参数</summary><pre v-if="event.request">请求：{{ JSON.stringify(event.request, null, 2) }}</pre><pre v-if="event.response">返回：{{ JSON.stringify(event.response, null, 2) }}</pre><pre v-if="event.details">详情：{{ JSON.stringify(event.details, null, 2) }}</pre></details>
            </div></article>
          </div>
        </template>
      </section>
    </div>

  </section>
</template>

<style scoped>
.team-metrics { grid-template-columns: repeat(6, minmax(0, 1fr)); }
.capacity-metric-card { position: relative; padding-right: 42px; }
.metric-refresh-button { position: absolute; top: 10px; right: 10px; display: inline-grid; width: 26px; height: 26px; place-items: center; padding: 0; border: 1px solid var(--line); border-radius: 4px; background: var(--surface-2); color: var(--muted); cursor: pointer; }
.metric-refresh-button:hover:not(:disabled) { border-color: var(--green); color: var(--green-strong); }
.metric-refresh-button:disabled { cursor: wait; opacity: .6; }
.free-config-grid { display: grid; grid-template-columns: minmax(360px, .9fr) minmax(560px, 1.1fr); gap: 16px; align-items: stretch; }
.team-subnav { display: flex; align-items: center; gap: 6px; padding: 4px; border-bottom: 1px solid var(--line); }
.team-subnav button { display: inline-flex; align-items: center; gap: 7px; min-height: 34px; padding: 0 12px; border: 1px solid transparent; border-radius: 5px; background: transparent; color: var(--muted); font-size: 11px; font-weight: 700; cursor: pointer; }
.team-subnav button:hover { background: var(--surface-2); color: var(--text-2); }
.team-subnav button.active { border-color: rgba(37, 143, 97, .25); background: var(--green-bg); color: var(--green-strong); }
.push-settings-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 16px; align-items: start; }
.push-settings-grid .sub2-config-panel, .push-settings-grid .cpa-config-panel { max-width: none; }
.lifecycle-modal { width: min(900px, calc(100vw - 32px)); max-height: min(820px, calc(100vh - 32px)); overflow: auto; }
.lifecycle-summary { display: flex; flex-wrap: wrap; gap: 8px 16px; padding: 10px; margin: 10px 0 14px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 11px; }
.lifecycle-timeline { display: grid; gap: 0; margin-left: 9px; border-left: 1px solid var(--line); }
.lifecycle-event { position: relative; display: grid; grid-template-columns: 1fr; gap: 3px; padding: 0 0 15px 18px; }
.lifecycle-event > i { position: absolute; left: -5px; top: 3px; width: 9px; height: 9px; border: 2px solid var(--surface); border-radius: 50%; background: var(--green); box-shadow: 0 0 0 1px var(--green); }
.lifecycle-event time, .lifecycle-event small { color: var(--muted); font-size: 10px; }.lifecycle-event strong { font-size: 12px; color: var(--text-2); }.lifecycle-event span { font-size: 11px; color: var(--text); }.lifecycle-event details { margin-top: 4px; }.lifecycle-event summary { color: var(--blue); cursor: pointer; font-size: 10px; }.lifecycle-event pre { max-height: 180px; overflow: auto; padding: 8px; border: 1px solid var(--line); background: var(--surface-2); white-space: pre-wrap; word-break: break-word; font: 10px/1.45 ui-monospace, SFMono-Regular, Consolas, monospace; }
.lifecycle-loading { padding: 30px; text-align: center; color: var(--muted); }
.push-settings-actions { grid-column: 1 / -1; display: flex; justify-content: flex-end; }
.sub2-config-panel { max-width: 980px; }
.panel-description { margin-top: 4px; color: var(--muted); font-size: 10px; }
.split-actions { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.monitor-countdowns { display: inline-flex; flex-wrap: wrap; justify-content: flex-end; gap: 6px; }
.countdown-pill { display: inline-flex; align-items: center; gap: 5px; min-height: 25px; padding: 0 8px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 10px; white-space: nowrap; }
.countdown-pill strong { color: var(--text-2); font-variant-numeric: tabular-nums; }
.status-countdown { border-color: rgba(54, 125, 158, .28); }
.status-countdown strong { color: var(--blue); }
.quota-countdown { border-color: rgba(219, 157, 61, .28); }
.quota-countdown strong { color: var(--amber, #b87918); }
.sub2-fields { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0 12px; }
.sub2-fields .wide { grid-column: 1 / -1; }
.checkbox-field { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
.checkbox-field > span { display: grid; gap: 3px; }
.group-picker { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); max-height: 132px; overflow-y: auto; padding: 7px; gap: 5px; border: 1px solid var(--line); border-radius: 5px; background: var(--bg-elevated); }
.group-picker label { display: grid; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; min-height: 34px; padding: 6px 8px; gap: 7px; border-radius: 4px; color: var(--text-2); cursor: pointer; }
.group-picker label:hover { background: var(--surface-2); }
.group-picker label span { overflow: hidden; font-size: 11px; font-weight: 600; text-overflow: ellipsis; white-space: nowrap; }
.group-picker label small, .group-picker > p { color: var(--muted); font-size: 10px; }
.group-picker > p { grid-column: 1 / -1; padding: 9px; }
.free-list { overflow: hidden; contain: layout paint; }
.free-list table { min-width: 1280px; }
.cost-cell { font-variant-numeric: tabular-nums; }
.cost-cell .user-cost { color: var(--text-2); font-size: 10px; }
.pipeline-action-menu { position: fixed; z-index: 1000; display: grid; width: 218px; gap: 2px; padding: 6px; border: 1px solid var(--line); border-radius: 6px; background: var(--bg-elevated); box-shadow: 0 12px 30px rgba(0, 0, 0, .18); }
.pipeline-action-menu button { display: flex; align-items: center; gap: 8px; min-height: 31px; padding: 0 9px; border: 0; border-radius: 4px; background: transparent; color: var(--text-2); font-size: 10px; text-align: left; cursor: pointer; }
.pipeline-action-menu button:hover:not(:disabled) { background: var(--surface-2); color: var(--green-strong); }
.pipeline-action-menu button:disabled { color: var(--muted); cursor: not-allowed; opacity: .55; }
.pipeline-action-menu button.danger-action { color: var(--red); }
.execution-list { display: grid; gap: 8px; margin: -4px 0 14px; }
.space-filter-tabs { display: flex; flex-wrap: wrap; gap: 8px; margin: 0 0 14px; }
.space-filter-tabs button { display: inline-flex; min-height: 32px; align-items: center; gap: 7px; padding: 0 11px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 10px; font-weight: 650; }
.space-filter-tabs button:hover, .space-filter-tabs button.active { border-color: rgba(37, 143, 97, .35); background: var(--green-bg); color: var(--green-strong); }
.space-filter-tabs span { min-width: 18px; padding: 2px 5px; border-radius: 9px; background: var(--surface-3); font-size: 9px; text-align: center; }
.execution-item { position: relative; display: grid; overflow: hidden; grid-template-columns: auto minmax(0, 1fr) auto; align-items: center; gap: 10px; min-height: 58px; padding: 10px 12px 13px; border: 1px solid rgba(54, 125, 158, .28); border-radius: 5px; background: var(--blue-bg); color: var(--blue); }
.execution-item > div:nth-child(2) { display: grid; min-width: 0; gap: 3px; }
.execution-item strong, .execution-item span { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.execution-item strong { color: var(--text); font-size: 11px; }
.execution-item span { font-size: 10px; }
.execution-progress { position: absolute; right: 0; bottom: 0; left: 0; height: 3px; overflow: hidden; background: rgba(54, 125, 158, .12); }
.execution-progress i { position: absolute; width: 38%; height: 100%; background: var(--blue); animation: progress-scan 1.2s ease-in-out infinite; }
.row-running { background: var(--blue-bg); }
.row-highlighted { box-shadow: inset 3px 0 var(--green); background: var(--green-bg); }
.running-text { display: flex !important; align-items: center; gap: 4px; color: var(--blue) !important; }
.spin { animation: spin .9s linear infinite; }
.stage-strip { display: grid; grid-template-columns: repeat(3, minmax(88px, 1fr)); min-width: 276px; gap: 5px; }
.stage-select { display: grid; grid-template-columns: auto minmax(0, 1fr); align-items: center; min-width: 0; height: 29px; padding-left: 7px; gap: 3px; border: 1px solid var(--line); border-radius: 4px; background: var(--surface-2); }
.stage-select > span { font-size: 9px; font-weight: 700; white-space: nowrap; }
.stage-select > select { min-width: 0; height: 27px; padding: 0 15px 0 2px; border: 0; background-color: transparent; color: inherit; font-size: 9px; font-weight: 650; }
.stage-select.tone-success { border-color: rgba(37, 143, 97, .25); background: var(--green-bg); color: var(--green-strong); }
.stage-select.tone-danger { border-color: rgba(219, 112, 112, .25); background: var(--red-bg); color: var(--red); }
.stage-select.tone-running { border-color: rgba(54, 125, 158, .28); background: var(--blue-bg); color: var(--blue); }
.credential-state { color: var(--green-strong) !important; }
.space-link { color: var(--blue) !important; }
.policy-control { display: flex; align-items: center; gap: 8px; min-width: 180px; }
.policy-control select { height: 30px; border: 1px solid var(--line); border-radius: 4px; background: var(--bg-elevated); color: var(--text-2); font-size: 10px; }
.mini-toggle { display: flex; align-items: center; gap: 5px; color: var(--muted); font-size: 10px; cursor: pointer; }
.mini-toggle input { position: absolute; opacity: 0; }
.mini-toggle i { position: relative; width: 30px; height: 17px; border: 1px solid var(--line); border-radius: 10px; background: var(--surface-3); }
.mini-toggle i::after { position: absolute; top: 3px; left: 3px; width: 9px; height: 9px; border-radius: 50%; background: var(--muted); content: ''; transition: transform .18s; }
.mini-toggle input:checked + i { border-color: var(--green); background: var(--green); }
.mini-toggle input:checked + i::after { background: #fff; transform: translateX(13px); }
.modal .field:first-of-type { margin-top: 18px; }
@keyframes spin { to { transform: rotate(360deg); } }
@keyframes progress-scan { from { transform: translateX(-110%); } to { transform: translateX(290%); } }
@media (max-width: 980px) { .free-config-grid { grid-template-columns: 1fr; } }
@media (max-width: 900px) { .push-settings-grid { grid-template-columns: 1fr; } .push-settings-actions { grid-column: auto; } }
@media (max-width: 620px) { .sub2-fields { grid-template-columns: 1fr; } .sub2-fields .wide { grid-column: auto; } .split-actions { align-items: stretch; flex-direction: column; } .split-actions > .btn { width: 100%; } .execution-item { grid-template-columns: auto minmax(0, 1fr); } .execution-item > .status-pill { display: none; } }
</style>
