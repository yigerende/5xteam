<script setup>
import ProOAIConfirmationStages from './ProOAIConfirmationStages.vue'
import { accountDisposition, filterDisposition, dispositionColumns } from '../pro-disposition'
import ConfigNav from './ConfigNav.vue'
import { useConfigSections } from '../useConfigSections'
const { configSection, revealConfigInvalid } = useConfigSections('sub2')
import { proCardEntry, issuerLabel } from '../pro-card-entry'
import { activationPlan } from '../proActivationPlan'
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue'
import { BadgeCheck, CirclePlus, Clock3, FileKey2, Gauge, History, KeyRound, LoaderCircle, LogIn, Pause, Play, RefreshCw, Rocket, RotateCcw, Search, Settings2, ShieldCheck, Trash2, UsersRound, X } from 'lucide-vue-next'
import { api } from '../api'
import { createLatestRequest } from '../latestRequest'
import { copyText } from '../clipboard'
import { formatTime } from '../utils'
import Sub2ProxyPicker from './Sub2ProxyPicker.vue'
import VM2ProxyPicker from './VM2ProxyPicker.vue'
import VM2GroupPicker from './VM2GroupPicker.vue'
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
import ProMergeV2Stages from './ProMergeV2Stages.vue'
import ProMerchantStages from './ProMerchantStages.vue'
import ProDeletionStages from './ProDeletionStages.vue'
import CompactErrorDetails from './CompactErrorDetails.vue'
import ProTransferDialog from './ProTransferDialog.vue'
import ProFinancialCell from './ProFinancialCell.vue'
import ProPushTimer from './ProPushTimer.vue'
import ProCostCell from './ProCostCell.vue'
import ProQualityPanel from './ProQualityPanel.vue'
import ProQualityCell from './ProQualityCell.vue'
import ProSIWCSettings from './ProSIWCSettings.vue'
import ProSIWCState from './ProSIWCState.vue'

const props = defineProps({ accounts: { type: Array, default: () => [] }, adminAccounts: { type: Array, default: () => [] }, defaultPageSize: { type: Number, default: 10 } })
const emit = defineEmits(['reload', 'open-ejh-card', 'open-nexovcc-card'])
function openCard(entry) { if(entry) emit(entry.provider==='nexovcc'?'open-nexovcc-card':'open-ejh-card',entry) }
function openActivationCard(account) { openCard(proCardEntry(account)) }
const activeTab = ref('accounts')
const spaceFilter = ref('in_progress')
const query = ref('')
const page = ref(1)
const pageSize = ref(props.defaultPageSize)
const rows = ref([])
const total = ref(0)
const listSummary = reactive({ all: 0, oauth_ready: 0, pushed: 0, merged: 0, deleted: 0, merchant:0, merge_v2:0, unmerged: 0, in_progress: 0 })
const selectedEmails = ref(new Set())
const crossPageSelection = ref(false)
let selectionRevision = 0
const busy = ref('')
const mergeRestartRequests = ref({})
const finalStepControlBusy = ref(false)
let finalStepControlRevision = 0
async function toggleAutoFinalStep() {
  if (finalStepControlBusy.value || !settingsLoaded.value) return
  finalStepControlBusy.value = true
  finalStepControlRevision++
  try {
    const saved = await api('/api/pro-settings/auto-final-step-control', { method: 'PUT', body: { paused: !settings.auto_delete_paused } })
    settings.auto_delete_paused = saved.auto_delete_paused
    setMessage(saved.auto_delete_paused ? '全自动最后一步已停止，查额度和其他流程继续；已提交上游的请求无法撤回。' : '全自动最后一步已启动，后续按原有条件和周期判断，本次点击不直接执行最后一步。', 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { finalStepControlRevision++; finalStepControlBusy.value = false; schedulePoll(0) }
}
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
  for (const stage of ['login', 'recharge', 'oauth', 'push', 'quota', 'merge', 'deactivate', 'merchant', 'merge_v2']) {
    if (activeManualStages[account.email.toLowerCase() + ':' + stage]) { state.steps[stage] = 'running'; delete state.errors[stage] }
  }
  return state
}
function phaseErrors(account) {
  const errors=Object.values(phaseState(account).errors)
  if (!account.pro_stage_progress) errors.push(account.pro_auto?.error)
  const deletionErrors=[account.pro_deactivation?.error,account.pro_post_merge_groups?.error].filter(Boolean)
  return [...new Set(errors.filter(e=>e && !(settings.auto_delete_enabled && account.pro_deactivation && deletionErrors.includes(e))))]
}
function manualTaskProgress({ email, mode, running }) { setManualStage(email, mode === 'oauth' ? 'oauth' : 'login', running) }
function autoStageBusy(account) { return !!busy.value || account.pro_workflow_running || account.pro_auto?.status==='running' || Object.values(phaseState(account).steps).includes('running') }
function deletionStageBusy(account) { return !!busy.value || account.pro_workflow_running || account.pro_auto?.status==='running' || Object.values(account.pro_manual_stages || {}).some(s => s.status==='running') || Object.keys(activeManualStages).some(k => k.startsWith(account.email.toLowerCase()+':')) }
async function saveMergeV2Stage(account, step) {
  if(deletionStageBusy(account))return
  const m=account.pro_merge_v2, expected=m?.steps?.[step.key]||''
  if(expected===step.status)return
  const reset=step.key==='submit' && !!m?.submitted_at && step.status!=='completed'
  if(!window.confirm(reset ? '确认上游没有受理这次请求？重置后续跑将重新提交。' : `确认手动修改合并空间2.0的${step.label}状态？修改只记录核实结果，不发送上游请求。`))return
  busy.value='merge-v2-stage:'+account.email
  try{await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/merge-v2-stage`,{method:'PUT',body:{stage:step.key,status:step.status,expected_status:expected,expected_updated_at:account.updated_at,confirm_not_accepted:reset}});await reload();setMessage('合并空间2.0状态已更新','success')}
  catch(e){await loadPage().catch(()=>{});setMessage(e.message,'error')}finally{busy.value=''}
}
async function saveDeletionStage(account, step) {
  if (deletionStageBusy(account)) return
  const expected=account.pro_deactivation?.steps?.[step.key]||''
  if (expected===step.status) return
  const labels={not_started:'未开始',pending:'待处理',completed:'成功',failed:'失败',interrupted:'已中断',unknown:'待核实',waiting:'等待确认'}
  const effect=step.key==='confirm' && step.status==='completed' ? '将按你核实的结果记录为已删除，停止所有定时续跑；未完成的分组调整只能手动续跑，退款仍以卡台流水为准。' : step.key==='submit' && step.status==='completed' ? '将记录为删除已提交，继续核验结果和处理分组，不会再次发送删除请求。' : step.key==='confirm' ? '将重新核验原删除请求；保留已提交记录，不会重复发送删除。' : '修改阶段记录；保留已发送的删除记录，续跑仍执行必要验证，不会重复提交删除。'
  if (!window.confirm(`将 ${account.email} 的删除${step.label}标记为“${labels[step.status]}”？${effect}`)) return
  busy.value='deletion-stage:'+account.email
  try {
    await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/deletion-stage`,{method:'PUT',body:{stage:step.key,status:step.status,expected_status:expected,expected_updated_at:account.updated_at}})
    await reload();setMessage(`删除${step.label}已标记为${labels[step.status]}`,'success')
  } catch(e) { await loadPage().catch(()=>{});setMessage(e.message,'error') }
  finally {busy.value=''}
}
async function saveAutoStage(account, step) {
  if (autoStageBusy(account)) return
  const expected=phaseState(account).steps[step.key]||''
  if (expected===step.status) return
  const labels={not_started:'未开始',pending:'待处理',completed:'已完成',failed:'失败',skipped:'已跳过',interrupted:'已中断',unknown:'待确认',waiting:step.key==='deactivate'?'等待确认':'等待额度'}
  const effect=step.key==='deactivate'?'仅修正全全自动最后一步阶段的显示，删除结果核验仍按原规则执行。确认账号已删除并停止核验，请使用右侧删除账户状态的「确认→成功」。':'仅修正阶段记录，后台仍按实际订单和执行结果运行；再次执行此步骤会更新状态。'
  if (!window.confirm(`将 ${account.email} 的${step.label}标记为“${labels[step.status]}”？${effect}`)) return
  busy.value='auto-stage:'+account.email
  try {
    await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/auto-stage`,{method:'PUT',body:{stage:step.key,status:step.status,expected_status:expected,expected_updated_at:account.updated_at}})
    await reload();setMessage(`${step.label}已标记为${labels[step.status]}`,'success')
  } catch(e) { await loadPage().catch(()=>{});setMessage(e.message,'error') }
  finally {busy.value=''}
}
function rechargeProgress({ email, running }) { setManualStage(email, 'recharge', running) }
const oauth = reactive({ open: false, loading: false, sessionID: '', authURL: '', callbackURL: '', expiresAt: '', targetEmail: '' })
const sub2Groups = ref([])
const cpaGroups = ref([])
const sub2Models = ref('')
const settingsLoaded = ref(false)
const groupErrors = reactive({ sub2: '', cpa: '' })
let groupLoad = null
const countdown = ref(0)
let countdownTimer
let searchTimer
let pollTimer
let disposed = false
const accountsLoading = ref(false)
const renderedAccountPage = ref('')
const accountsRequest = createLatestRequest(value => { accountsLoading.value = value })

const defaults = () => ({
  post_merge_group_ids: [], post_merge_group_names: [],
  provider: 'sub2', quota_enabled: false, quota_used_threshold: 100, quota_check_interval_seconds: 120,
  auto_merge_enabled: false, auto_delete_enabled: false, auto_merchant_enabled:false, auto_merge_v2_enabled:false, auto_delete_paused: false, target_admin_id: '', target_seat_type: 'default', retry_count: 2,recharge_replacement_count: 2,recharge_failover_count: 0, retry_interval_seconds: 3, concurrency: 2,
  next_quota_sweep_at: null,
  sub2: { auth_mode: '', bind_proxy: false, proxy_ids: [], url: '', email: '', password_present: false, group_ids: [], group_names: [], models: [], account_concurrency: 10, priority: 1, cpa_ws: false },
  cpa: { url: '', key_present: false, websockets: false, group_ids: [], group_names: [] },
  vm2api: { group_ids: [], url: '', key_present: false, pool_id: '', bind_proxy: false, proxy_ids: [], runtime_type: 'docker', kernel: 'ubuntu-24.04', memory: '1g', vcpus: 2, disk_gb: 20, region: '', locale: 'en_US.UTF-8', status_check_interval_seconds: 120, quota_check_interval_seconds: 120, relogin_failure_limit: 2 },
  sub2_password: '', cpa_key: '', vm2api_key: '',
})
const settings = reactive(defaults())
function normalizeSection(base, value) {
  const result = { ...base, ...value }
  for (const key of Object.keys(base)) {
    if (Array.isArray(base[key]) && !Array.isArray(result[key])) result[key] = []
  }
  return result
}
function applySettings(data) {
  const base = defaults()
  Object.assign(settings, normalizeSection(base, data), {
    sub2: normalizeSection(base.sub2, data.sub2),
    cpa: normalizeSection(base.cpa, data.cpa),
    vm2api: normalizeSection(base.vm2api, { ...data.vm2api, group_ids: data.vm2api?.group_ids || (data.vm2api?.pool_id ? [data.vm2api.pool_id] : []) }),
    sub2_password: '', cpa_key: '', vm2api_key: '',
  })
}
const pushModeDrafts = new Map()
async function switchPushMode(event) {
  if (busy.value) return
  const mode=event.target.value
  const old=settings.sub2.auth_mode||''
  pushModeDrafts.set(old,{sub2:JSON.parse(JSON.stringify(settings.sub2)),password:settings.sub2_password,models:sub2Models.value})
  busy.value='switch-push-mode'
  try {
    const draft=pushModeDrafts.get(mode)
    settings.sub2=normalizeSection(defaults().sub2, draft?.sub2||await api('/api/pro-settings/sub2/variant?auth_mode='+encodeURIComponent(mode)))
    settings.sub2.auth_mode=mode
    settings.sub2_password=draft?.password||''
    sub2Models.value=draft?.models??settings.sub2.models.join('\n')
    sub2Groups.value=settings.sub2.group_ids.map((id,index)=>({id:Number(id),name:settings.sub2.group_names[index]||`#${id}`}))
    groupErrors.sub2=''
  } catch(e) {event.target.value=old;setMessage(e.message,'error')}
  finally {busy.value=''}
}

