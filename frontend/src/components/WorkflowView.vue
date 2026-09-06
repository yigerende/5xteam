<script setup>
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import {
  ArrowLeftRight, Check, FileJson, FolderOpen, LogIn, Play, RotateCcw, ScanLine, Square, Upload, UsersRound,
} from 'lucide-vue-next'
import { api } from '../api'
import { extractAccessTokens, extractTokensFromFiles, progressLabel, shortID, statusText } from '../utils'
import StatusPill from './StatusPill.vue'
import MessageBar from './MessageBar.vue'

const props = defineProps({
  mode: { type: String, required: true },
  adminAccounts: { type: Array, default: () => [] },
  anyActive: Boolean,
})
const emit = defineEmits(['job-state', 'history-changed', 'navigate'])

const spaceMergeTabs = [
  { id: 'full', label: '任务台', icon: UsersRound },
  { id: 'enter', label: '进入空间', icon: LogIn },
  { id: 'transfer', label: '合并空间', icon: ArrowLeftRight },
  { id: 'kick', label: '移出空间', icon: UsersRound },
]

const configs = {
  full: {
    title: '空间合并任务', subtitle: '完整执行邀请、接受、合并与移出流程', eyebrow: 'AUTOMATION FLOW',
    action: '开始执行', confirm: '完整空间合并流程', steps: ['invite', 'accept', 'transfer', 'kick'], seat: true, history: false,
  },
  enter: {
    title: '进入空间', subtitle: '邀请子号并接受加入团队空间', eyebrow: 'TEAM ENTRY',
    action: '开始进入', confirm: '邀请并接受进入空间', steps: ['invite', 'accept'], seat: true, history: true,
  },
  transfer: {
    title: '合并空间', subtitle: '将子号个人空间迁移到指定团队', eyebrow: 'SPACE TRANSFER',
    action: '开始合并', confirm: '合并个人空间', steps: ['transfer'], seat: false, history: true,
  },
  kick: {
    title: '移出空间', subtitle: '从团队中移出指定子号', eyebrow: 'MEMBER REMOVAL',
    action: '开始移出', confirm: '从团队移出', steps: ['kick'], seat: false, history: true,
  },
}
const config = computed(() => configs[props.mode])
const form = reactive({ admin: '', team: '', seatType: 'default', tokenText: '' })
const inspection = ref(null)
const progressRecords = ref([])
const job = ref(null)
const message = reactive({ text: '', type: '' })
const busy = ref(false)
const confirmOpen = ref(false)
const fileInput = ref(null)
const folderInput = ref(null)
let pollTimer

const tokens = computed(() => form.tokenText.split(/\s+/).map((item) => item.trim()).filter(Boolean))
const validUsers = computed(() => inspection.value?.users?.filter((item) => !item.error).length || 0)
const invalidUsers = computed(() => (inspection.value?.users?.length || 0) - validUsers.value)
const jobActive = computed(() => job.value && ['queued', 'running', 'cancelling'].includes(job.value.status))
const progressPercent = computed(() => job.value?.total ? Math.round((job.value.completed / job.value.total) * 100) : 0)
const progressByUser = computed(() => new Map(progressRecords.value.map((item) => [item.user_id, item])))
const selectedAdmin = computed(() => props.adminAccounts.find((item) => item.id === form.admin))
const teamID = computed(() => form.team || selectedAdmin.value?.team_account_id || inspection.value?.admin?.account_id || '')

function setMessage(text = '', type = '') {
  message.text = text
  message.type = type
}

function parseJSON(showMessage = true) {
  const raw = form.tokenText.trim()
  if (!raw) {
    if (showMessage) setMessage('请先粘贴 JSON 内容', 'error')
    return []
  }
  try {
    const found = extractAccessTokens(JSON.parse(raw))
    if (!found.length) throw new Error('JSON 中没有找到 Access Token')
    form.tokenText = found.join('\n')
    if (showMessage) setMessage(`已解析出 ${found.length} 个 Access Token`, 'success')
    return found
  } catch (error) {
    if (showMessage) setMessage(`JSON 解析失败：${error.message}`, 'error')
    return []
  }
}

async function importFile(event) {
  const file = event.target.files?.[0]
  event.target.value = ''
  if (!file) return
  try {
    form.tokenText = await file.text()
    parseJSON()
  } catch (error) {
    setMessage(`读取 JSON 文件失败：${error.message}`, 'error')
  }
}