const rowDisposition = account => accountDisposition(account, settings, spaceFilter.value)
const flowColumnTitle = computed(() => { const fixed=filterDisposition(spaceFilter.value); if(fixed)return dispositionColumns[fixed]; const modes=[...new Set(rows.value.map(rowDisposition))]; return modes.length===1?dispositionColumns[modes[0]]:'流程状态' })
const accountTimeLabel = computed(() => ({in_progress:'进入流程',merged:'合并成功',deleted:'进入删除',merchant:'商户完成',merge_v2:'进入合并2.0',unmerged:'进入 Pro'}[spaceFilter.value] || '进入 Pro'))
const accountSortHint = computed(() => spaceFilter.value==='unmerged' ? '按进入 Pro 时间升序，最早在前' : spaceFilter.value==='all' ? '按进入 Pro 时间降序，最新在前' : '按进入当前状态的时间降序，最新在前')
const displayedAccounts = computed(() => rows.value)
const selectedAccounts = computed(() => {
  const visible = new Map(rows.value.map(account => [account.email.toLowerCase(), account]))
  return [...selectedEmails.value].map(email => visible.get(email) || { email })
})
const allVisibleSelected = computed(() => displayedAccounts.value.length && displayedAccounts.value.every((account) => selectedEmails.value.has(account.email.toLowerCase())))
const oauthReady = computed(() => Number(listSummary.oauth_ready || 0))
const pushed = computed(() => Number(listSummary.pushed || 0))
const merged = computed(() => Number(listSummary.merged || 0))