async function importFolder(event) {
  const result = await extractTokensFromFiles(event.target.files)
  event.target.value = ''
  if (!result.files.length) return setMessage('所选文件夹中没有 JSON 文件', 'error')
  if (!result.tokens.length) return setMessage('JSON 文件中没有找到 Access Token', 'error')
  form.tokenText = result.tokens.join('\n')
  setMessage(`已从 ${result.files.length} 个文件解析出 ${result.tokens.length} 个 Token${result.invalid ? `，跳过 ${result.invalid} 个无效文件` : ''}`, 'success')
}

async function inspect() {
  if ((form.tokenText.trim().startsWith('{') || form.tokenText.trim().startsWith('[')) && !parseJSON(false).length) {
    setMessage('JSON 中没有找到有效的 Access Token', 'error')
    return null
  }
  if (!form.admin || !tokens.value.length) {
    setMessage('请选择母号并输入子号 Access Token', 'error')
    return null
  }
  busy.value = true
  try {
    const result = await api('/api/tokens/inspect', {
      method: 'POST', body: { admin_account_id: form.admin, admin_token: '', user_tokens: tokens.value },
    })
    inspection.value = result
    progressRecords.value = config.value.history && teamID.value
      ? await api(`/api/account-progress?team_account_id=${encodeURIComponent(teamID.value)}`)
      : []
    const invalid = result.users.filter((item) => item.error).length + (result.admin_error ? 1 : 0)
    setMessage(invalid ? `解析完成，发现 ${invalid} 项无效凭据` : '凭据解析完成，可以启动任务', invalid ? 'error' : 'success')
    return result
  } catch (error) {
    setMessage(error.message, 'error')
    return null
  } finally {
    busy.value = false
  }
}

async function prepareStart() {
  if (props.anyActive && !jobActive.value) return setMessage('已有任务正在执行，请等待或先停止', 'error')
  const result = await inspect()
  if (!result?.admin || result.users.some((item) => item.error)) return setMessage('请先修正无效凭据', 'error')
  confirmOpen.value = true
}

async function start() {
  confirmOpen.value = false
  busy.value = true
  const payload = {
    admin_account_id: form.admin,
    admin_token: '',
    user_tokens: tokens.value,
    team_account_id: form.team.trim(),
  }
  if (config.value.seat) payload.seat_type = form.seatType
  try {
    const path = props.mode === 'full' ? '/api/jobs' : `/api/jobs/${props.mode}`
    job.value = await api(path, { method: 'POST', body: payload })
    emit('job-state', props.mode, true)
    setMessage(`任务 ${job.value.id} 已启动`, 'success')
    schedulePoll()
  } catch (error) {
    setMessage(error.message, 'error')
  } finally {
    busy.value = false
  }
}

function schedulePoll() {
  clearTimeout(pollTimer)
  pollTimer = setTimeout(poll, 700)
}

async function poll() {
  if (!job.value) return
  try {
    job.value = await api(`/api/jobs/${encodeURIComponent(job.value.id)}`)
    if (jobActive.value) schedulePoll()
    else {
      emit('job-state', props.mode, false)
      emit('history-changed')
    }
  } catch (error) {
    setMessage(error.message, 'error')
  }
}

async function cancel() {
  if (!job.value || !window.confirm('确认停止当前任务？')) return
  try {
    await api(`/api/jobs/${encodeURIComponent(job.value.id)}/cancel`, { method: 'POST', body: {} })
    job.value.status = 'cancelling'
    schedulePoll()
  } catch (error) {
    setMessage(error.message, 'error')
  }
}

function reset() {
  if (jobActive.value) return setMessage('请先停止当前任务', 'error')
  Object.assign(form, { admin: '', team: '', seatType: 'default', tokenText: '' })
  inspection.value = null
  progressRecords.value = []
  job.value = null
  setMessage()
}

function stepFor(result, key) {
  return result.steps?.find((step) => step.key === key)
}

function stepSymbol(step) {
  return { completed: '✓', failed: '×', running: '…', cancelled: '■', pending: '·' }[step?.status] || '·'
}