async function loadPage({ background = false } = {}) {
  const controlRevision = finalStepControlRevision
  const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize.value), query: query.value.trim(), merge_state: spaceFilter.value === 'all' ? '' : spaceFilter.value })
  // A filter change must not leave actionable rows from the previous tab.
  if (renderedAccountPage.value !== params.toString()) rows.value = []
  return accountsRequest.run(
    signal => api(`/api/pro-accounts?${params}`, { signal }),
    data => {
      const previous = new Map(rows.value.map(account => [account.email, account]))
      renderedAccountPage.value = params.toString()
      rows.value = data.items || []; total.value = Number(data.total || 0); Object.assign(listSummary, data.summary || {})
      if (typeof data.auto_delete_paused === 'boolean' && controlRevision === finalStepControlRevision && !finalStepControlBusy.value) settings.auto_delete_paused = data.auto_delete_paused
      for (const account of rows.value) {
        if (previous.get(account.email)?.pro_workflow_running && !account.pro_workflow_running) {
          if (account.pro_last_error) setMessage(account.email + '：' + account.pro_last_error, 'error')
          else if (account.pro_remove_status === 'completed') setMessage(account.email + ' 空间四步流程完成', 'success')
        }
      }
      const lastPage = Math.max(1, Math.ceil(total.value / pageSize.value))
      if (page.value > lastPage) { page.value = lastPage; return loadPage() }
      const visible = new Set(rows.value.map((account) => account.email.toLowerCase()))
      if (!crossPageSelection.value) selectedEmails.value = new Set([...selectedEmails.value].filter((email) => visible.has(email)))
    }, { background },
  )
}
function schedulePoll(delay = 1500) {
  window.clearTimeout(pollTimer)
  if (disposed) return
  pollTimer = window.setTimeout(async () => {
    try {
      if (activeTab.value === 'accounts' && !document.hidden) await loadPage({ background: true })
    } catch { /* Keep last known states and retry; do not mask a business error. */ }
    finally {
      schedulePoll(busy.value || rows.value.some(account => account.pro_workflow_running || Object.values(phaseState(account).steps || {}).includes('running')) ? 1500 : 5000)
    }
  }, delay)
}
watch(activeTab, tab => { if (tab === 'accounts') schedulePoll(0) })
watch([activeTab, () => settings.provider, settingsLoaded], () => {
  cancelGroupLoad()
  if (activeTab.value !== 'push' || !settingsLoaded.value) return
  const provider = settings.provider
  if (provider === 'vm2api') return
  const target = settings[provider]
  const ready = provider === 'sub2'
    ? target.url && target.email && (target.password_present || settings.sub2_password)
    : target.url && (target.key_present || settings.cpa_key)
  if (ready) void loadGroups(provider, true)
})
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
function loadPageHandled() { return loadPage().catch(error => setMessage(error.message, 'error')) }
function clearSelection() { selectionRevision++; selectedEmails.value = new Set(); crossPageSelection.value = false }
function setPage(value) { page.value = value; if (!crossPageSelection.value) clearSelection(); loadPageHandled() }
function setPageSize(value) { pageSize.value = value; page.value = 1; if (!crossPageSelection.value) clearSelection(); loadPageHandled() }
function setSpaceFilter(value) { if (spaceFilter.value === value) return; window.clearTimeout(searchTimer); spaceFilter.value = value; page.value = 1; clearSelection(); loadPageHandled() }
watch(query, () => { window.clearTimeout(searchTimer); page.value = 1; clearSelection(); searchTimer = window.setTimeout(loadPageHandled, 250) })

async function selectUnloggedAccounts() {
  if (busy.value || spaceFilter.value !== 'unmerged') return
  const revision = ++selectionRevision
  const search = query.value.trim()
  busy.value = 'select-unlogged'
  try {
    const result = await api('/api/pro-accounts/select-unlogged?' + new URLSearchParams({ query: search }))
    if (disposed || revision !== selectionRevision || spaceFilter.value !== 'unmerged' || query.value.trim() !== search) return
    selectedEmails.value = new Set((result.emails || []).map(email => email.toLowerCase()))
    crossPageSelection.value = true
    setMessage(selectedEmails.value.size ? `已选中 ${selectedEmails.value.size} 个未登录账号（全部分页${search ? '，按当前搜索条件' : ''}）` : '没有符合条件的未登录账号', 'success')
  } catch (error) {
    if (!disposed && revision === selectionRevision) setMessage(error.message, 'error')
  } finally { busy.value = '' }
}

function setMessage(text = '', type = '') { message.text = text; message.type = type }
function toggle(account) { const next = new Set(selectedEmails.value); const email = account.email.toLowerCase(); next.has(email) ? next.delete(email) : next.add(email); selectedEmails.value = next }
function toggleVisible() { const next = new Set(selectedEmails.value); displayedAccounts.value.forEach((a) => allVisibleSelected.value ? next.delete(a.email.toLowerCase()) : next.add(a.email.toLowerCase())); selectedEmails.value = next }
const vmStages = { creating: '创建并开机', authorizing: '写入凭证', validating: '校验账号', scheduling: '启用调度', ready: '就绪', deleting: '清理中', delete_failed: '清理失败', deleted: '已清理' }
function quotaLabel(window) { return window ? `${Number(window.used_percent || 0).toFixed(1)}%` : '-' }
function statusTone(value) { return value === 'completed' ? 'success' : value === 'failed' || value === 'reauthorize_required' ? 'danger' : value === 'running' ? 'running' : 'pending' }
function postMergeGroupsPending(account) { return account.pro_post_merge_groups && account.pro_post_merge_groups.status !== 'completed' }
function postMergeGroupsLabel(account) { return { pending: '待调整分组', running: '正在调整分组', completed: '分组已调整', failed: '分组调整失败' }[account.pro_post_merge_groups?.status] || '待调整分组' }
function updateCountdown() { countdown.value = settings.next_quota_sweep_at ? Math.max(0, Math.ceil((Date.parse(settings.next_quota_sweep_at) - Date.now()) / 1000)) : 0 }
async function reload() { await loadPage(); emit('reload') }

async function loadSettings() {
  try {
    const data = await api('/api/pro-settings')
    applySettings(data)
    sub2Models.value = (settings.sub2.models || []).join('\n')
    for (const provider of ['sub2', 'cpa']) {
      const target = settings[provider]
      const groups = provider === 'sub2' ? sub2Groups : cpaGroups
      groups.value = (target.group_ids || []).map((id, index) => ({ id: Number(id), name: target.group_names?.[index] || `#${id}` }))
      if (provider === 'sub2') {
        for (const [index, id] of (settings.post_merge_group_ids || []).entries()) {
          if (!groups.value.some(group => group.id === Number(id))) groups.value.push({ id: Number(id), name: settings.post_merge_group_names?.[index] || `#${id}` })
        }
      }
    }
    settingsLoaded.value = true
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
async function restartMergeV2(account) {
  if (mergeRestartRequests.value[account.email]) return
  mergeRestartRequests.value[account.email] = true
  try {
    await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/merge-v2-restart`, {method:'POST', body:{request_id:crypto.randomUUID()}})
    setMessage('已提交重新执行，旧任务退出后从邀请开始。')
    await loadPage()
    schedulePoll(0)
  } catch (error) { setMessage(error.message, 'error') }
  finally { delete mergeRestartRequests.value[account.email] }
}
async function runOne(account, action) {
  if (busy.value || account.pro_workflow_running) return
  if (action === 'deactivate' && !account.pro_deactivation?.submitted_at && !window.confirm(`确认删除 OpenAI 账户 ${account.email}？此操作会删除上游账户，不是重新推送。若要重新推送，请使用“推送 / 重新授权”。删除请求一旦提交上游，停止流程也无法撤回。`)) return
  busy.value = `${action}:${account.email}`
  const stage = action === 'refresh' ? 'oauth' : action
  setManualStage(account.email, stage, true)
  setMessage()
  schedulePoll(0)
  const paths = { refresh: 'oauth/refresh', push: 'push', quota: 'quota', merge: 'merge', deactivate: 'deactivate', merchant:'merchant', merge_v2:'merge-v2' }
  let accepted = false
  try {
    const result = await api(`/api/pro-accounts/${encodeURIComponent(account.email)}/${paths[action]}`, { method: 'POST' })
    accepted = ['merge','deactivate','merchant','merge_v2'].includes(action)
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
    else if (action === 'merge' && latest?.pro_remove_status === 'completed' && !postMergeGroupsPending(latest)) setMessage(account.email + ' 空间四步流程完成', 'success')
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
  if (!window.confirm(`确认删除已勾选的 ${emails.length} 个 Pro 账号？将删除本地账号、凭证及关联的 Team 轮转记录，不会移回邮件管理。已绑定 vm2api 的账号会先删除对应槽位和运行环境，失败则保留本地记录供重试。不会操作 OpenAI、Sub2 或 CPA；开通订单和银行卡使用记录保留。此操作不可撤销。`)) return
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
  target.group_names = (target.group_ids || []).map((id, index) => available.find((g) => Number(g.id) === Number(id))?.name || target.group_names?.[index] || `#${id}`)
  if (provider === 'sub2') settings.post_merge_group_names = settings.post_merge_group_ids.map(id => available.find(g => Number(g.id) === Number(id))?.name || `#${id}`)
}
function connectionPayload(provider) {
  if (provider === 'vm2api') return { vm2api: { ...settings.vm2api, url: settings.vm2api.url.trim() }, vm2api_key: settings.vm2api_key }
  return provider === 'sub2'
    ? { sub2: { auth_mode: settings.sub2.auth_mode || '', url: settings.sub2.url.trim(), email: settings.sub2.email.trim() }, sub2_password: settings.sub2_password }
    : { cpa: { url: settings.cpa.url.trim() }, cpa_key: settings.cpa_key }
}
function updateGroups(provider, groups) {
  const target = provider === 'sub2' ? sub2Groups : cpaGroups
  const loaded = new Map((groups || []).map(group => [Number(group.id), { ...group, id: Number(group.id) }]))
  const selected = [...(settings[provider].group_ids || []), ...(provider === 'sub2' ? settings.post_merge_group_ids || [] : [])]
  for (const id of selected) {
    if (!loaded.has(Number(id))) loaded.set(Number(id), target.value.find(group => Number(group.id) === Number(id)) || { id: Number(id), name: `#${id}` })
  }
  target.value = [...loaded.values()]
}
function cancelGroupLoad() {
  if (!groupLoad) return
  groupLoad.controller.abort()
  if (busy.value === `groups-${groupLoad.provider}`) busy.value = ''
  groupLoad = null
}
async function loadGroups(provider, automatic = false) {
  if (busy.value) return
  const controller = new AbortController()
  const payload = connectionPayload(provider)
  groupLoad = { provider, controller }
  busy.value = `groups-${provider}`
  groupErrors[provider] = ''
  const current = () => !disposed && !controller.signal.aborted && JSON.stringify(payload) === JSON.stringify(connectionPayload(provider))
  try {
    const groups = await api(`/api/pro-settings/${provider}/groups`, { method: 'POST', body: payload, signal: controller.signal })
    if (!current()) return
    updateGroups(provider, groups)
    if (!automatic) setMessage(`已读取 ${groups.length} 个分组`, 'success')
  } catch (error) {
    if (!current()) return
    groupErrors[provider] = `读取分组失败：${error.message}。已保存的选择仍保留，可点击重新读取。`
    if (!automatic) setMessage(error.message, 'error')
  } finally {
    if (groupLoad?.controller === controller) { groupLoad = null; busy.value = '' }
  }
}
async function testProvider(provider) {
  if (busy.value) return
  busy.value = `test-${provider}`
  try { const result = await api(`/api/pro-settings/${provider}/test`, { method: 'POST', body: connectionPayload(provider) }); if (provider === 'sub2' && result.groups) { updateGroups(provider, result.groups); groupErrors[provider] = '' }; setMessage(`${provider.toUpperCase()} 连接成功`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}
async function saveSettings() {
  if (busy.value) return
  busy.value = 'save-settings'
  try {
    syncGroupNames('sub2'); syncGroupNames('cpa')
    settings.sub2.models = [...new Set(sub2Models.value.split(/[\n,]+/).map(item => item.trim()).filter(Boolean))]
    const saved = await api('/api/pro-settings', { method: 'PUT', body: settings })
    applySettings(saved)
    sub2Models.value = settings.sub2.models.join('\n')
    updateCountdown(); setMessage('Pro 配置已保存', 'success')
  }
  catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = '' }
}

onMounted(async () => {
  countdownTimer = window.setInterval(updateCountdown, 1000)
  try { await Promise.all([loadSettings(), loadPage()]) }
  catch (error) { setMessage(error.message, 'error') }
  finally { schedulePoll() }
})
onBeforeUnmount(() => { disposed = true; cancelGroupLoad(); accountsRequest.dispose(); window.clearInterval(countdownTimer); window.clearTimeout(searchTimer); window.clearTimeout(pollTimer) })
</script>

<template>
  <section class="view-stack">
    <ProAccountLogDialog :default-page-size="defaultPageSize" ref="accountLogs" />
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
      <button :class="{ active: activeTab === 'quality' }" type="button" @click="activeTab = 'quality'"><ShieldCheck :size="15" />降智检测</button>
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
        <div class="account-toolbar"><div class="space-filter-tabs"><button :class="{ active: spaceFilter === 'all' }" @click="setSpaceFilter('all')">全部 <span>{{ listSummary.all }}</span></button><button :class="{ active: spaceFilter === 'unmerged' }" title="未开始后续流程的账号，包含仅登录完成的账号" @click="setSpaceFilter('unmerged')">未空间合并 <span>{{ listSummary.unmerged }}</span></button><button :class="{ active: spaceFilter === 'in_progress' }" title="已开始阶段但尚未成功合并，包含等待额度、失败及中断的账号" @click="setSpaceFilter('in_progress')">流程中 <span>{{ listSummary.in_progress }}</span></button><button :class="{ active: spaceFilter === 'merged' }" @click="setSpaceFilter('merged')">已空间合并 <span>{{ merged }}</span></button><button :class="{ active: spaceFilter === 'deleted' }" @click="setSpaceFilter('deleted')">删除账户 <span>{{ listSummary.deleted }}</span></button><button :class="{active:spaceFilter==='merchant'}" @click="setSpaceFilter('merchant')">API 商户 <span>{{listSummary.merchant}}</span></button><button :class="{active:spaceFilter==='merge_v2'}" @click="setSpaceFilter('merge_v2')">合并空间2.0 <span>{{listSummary.merge_v2}}</span></button><button :class="{active:spaceFilter==='dead'}" @click="setSpaceFilter('dead')">死号 <span>{{listSummary.dead||0}}</span></button></div><div class="heading-actions"><button v-if="spaceFilter === 'in_progress' || settings.auto_delete_paused" class="btn ghost" :class="{ 'danger-text': !settings.auto_delete_paused }" type="button" :disabled="finalStepControlBusy || !settingsLoaded" :title="settings.auto_delete_paused ? '恢复按原条件和周期判断，不会立即执行最后一步' : '暂停删除、空间合并、合并空间2.0和 API 商户，查额度和前序流程继续'" @click="toggleAutoFinalStep"><LoaderCircle v-if="finalStepControlBusy" :size="14" class="spin"/><Play v-else-if="settings.auto_delete_paused" :size="14"/><Pause v-else :size="14"/>{{ finalStepControlBusy ? '处理中…' : settings.auto_delete_paused ? '启动全自动最后一步' : '停止全自动最后一步' }}</button><label class="compact-search"><Search :size="14" /><input v-model="query" placeholder="搜索账号" /></label><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length || selectedAccounts.some(account => account.pro_workflow_running || account.pro_auto?.status === 'running')" @click="requestTemporaryAT(selectedAccounts, true)"><KeyRound :size="14" />批量获取临时 AT</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="runBatch('push')"><Rocket :size="14" />批量推送</button><button class="btn ghost" :disabled="!!busy || !selectedAccounts.length" @click="runBatch('quota')"><Gauge :size="14" />批量查额度</button><button class="btn ghost danger-text" :disabled="!!busy || !selectedAccounts.length" @click="deleteSelectedAccounts"><LoaderCircle v-if="busy === 'delete-batch'" :size="14" class="spin" /><Trash2 v-else :size="14" />{{ busy === 'delete-batch' ? '删除中…' : '批量删除' }}<span v-if="selectedAccounts.length">（{{ selectedAccounts.length }}）</span></button></div></div>
        <div v-if="spaceFilter === 'unmerged' || selectedAccounts.length" class="heading-actions">
          <button v-if="spaceFilter === 'unmerged'" class="btn ghost" type="button" :disabled="!!busy" title="跨全部分页，按当前搜索条件选中未取得登录凭证且无登录完成记录的账号" @click="selectUnloggedAccounts"><LoaderCircle v-if="busy === 'select-unlogged'" :size="14" class="spin"/><UsersRound v-else :size="14"/>{{ busy === 'select-unlogged' ? '筛选中…' : '一键选中未登录账号' }}</button>
          <span v-if="selectedAccounts.length" class="table-note" role="status">已选 {{ selectedAccounts.length }} 个{{ crossPageSelection ? '（跨页）' : '' }}</span>
          <button v-if="selectedAccounts.length" class="btn ghost" type="button" :disabled="!!busy" @click="clearSelection">清空选择</button>
        </div>
        <p v-if="settings.auto_delete_paused" class="table-note" role="status">全自动最后一步已暂停 · 查额度及其他流程继续 · 点击「启动全自动最后一步」恢复原规则</p>
        <div class="table-shell"><table class="pro-table"><thead><tr><th><input type="checkbox" :checked="allVisibleSelected" @change="toggleVisible" /></th><th>账号</th><th>智商情况</th><th>OAuth</th><th title="显示成功开通订单保存的套餐，不再远程识别">开通套餐</th><th>开通银行卡</th><th>回款状态</th><th>推送时限</th><th>推送时长</th><th>推送</th><th title="与 Team 轮转一致：查询 Sub2 近 90 天统计，随额度检测更新">消耗额度</th><th>5小时</th><th>7天</th><th>空间合并</th><th>全自动阶段</th><th>{{flowColumnTitle}}</th><th>OAI 确认</th><th class="actions-column">操作</th></tr></thead><tbody>
          <tr v-if="accountsLoading && !displayedAccounts.length"><td colspan="18" class="empty-cell" role="status"><LoaderCircle :size="16" class="spin" /> 正在加载账号…</td></tr>
          <tr v-else-if="!displayedAccounts.length"><td colspan="18" class="empty-cell">暂无符合条件的 Pro 账号</td></tr>
<tr v-for="account in displayedAccounts" :key="account.email"><td><input type="checkbox" :checked="selectedEmails.has(account.email.toLowerCase())" @change="toggle(account)" /></td><td class="account-cell"><strong>{{ account.email }}</strong><StatusPill v-if="account.pro_dead" tone="danger" :title="account.pro_dead.reason">死号 · {{formatTime(account.pro_dead.at)}}</StatusPill><small :title="accountSortHint">{{ accountTimeLabel }} {{ account.state_entered_at ? formatTime(account.state_entered_at) : '时间未记录' }}</small><small v-if="account.pro_migration?.paused" class="table-note" :title="account.pro_migration.notice">迁移待续跑</small><CompactErrorDetails v-if="account.pro_last_error && !(rowDisposition(account)==='deactivate' && account.pro_deactivation)" :messages="[account.pro_last_error]" /></td><td><ProQualityCell :account="account" /><ProSIWCState :account="account" @updated="loadPage"/></td><td><StatusPill :tone="statusTone(account.oauth_status)">{{ account.siwc_present && (account.pro_siwc || settings.sub2.auth_mode === 'siwc') ? 'SIWC RT / AT 完整' : account.access_token_present && account.refresh_token_present ? 'AT / RT 完整' : '待授权' }}</StatusPill><small v-if="account.oauth_expires_at" class="table-note">{{ formatTime(account.oauth_expires_at) }}</small></td><td><StatusPill :tone="activationPlan(account).tone" :title="activationPlan(account).title">{{ activationPlan(account).label }}</StatusPill></td><td class="activation-card-cell"><template v-if="account.activation_card"><button v-if="proCardEntry(account)" type="button" class="activation-card-link" :title="`在 ${issuerLabel(proCardEntry(account))} 卡片管理中查看此账号的开通卡`" @click="openActivationCard(account)"><strong>{{ account.activation_card.card_name || '银行卡' }}</strong><small class="table-note">{{ account.activation_card.card_last4 ? '尾号 ' + account.activation_card.card_last4 : '尾号未记录' }} · {{issuerLabel(proCardEntry(account))}} ↗</small></button><template v-else><strong>{{ account.activation_card.card_name || '银行卡' }}</strong><small class="table-note">{{ account.activation_card.card_last4 ? '尾号 ' + account.activation_card.card_last4 : '尾号未记录' }}</small></template></template><span v-else class="table-note">未记录</span></td><td><ProFinancialCell :account="account" :default-page-size="defaultPageSize" @open-card="openCard" @updated="emit('reload')"/></td><td><strong>{{ account.pro_push_timeout_enabled === true ? '已开启' : account.pro_push_timeout_enabled === false ? '未开启' : '未知' }}</strong><small class="table-note">{{ account.pro_push_timeout_enabled === true ? account.pro_push_timeout_minutes + ' 分钟' : account.pro_push_timeout_enabled === false ? '无强制合并时限' : '历史设置未记录' }}</small></td><td><ProPushTimer :account="account"/></td><td><StatusPill :tone="statusTone(account.push_status)">{{ account.push_status === 'completed' ? (account.push_provider || '-').toUpperCase() : account.push_status || '未推送' }}</StatusPill><template v-if="account.vm2api"><small class="table-note">{{ account.vm2api.runtime_type === 'kvm' ? 'KVM' : 'Docker' }} · {{ vmStages[account.vm2api.stage] || account.vm2api.stage }}</small><small class="table-note" :title="account.vm2api.slot_id">{{ account.vm2api.container_name || account.vm2api.slot_id }}</small><small v-if="account.vm2api.stage === 'ready'" class="table-note">{{ account.vm2api.running ? '已开机' : '已停止' }} · {{ account.vm2api.schedulable ? '调度开启' : '调度暂停' }}</small><small v-if="account.vm2api.error" class="table-note danger-text" :title="account.vm2api.error">{{ account.vm2api.error }}</small><button v-if="account.vm2api.stage === 'delete_failed' && ['completed', 'team_removed'].includes(account.pro_remove_status)" class="btn ghost compact" type="button" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="runOne(account, 'merge')">重试清理</button></template></td><td><ProCostCell :account="account" /></td><td>{{ quotaLabel(account.quota_5h) }}</td><td>{{ quotaLabel(account.quota_7d) }}<small v-if="account.quota_checked_at" class="table-note">{{ formatTime(account.quota_checked_at) }}</small></td><td><StatusPill :tone="account.space_merged_once ? 'success' : 'pending'">{{ account.space_merged_once ? '已成功' : '未合并' }}</StatusPill></td><td class="auto-flow-cell"><StatusPill v-if="account.pro_auto?.payment_review_required" tone="partial" title="已停止自动重试，原账号和订单保留；核实后手动处理">付款待核实</StatusPill><small v-else-if="account.pro_auto?.status === 'failed'" class="danger-text">流程失败</small><button v-if="account.quota_resume_available || ['failed','interrupted','stopped'].includes(account.pro_auto?.status)" type="button" class="btn ghost compact" :disabled="!!busy || account.pro_workflow_running" @click="autoPro?.open(account)">{{account.quota_resume_available ? '恢复额度监控' : '重试 / 继续'}}</button><ProAutoStages :state="phaseState(account)" :disposition="rowDisposition(account)" editable :disabled="autoStageBusy(account)" @change="saveAutoStage(account,$event)"/><button type="button" class="btn ghost compact auto-flow-open" @click="account.pro_auto?.id ? autoPro?.open(account) : accountLogs?.open(account)">{{account.pro_auto?.id ? '查看全自动进度' : '查看账号流程日志'}}</button><CompactErrorDetails :messages="phaseErrors(account)" /></td><td v-if="rowDisposition(account)==='merge_v2'" class="flow-state"><ProMergeV2Stages :account="account" :disabled="deletionStageBusy(account)" :restarting="!!mergeRestartRequests[account.email]" @restart="restartMergeV2(account)" @retry="runOne(account,'merge_v2')" @change="saveMergeV2Stage(account,$event)"/></td><td v-else-if="rowDisposition(account)==='merchant'" class="flow-state"><ProMerchantStages :account="account" :disabled="autoStageBusy(account)" @retry="runOne(account, 'merchant')"/></td><td v-else-if="rowDisposition(account)==='merge'" class="flow-state"><div class="merge-stage-strip"><label v-for="step in mergeStages(account)" :key="step.key" class="merge-stage" :class="step.status" :data-stage="step.key" :title="`${step.label}：${step.text}`"><LoaderCircle v-if="step.status === 'running'" class="spin" :size="11" /><b>{{ { invite: '邀', accept: '进', transfer: '合', remove: '移' }[step.key] }}</b><select :value="step.status" :aria-label="`${step.label}阶段状态`" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @change="saveMergeStage(account, step, $event)"><option value="not_started">未开始</option><option value="pending">待处理</option><option v-if="step.status === 'running'" value="running" disabled>执行中</option><option v-if="step.status === 'team_removed'" value="team_removed" disabled>已移出</option><option v-if="step.status === 'unknown'" value="unknown" disabled>待确认</option><option value="completed">成功</option><option value="failed">失败</option></select></label></div><small v-if="mergeActivity(account)" class="merge-activity"><LoaderCircle class="spin" :size="11" />{{ mergeActivity(account) }}</small><div v-if="account.pro_post_merge_groups" class="post-merge-group-status"><StatusPill :tone="statusTone(account.pro_post_merge_groups.status)"><LoaderCircle v-if="account.pro_post_merge_groups.status === 'running'" class="spin" :size="11" />{{ postMergeGroupsLabel(account) }}</StatusPill><CompactErrorDetails :messages="[account.pro_post_merge_groups.error]" /><button v-if="postMergeGroupsPending(account) && account.pro_remove_status === 'completed'" class="btn ghost compact" type="button" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="runOne(account, 'merge')">重试调整分组</button></div></td><td v-else class="flow-state"><ProDeletionStages :account="account" :disabled="deletionStageBusy(account)" @change="saveDeletionStage(account,$event)" @retry="runOne(account, 'deactivate')"/></td><td><ProOAIConfirmationStages :account="account" :mode="rowDisposition(account)" @updated="loadPage"/></td><td class="actions-cell"><ProActionMenu :email="account.email"><button v-if="account.pro_migration" type="button" title="继续迁移流程" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="resumeImported(account)"><Rocket :size="15" />继续迁移流程</button><button type="button" title="全自动开通 Pro / 查看进度与停止" :disabled="!!busy || !!account.pro_dead" @click="autoPro?.open(account)"><Rocket :size="15" />全自动开通 Pro</button><button type="button" title="查看账号全流程日志" @click="accountLogs?.open(account)"><History :size="15" />查看账号全流程日志</button><button type="button" title="查看、复制和导出 AT / RT" @click="credentialViewer?.open(account)"><FileKey2 :size="15" />查看、复制和导出 AT / RT</button><button type="button" title="手动授权" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="beginOAuth(account)"><LogIn :size="15" />手动授权</button><button type="button" title="刷新 AT" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || !account.refresh_token_present" @click="runOne(account, 'refresh')"><RefreshCw :size="15" />刷新 AT</button><button type="button" title="获取临时 AT" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="requestTemporaryAT([account])"><KeyRound :size="15" />获取临时 AT</button><button type="button" title="开通 Pro" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || !account.access_token_present" @click="recharge?.open(account)"><CirclePlus :size="15" />开通 Pro</button><button type="button" title="登录获取 RT / AT" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="requestOAuthLogin(account)"><BadgeCheck :size="15" />登录获取 RT / AT</button><button type="button" title="推送 / 重新授权" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || (!account.refresh_token_present && settings.sub2.auth_mode !== 'siwc' && !account.pro_siwc)" @click="runOne(account, 'push')"><Rocket :size="15" />推送 / 重新授权</button><button type="button" title="查询额度" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running' || account.push_status !== 'completed'" @click="runOne(account, 'quota')"><Gauge :size="15" />查询额度</button><button type="button" title="执行或续跑空间合并" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="runOne(account, 'merge')"><LoaderCircle v-if="busy === `merge:${account.email}` || account.pro_workflow_running" class="spin" :size="15" /><ShieldCheck v-else :size="15" />执行或续跑空间合并</button><button type="button" title="执行或续跑删除账户" :disabled="autoStageBusy(account)" @click="runOne(account, 'deactivate')"><Trash2 :size="15"/>执行或续跑删除账户</button><button type="button" title="执行或续跑合并空间2.0" :disabled="autoStageBusy(account) || !!account.pro_merge_v2?.completed_at" @click="runOne(account,'merge_v2')"><ShieldCheck :size="15"/>执行或续跑合并空间2.0</button><button type="button" title="执行或续跑 API 商户" :disabled="autoStageBusy(account) || (!!account.pro_merchant?.completed_at && !!account.pro_merchant?.retired_at)" @click="runOne(account, 'merchant')"><ShieldCheck :size="15"/>执行或续跑 API 商户</button><button type="button" title="移回邮件管理" :disabled="!!busy || account.pro_workflow_running || account.pro_auto?.status === 'running'" @click="returnOne(account)"><RotateCcw :size="15" />移回邮件管理</button></ProActionMenu></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="setPage" @update:page-size="setPageSize" />
      </section>
    </template>

    <form v-else-if="activeTab === 'push'" class="push-grid config-workspace ops-config" @invalid.capture="revealConfigInvalid" @submit.prevent="saveSettings">
      <ConfigNav v-model="configSection" label="Pro 推送平台配置" :items="[['sub2','Sub2','分组 · 模型 · 推送参数'],['cpa','CPA','Management API'],['vm2api','vm2api','槽位 · 规格 · 代理']]" />
      <section class="panel provider-panel" v-show="configSection === 'sub2'" data-config-section="sub2" :class="{ selected: settings.provider === 'sub2' }"><div class="panel-title"><div><span>SUB2</span><h2>Sub2 / Sub2SIWC 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="sub2" />{{ settings.provider === 'sub2' ? '当前启用' : '启用' }}</label></div><div class="settings-fields"><label class="field wide"><span>推送方式</span><select :value="settings.sub2.auth_mode" :disabled="!!busy" @change="switchPushMode"><option value="">Sub2（原 OAuth）</option><option value="siwc">Sub2SIWC（SIWC 授权）</option></select><small>两种方式分别保存连接、分组和代理。SIWC 全自动沿用首次登录会话获取 SIWC RT/AT；切换后点击保存生效。</small></label><label class="field wide"><span>地址</span><input v-model="settings.sub2.url" placeholder="https://sub2.example.com" /></label><label class="field"><span>管理员邮箱</span><input v-model="settings.sub2.email" /></label><label class="field"><span>管理员密码</span><input v-model="settings.sub2_password" type="password" :placeholder="settings.sub2.password_present ? '已保存，留空不修改' : '请输入密码'" /></label><label class="field"><span>账号并发</span><input v-model.number="settings.sub2.account_concurrency" type="number" min="1" max="100" /></label><label class="field"><span>优先级</span><input v-model.number="settings.sub2.priority" type="number" min="1" max="100" /></label><label class="toggle-row wide"><div><strong>WS 推荐配置</strong><small>推送时携带 cpa_ws=1</small></div><input v-model="settings.sub2.cpa_ws" type="checkbox" /><i></i></label></div><Sub2ProxyPicker :key="settings.sub2.auth_mode || 'oauth'" :auth-mode="settings.sub2.auth_mode" v-model:enabled="settings.sub2.bind_proxy" v-model:ids="settings.sub2.proxy_ids" :url="settings.sub2.url" :email="settings.sub2.email" :password="settings.sub2_password" :active="settings.provider === 'sub2'" :disabled="!!busy" endpoint="/api/pro-settings/sub2/proxies" /><div class="group-box"><div><strong>OpenAI 分组</strong><button class="btn ghost compact" type="button" :disabled="!!busy" @click="loadGroups('sub2')"><RefreshCw v-if="busy === 'groups-sub2'" class="spin" :size="14" />{{ busy === 'groups-sub2' ? '读取中' : '读取分组' }}</button></div><p v-if="groupErrors.sub2" role="status">{{ groupErrors.sub2 }}</p><p v-if="!sub2Groups.length">{{ busy === 'groups-sub2' ? '正在读取分组…' : '暂无分组' }}</p><label v-for="group in sub2Groups" :key="group.id"><input v-model="settings.sub2.group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div>
      <div class="group-box post-merge-group-box"><div><strong>合并/删除/API 商户移出后分组</strong><button class="btn ghost compact" type="button" :disabled="!!busy" @click="loadGroups('sub2')">读取分组</button></div><p>可多选；留空保持原分组。空间合并并移出成功后，或删除账户 / API 商户请求受理后，立即替换为所选分组；删除无需等待最终确认。</p><p v-if="!sub2Groups.length">暂无分组</p><label v-for="group in sub2Groups" :key="group.id"><input v-model="settings.post_merge_group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div>
      <ProSIWCSettings v-if="settings.sub2.auth_mode === 'siwc'" :transfer="false"/><label class="field models-field"><span>可用模型 <small>每行或逗号分隔，留空表示不限制</small></span><textarea v-model="sub2Models" rows="4" spellcheck="false" placeholder="gpt-5.2-codex&#10;gpt-5.1-codex-mini"></textarea></label><div class="panel-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="testProvider('sub2')"><RefreshCw v-if="busy === 'test-sub2'" class="spin" :size="14" />{{ busy === 'test-sub2' ? '连接中' : '测试连接' }}</button></div></section>
      <section class="panel provider-panel" v-show="configSection === 'cpa'" data-config-section="cpa" :class="{ selected: settings.provider === 'cpa' }"><div class="panel-title"><div><span>CPA</span><h2>CPA 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="cpa" />{{ settings.provider === 'cpa' ? '当前启用' : '启用' }}</label></div><div class="settings-fields"><label class="field wide"><span>Management API 地址</span><input v-model="settings.cpa.url" placeholder="http://cpa:8317" /></label><label class="field wide"><span>Management Key</span><input v-model="settings.cpa_key" type="password" :placeholder="settings.cpa.key_present ? '已保存，留空不修改' : '请输入 Key'" /></label><label class="toggle-row wide"><div><strong>WS 推荐配置</strong><small>上传 auth 文件时启用 WebSocket</small></div><input v-model="settings.cpa.websockets" type="checkbox" /><i></i></label></div><div class="group-box"><div><strong>账号分组</strong><button class="btn ghost compact" type="button" :disabled="!!busy" @click="loadGroups('cpa')"><RefreshCw v-if="busy === 'groups-cpa'" class="spin" :size="14" />{{ busy === 'groups-cpa' ? '读取中' : '读取分组' }}</button></div><p v-if="groupErrors.cpa" role="status">{{ groupErrors.cpa }}</p><p v-if="!cpaGroups.length">{{ busy === 'groups-cpa' ? '正在读取分组…' : '暂无分组' }}</p><label v-for="group in cpaGroups" :key="group.id"><input v-model="settings.cpa.group_ids" type="checkbox" :value="group.id" />{{ group.name }}</label></div><div class="panel-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="testProvider('cpa')"><RefreshCw v-if="busy === 'test-cpa'" class="spin" :size="14" />{{ busy === 'test-cpa' ? '连接中' : '测试连接' }}</button></div></section>
      <section class="panel provider-panel" v-show="configSection === 'vm2api'" data-config-section="vm2api" :class="{ selected: settings.provider === 'vm2api' }">
        <div class="panel-title"><div><span>vm2api</span><h2>vm2api 设置</h2></div><label class="provider-choice"><input v-model="settings.provider" type="radio" value="vm2api" />{{ settings.provider === 'vm2api' ? '当前启用' : '启用' }}</label></div>
        <div class="settings-fields">
          <label class="field wide"><span>vm2api 地址</span><input v-model="settings.vm2api.url" placeholder="https://vm2api.example.com（不带 /v1）" /></label>
          <label class="field wide"><span>管理密钥（KIN_API_KEY）</span><input v-model="settings.vm2api_key" type="password" autocomplete="new-password" :placeholder="settings.vm2api.key_present ? '已保存，留空不修改' : '填写管理密钥，不是模型调用 Key'" /></label>
          <label class="field"><span>新槽位形态</span><select v-model="settings.vm2api.runtime_type"><option value="docker">容器（Docker）</option><option value="kvm">虚拟机（KVM）</option></select></label>
          <label class="field"><span>系统</span><select v-model="settings.vm2api.kernel"><option value="ubuntu-24.04">Ubuntu 24.04</option><option value="debian-12">Debian 12</option><option value="archlinux">Arch Linux</option><option value="fedora-41">Fedora 41</option></select></label>
          <label class="field"><span>{{ settings.vm2api.runtime_type === 'kvm' ? '虚拟机内存' : '容器内存上限' }}</span><select v-model="settings.vm2api.memory"><option v-for="size in ['256m','512m','1g','2g','4g','8g','16g']" :key="size" :value="size">{{ size.toUpperCase() }}</option></select></label>
          <template v-if="settings.vm2api.runtime_type === 'kvm'">
            <label class="field"><span>vCPU</span><input v-model.number="settings.vm2api.vcpus" type="number" min="1" max="16" /></label>
            <label class="field"><span>磁盘（GB）</span><input v-model.number="settings.vm2api.disk_gb" type="number" min="10" max="200" /></label>
            <p class="table-note wide">KVM 需要服务器支持 /dev/kvm，首次启动可能需要数分钟。每个槽位使用独立虚拟机和 Codex 内核。</p>
          </template>
          <label class="field"><span>地区</span><select v-model="settings.vm2api.region"><option value="">自动</option><option value="us-west">美西</option><option value="us-central">美中</option><option value="us-east">美东</option><option value="eu">欧洲</option><option value="ap-east">亚太东</option><option value="ap-southeast">亚太东南</option></select></label>
          <label class="field"><span>系统语言</span><select v-model="settings.vm2api.locale"><option value="en_US.UTF-8">English</option><option value="zh_CN.UTF-8">中文</option><option value="ja_JP.UTF-8">日本語</option><option value="C">C</option></select></label>
        </div>
        <VM2GroupPicker scope="pro" v-model:ids="settings.vm2api.group_ids" :url="settings.vm2api.url" :management-key="settings.vm2api_key" :active="settings.provider === 'vm2api'" :disabled="!!busy" />
        <VM2ProxyPicker scope="pro" v-model:enabled="settings.vm2api.bind_proxy" v-model:ids="settings.vm2api.proxy_ids" :url="settings.vm2api.url" :management-key="settings.vm2api_key" :active="settings.provider === 'vm2api'" :disabled="!!busy" />
        <p class="table-note">一账号一槽位，独立运行 Codex 内核。未选代理默认使用 vm2api 的 local 本地代理；多选时优先绑定最少的可用代理。</p>
        <p class="table-note">重试、重登沿用原槽位和规格。额度达到 Pro 配置阈值后执行空间合并，移出成功后删除槽位及运行环境，删除失败可重试清理。</p>
        <div class="panel-actions"><button class="btn ghost" type="button" :disabled="!!busy" @click="testProvider('vm2api')"><RefreshCw v-if="busy === 'test-vm2api'" class="spin" :size="14" />{{ busy === 'test-vm2api' ? '连接中' : '测试连接' }}</button></div>
      </section>
      <div class="save-row config-footer"><p class="table-note">仅启用一套作为新账号默认。已绑定 vm2api 的账号保留原连接和槽位，切换设置不会迁移。</p><button class="btn primary" type="submit" :disabled="!!busy"><Settings2 :size="15" />保存推送设置</button></div>
    </form>

    <div v-if="oauth.open" class="modal-backdrop" @click.self="oauth.open = false"><form class="modal oauth-modal" @submit.prevent="finishOAuth"><div class="modal-heading"><div><span class="overline">OPENAI CODEX OAUTH</span><h2>{{ oauth.targetEmail ? '手动授权 Pro 账号' : '添加 Pro 账号' }}</h2></div><button class="icon-button" type="button" title="关闭" @click="oauth.open = false"><X :size="17" /></button></div><p>复制授权链接到需要登录的浏览器。完成 Google/OpenAI 登录后，页面会停在无法访问的 localhost，把地址栏中的完整 URL 粘贴到下方。</p><label class="field"><span>授权链接</span><div class="url-row"><input :value="oauth.authURL" readonly /><button class="btn ghost" type="button" @click="copyOAuthURL">复制链接</button></div></label><label class="field"><span>完整回调 URL</span><textarea v-model="oauth.callbackURL" rows="4" required placeholder="http://localhost:1455/auth/callback?code=...&state=..." /></label><small>会话有效期至 {{ formatTime(oauth.expiresAt) }}，成功后本地加密保存 AT、RT 与 ID Token。</small><div class="panel-actions"><button class="btn ghost" type="button" @click="oauth.open = false">取消</button><button class="btn primary" type="submit" :disabled="oauth.loading"><LoaderCircle v-if="oauth.loading" class="spin" :size="15" /><LogIn v-else :size="15" />完成授权</button></div></form></div>
    <ProGPTPayPanel v-if="['auto', 'cards', 'gptpay'].includes(activeTab)" :key="activeTab" :mode="activeTab === 'auto' ? 'settings' : activeTab === 'gptpay' ? 'orders' : 'cards'" :default-page-size="defaultPageSize" :admin-accounts="adminAccounts" @saved="loadSettings" @open-card="openCard" />
    <ProQualityPanel v-if="activeTab === 'quality'" @updated="loadPage" />
    <ProSchedulePanel v-if="activeTab==='runs'" :default-page-size="defaultPageSize"/>
    <ProTransferDialog ref="transferDialog" :selected="selectedAccounts" :query="query" :merge-state="spaceFilter" @busy="transferBusy" @updated="reload" />
    <ProAutoDialog :default-page-size="defaultPageSize" ref="autoPro" @updated="reload" />
    <ProRechargeDialog :default-page-size="defaultPageSize" ref="recharge" @updated="loadPage" @progress="rechargeProgress" />
    <TemporaryATDialog ref="temporaryAT" @busy="temporaryATBusy" @progress="manualTaskProgress" @finished="temporaryATFinished" />
    <AccountCredentialsDialog ref="credentialViewer" @exported="setMessage($event, 'success')" @saved="loadPage" />
  </section>
</template>

<style scoped>
.space-merge-tabs { display:flex;flex-wrap:wrap;gap:3px;border-bottom:1px solid var(--line);margin-bottom:20px; }
.space-merge-tabs button { display:flex;align-items:center;gap:6px;padding:10px 14px;border-bottom:2px solid transparent;color:var(--muted);font-size:12px; }
.space-merge-tabs button.active {color:var(--green-strong);border-bottom-color:var(--green);}
.auto-flow-cell {min-width:330px;max-width:380px;} .auto-flow-open{display:block;text-align:left;} .auto-flow-cell .table-note {max-width:300px;white-space:normal;overflow-wrap:anywhere;}
.pro-metrics { grid-template-columns: repeat(4, minmax(0, 1fr)); }
.account-toolbar { display: flex; justify-content: space-between; align-items: center; gap: 12px; margin-bottom: 14px; }
.space-filter-tabs { display: flex; flex-wrap: wrap; gap: 8px; }
.space-filter-tabs button { min-height: 32px; padding: 0 11px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 10px; font-weight: 650; }
.space-filter-tabs button.active { border-color: rgba(37,143,97,.35); background: var(--green-bg); color: var(--green-strong); }
.pro-table { min-width: 1570px; }
.activation-card-link { text-align:left;color:var(--blue);border-radius:5px; }
.activation-card-link:hover { text-decoration:underline; }
.activation-card-link:focus-visible { outline:2px solid var(--blue);outline-offset:4px; }
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
.push-grid { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 16px; }
.provider-panel { border-top: 2px solid transparent; min-width: 0; } .provider-panel.selected { border-top-color: var(--green); }
.push-grid .provider-panel .table-note { display: block; width: auto; max-width: 100%; white-space: normal; overflow-wrap: anywhere; line-height: 1.8; }
.push-grid .provider-panel > p.table-note { margin-top: 12px; }
.provider-choice { display: flex; align-items: center; gap: 6px; color: var(--text-2); font-size: 11px; }
.group-box { margin-top: 18px; padding-top: 14px; border-top: 1px solid var(--line); }
.group-box > div { display: flex; justify-content: space-between; align-items: center; }
.group-box p { color: var(--muted); font-size: 11px; }
.group-box label { display: inline-flex; align-items: center; gap: 5px; margin: 10px 12px 0 0; font-size: 11px; }
.models-field { margin-top: 18px; } .models-field textarea { resize: vertical; }
.post-merge-group-status { display: grid; justify-items: start; gap: 5px; margin-top: 8px; max-width: 270px; }
.save-row { grid-column: 1 / -1; display: flex; justify-content: flex-end; }
.countdown { display: inline-flex; align-items: center; gap: 5px; color: var(--muted); font-size: 11px; }
.oauth-modal { width: min(650px, 100%); } .oauth-modal .field { margin-top: 16px; } .oauth-modal textarea { resize: vertical; }
.url-row { display: grid; grid-template-columns: 1fr auto; gap: 8px; } .spin { animation: spin .9s linear infinite; }
@keyframes spin { to { transform: rotate(360deg); } }
@media (max-width: 1000px) { .pro-metrics, .push-grid { grid-template-columns: repeat(2, minmax(0, 1fr)); } .account-toolbar { align-items: stretch; flex-direction: column; } }
@media (max-width: 700px) { .pro-metrics, .push-grid { grid-template-columns: 1fr; } .save-row { grid-column: auto; } }
</style>