function resultMessage(result) {
  return result.error || [...(result.steps || [])].reverse().find((step) => step.message)?.message || '-'
}

onBeforeUnmount(() => clearTimeout(pollTimer))
</script>

<template>
  <section class="view-stack">
    <header class="page-heading">
      <div>
        <span class="overline">{{ config.eyebrow }}</span>
        <h1>{{ config.title }}</h1>
        <p>{{ config.subtitle }}</p>
      </div>
      <div class="heading-actions">
        <StatusPill :tone="jobActive ? 'running' : 'success'">{{ jobActive ? 'Live' : 'Ready' }}</StatusPill>
        <button class="btn ghost" type="button" @click="reset"><RotateCcw :size="15" />重置</button>
        <button class="btn primary" type="button" :disabled="busy" @click="prepareStart"><Play :size="15" />{{ config.action }}</button>
      </div>
    </header>

    <nav class="space-merge-tabs" role="tablist" aria-label="空间合并菜单"><button v-for="item in spaceMergeTabs" :key="item.id" type="button" role="tab" :aria-selected="props.mode === item.id" :class="{ active: props.mode === item.id }" @click="emit('navigate', item.id)"><component :is="item.icon" :size="15" />{{ item.label }}</button></nav>

    <div class="metric-grid">
      <article class="metric-card blue"><UsersRound :size="17" /><div><span>母号状态</span><strong>{{ inspection?.admin ? '已就绪' : '待解析' }}</strong><small>{{ inspection?.admin?.email || '尚未选择凭据' }}</small></div></article>
      <article class="metric-card green"><Check :size="17" /><div><span>子号数量</span><strong>{{ inspection?.users?.length || tokens.length }}</strong><small>{{ inspection ? `${validUsers} 有效 · ${invalidUsers} 无效` : '等待输入' }}</small></div></article>
      <article class="metric-card amber"><ScanLine :size="17" /><div><span>任务进度</span><strong>{{ job ? `${job.completed} / ${job.total}` : '0 / 0' }}</strong><small>{{ job ? statusText[job.status] : '尚未开始' }}</small></div></article>
      <article class="metric-card slate"><Square :size="17" /><div><span>成功 / 失败</span><strong>{{ job ? `${job.succeeded} / ${job.failed}` : '0 / 0' }}</strong><small>本次任务</small></div></article>
    </div>

    <div class="workflow-grid">
      <form class="panel credential-panel" @submit.prevent="prepareStart">
        <div class="panel-title"><div><span>INPUT</span><h2>凭据与团队</h2></div><StatusPill :tone="inspection?.admin && !invalidUsers ? 'success' : 'pending'">{{ inspection?.admin && !invalidUsers ? '校验通过' : '未校验' }}</StatusPill></div>
        <label class="field"><span>母号配置</span><select v-model="form.admin" required><option value="">请选择母号配置</option><option v-for="account in adminAccounts" :key="account.id" :value="account.id">{{ account.label }} · {{ account.email }} · {{ shortID(account.team_account_id) }}</option></select></label>
        <div class="field-row">
          <label class="field"><span>团队 Account ID <small>可留空自动读取</small></span><input v-model="form.team" placeholder="account-..." /></label>
          <label v-if="config.seat" class="field"><span>本次邀请席位</span><select v-model="form.seatType"><option value="default">Standard（标准）</option><option value="prolite">Premium（5x）</option></select></label>
        </div>
        <label class="field grow"><span>子号 Access Token <small>每行一个，支持 Sub2API / CPA JSON</small></span><textarea v-model="form.tokenText" rows="10" spellcheck="false"></textarea></label>
        <input ref="fileInput" hidden type="file" accept=".json,application/json" @change="importFile" />
        <input ref="folderInput" hidden type="file" accept=".json,application/json" webkitdirectory directory multiple @change="importFolder" />
        <div class="compact-actions">
          <button class="btn ghost" type="button" @click="fileInput.click()"><FileJson :size="15" />JSON 文件</button>
          <button class="btn ghost" type="button" @click="folderInput.click()"><FolderOpen :size="15" />JSON 文件夹</button>
          <button class="btn ghost" type="button" @click="parseJSON()"><Upload :size="15" />解析 JSON</button>
        </div>
        <MessageBar :message="message" />
        <div class="panel-actions"><button class="btn ghost" type="button" :disabled="busy" @click="inspect"><ScanLine :size="15" />解析凭据</button><button class="btn primary" type="submit" :disabled="busy"><Play :size="15" />{{ config.action }}</button></div>
      </form>

      <section class="panel preview-panel">
        <div class="panel-title"><div><span>PREVIEW</span><h2>凭据预览</h2></div><span class="muted-count">{{ inspection?.users?.length || 0 }} 个子号</span></div>
        <div v-if="inspection?.admin" class="identity-strip"><div class="avatar">{{ (inspection.admin.name || inspection.admin.email || 'A').slice(0, 1).toUpperCase() }}</div><div><strong>{{ inspection.admin.name || inspection.admin.email }}</strong><small>{{ inspection.admin.email }} · {{ inspection.admin.plan_type || '未知计划' }}</small></div><StatusPill tone="success">有效</StatusPill></div>
        <div v-else class="identity-strip empty">母号信息将在这里显示</div>
        <div class="table-shell preview-table">
          <table>
            <thead><tr><th>#</th><th>子号</th><th>计划</th><th v-if="config.history">历史阶段</th><th>状态</th></tr></thead>
            <tbody>
              <tr v-if="!inspection?.users?.length"><td :colspan="config.history ? 5 : 4" class="empty-cell">等待解析凭据</td></tr>
              <tr v-for="item in inspection?.users || []" :key="item.index">
                <td>{{ item.index }}</td><td class="account-cell"><strong>{{ item.error ? `第 ${item.index} 行` : item.user.email }}</strong><small>{{ item.error || shortID(item.user.user_id) }}</small></td><td>{{ item.error ? '-' : (item.user.plan_type || '-') }}</td>
                <td v-if="config.history"><StatusPill :tone="progressByUser.get(item.user?.user_id) ? 'success' : 'pending'">{{ progressLabel(progressByUser.get(item.user?.user_id)) }}</StatusPill></td>
                <td><StatusPill :tone="item.error ? 'danger' : 'success'">{{ item.error ? '无效' : '有效' }}</StatusPill></td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>
    </div>

    <section v-if="job" class="panel job-panel">
      <div class="panel-title"><div><span>EXECUTION</span><h2>执行轨迹</h2></div><div class="heading-actions"><StatusPill :tone="job.status">{{ statusText[job.status] || job.status }}</StatusPill><button class="btn danger" type="button" :disabled="!jobActive" @click="cancel"><Square :size="14" />停止</button></div></div>
      <div class="progress-line"><i :style="{ width: `${progressPercent}%` }"></i></div>
      <div class="progress-caption"><span>{{ job.completed }} / {{ job.total }}（{{ progressPercent }}%）</span><span>团队 {{ shortID(job.team_account_id) }}</span></div>
      <div class="table-shell result-table"><table><thead><tr><th>#</th><th>子号</th><th>状态</th><th v-for="key in config.steps" :key="key">{{ { invite: '邀请', accept: '接受', transfer: '合并', kick: '移出' }[key] }}</th><th>结果</th></tr></thead><tbody><tr v-for="result in job.results" :key="result.index"><td>{{ result.index }}</td><td class="account-cell"><strong>{{ result.user.email || `第 ${result.index} 行` }}</strong><small>{{ shortID(result.user.user_id) }}</small></td><td><StatusPill :tone="result.status">{{ statusText[result.status] || result.status }}</StatusPill></td><td v-for="key in config.steps" :key="key"><span class="step-dot" :class="stepFor(result, key)?.status" :title="stepFor(result, key)?.message">{{ stepSymbol(stepFor(result, key)) }}</span></td><td class="result-message">{{ resultMessage(result) }}</td></tr></tbody></table></div>
    </section>

    <div v-if="confirmOpen" class="modal-backdrop" @click.self="confirmOpen = false"><section class="modal"><span class="overline">CONFIRM EXECUTION</span><h2>启动{{ config.title }}</h2><p>将使用团队 {{ shortID(teamID) }} 对 {{ tokens.length }} 个子号执行“{{ config.confirm }}”。</p><div class="panel-actions"><button class="btn ghost" type="button" @click="confirmOpen = false">取消</button><button class="btn primary" type="button" @click="start"><Play :size="15" />确认启动</button></div></section></div>
  </section>
</template>
