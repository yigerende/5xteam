<script setup>
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { Play, Save, RefreshCw, ChevronRight } from 'lucide-vue-next'
import { api } from '../api'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'
import Pagination from './Pagination.vue'

const props = defineProps({ adminAccounts: { type: Array, default: () => [] } })
const settings = ref({ enabled: false, threshold_percent: 50, interval_seconds: 300, concurrency: 2, max_per_run: 0, retry_count: 1 })
const runs = ref([]); const tasks = ref([]); const events = ref([]); const selectedRun = ref(null); const busy = ref(''); const message = ref({ text: '', type: '' }); const page = ref(1); const pageSize = ref(10)
const clock = ref(Date.now())
let countdownTimer
const pages = computed(() => Math.max(1, Math.ceil(runs.value.length / pageSize.value))); const shownRuns = computed(() => runs.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
function setMessage(text, type = '') { message.value = { text, type } }
async function load() { try { settings.value = await api('/api/auto-rotation/settings'); runs.value = await api('/api/auto-rotation/runs') } catch (e) { setMessage(e.message, 'error') } }
async function save() { busy.value = 'save'; try { settings.value = await api('/api/auto-rotation/settings', { method: 'PUT', body: settings.value }); setMessage('自动轮转配置已保存', 'success') } catch (e) { setMessage(e.message, 'error') } finally { busy.value = '' } }
async function trigger() { busy.value = 'run'; try { await api('/api/auto-rotation/run', { method: 'POST', body: {} }); setMessage('已触发自动轮转批次', 'success'); await load() } catch (e) { setMessage(e.message, 'error') } finally { busy.value = '' } }
async function viewRun(run) { selectedRun.value = run; try { [tasks.value, events.value] = await Promise.all([api(`/api/auto-rotation/runs/${encodeURIComponent(run.id)}/tasks`), api(`/api/auto-rotation/events?run_id=${encodeURIComponent(run.id)}`)]) } catch (e) { setMessage(e.message, 'error') } }
function runStatus(value) { return value === 'completed' ? 'success' : value === 'failed' ? 'danger' : value === 'running' ? 'running' : 'pending' }
const eventTypes = { decision: '自动补充判定', seat_snapshot: '席位决策快照', seat_query: '查询母号席位', seat_check: '实时席位规则检查', seat_reserved: '预占 5x 席位', seat_released: '释放席位', join_trace: '邀请确认诊断', manual_stage: '手动修正阶段', step: '流程步骤', request: '流程请求', retry: '步骤重试', dead_detected: '识别死号', dead_remove_start: '开始移出死号', dead_remove_success: '死号移出成功', dead_remove_failed: '死号移出失败' }
const stageNames = { invite: '邀请并进入空间', oauth: '获取 Codex OAuth', push: '推送 Sub2', quota: '查询额度' }
function eventType(value) { return eventTypes[value] || value || '系统事件' }
function eventStage(value) { return stageNames[value] || value || '-' }
function taskEmail(taskID) { return tasks.value.find((item) => item.id === taskID)?.email || '-' }
function adminName(adminID) { const item = props.adminAccounts.find((value) => String(value.id) === String(adminID)); return item ? (item.label || item.email) : (adminID || '-') }
function eventSubject(event) { return event.account_id ? (taskEmail(event.task_id) !== '-' ? taskEmail(event.task_id) : event.account_id) : (event.admin_account_id ? adminName(event.admin_account_id) : '-') }
function eventMessage(event) {
  const details = event.details || {}
  if (event.type === 'seat_query') return `远端剩余 ${details.remote_remaining ?? '-'}，本地在途 ${details.local_reserved ?? 0}，实际可分配 ${details.available ?? 0}`
  if (event.type === 'seat_check') return `远端总数 ${details.remote_total ?? '-'}，空间内 ${details.inside_premium ?? 0}，邀请在途 ${details.in_flight_invites ?? 0}，可分配 ${details.available ?? 0}`
  if (event.type === 'join_trace') {
    const parts = []
    if (details.http_status) parts.push(`HTTP ${details.http_status}`)
    if (details.duration_ms != null) parts.push(`耗时 ${details.duration_ms} ms`)
    if (details.error) parts.push(`错误：${details.error}`)
    if (details.team_account_id) parts.push(`空间 ${details.team_account_id}`)
    if (details.user_id) parts.push(`用户 ${details.user_id}`)
    return parts.join('，') || event.message || '-'
  }
  if (event.type === 'manual_stage') return `状态：${details.message || event.from_status || '-'}${event.from_status ? ` → ${event.to_status}` : ''}`
  if (event.type === 'dead_detected') return `来源 ${details.source === 'relogin' ? '401 重登' : 'Codex OAuth'}，原因 ${details.error_code || details.reason || '-'}`
  if (event.type === 'dead_remove_failed') return details.error || event.message || '自动移出失败'
  if (event.type === 'dead_remove_start' || event.type === 'dead_remove_success') return details.dead_reason || event.message || '-'
  if (event.type === 'seat_snapshot') return `本轮可用 ${details.available_after_reservation ?? 0}，每轮上限 ${details.max_per_run > 0 ? details.max_per_run : '不限'}`
  if (event.type === 'retry') return `第 ${event.attempt || '-'} 次重试`
  return event.message || '-'
}
const nextCheckSeconds = computed(() => {
  if (!settings.value.enabled) return null
  const interval = Math.max(10, Number(settings.value.interval_seconds) || 300)
  const latest = runs.value[0]?.started_at ? new Date(runs.value[0].started_at).getTime() : 0
  if (!latest || !Number.isFinite(latest)) return interval
  return Math.max(0, Math.ceil(interval - (clock.value - latest) / 1000))
})
function countdownText() { return nextCheckSeconds.value == null ? '已关闭' : `${nextCheckSeconds.value}s` }
onMounted(() => { countdownTimer = window.setInterval(() => { clock.value = Date.now() }, 1000); load() })
onBeforeUnmount(() => window.clearInterval(countdownTimer))
</script>
<template>
  <section class="page-section auto-rotation-view">
    <div class="panel-title responsive"><div><span>AUTO ROTATION</span><h2>全自动轮转</h2><p class="panel-description">额度低于阈值后，优先处理已加入轮转但尚未邀请的账号，不足时再从邮件管理导入。</p></div><div class="heading-actions"><span class="auto-countdown">下次检查 <strong>{{ countdownText() }}</strong></span><StatusPill :tone="settings.enabled ? 'success' : 'pending'">{{ settings.enabled ? '已开启' : '已关闭' }}</StatusPill><button class="btn ghost" :disabled="!!busy" @click="load"><RefreshCw :size="15" />刷新</button><button class="btn primary" :disabled="!!busy" @click="trigger"><Play :size="15" />立即执行</button></div></div>
    <MessageBar :message="message" />
    <form class="panel auto-config" @submit.prevent="save"><div class="auto-fields"><label class="field checkbox-field"><span>自动轮转开关<small>后台定时检查并补充账号</small></span><input v-model="settings.enabled" type="checkbox" /></label><label class="field"><span>7天平均剩余额度阈值（%）</span><input v-model.number="settings.threshold_percent" type="number" min="1" max="100" required /></label><label class="field"><span>检查间隔（秒）</span><input v-model.number="settings.interval_seconds" type="number" min="10" max="86400" required /></label><label class="field"><span>最大并发数</span><input v-model.number="settings.concurrency" type="number" min="1" max="20" required /></label><label class="field"><span>每轮最大补充数（0 不限制）</span><input v-model.number="settings.max_per_run" type="number" min="0" max="500" required /></label><label class="field"><span>单账号重试次数</span><input v-model.number="settings.retry_count" type="number" min="0" max="10" required /></label></div><div class="panel-actions"><button class="btn primary" :disabled="!!busy" type="submit"><Save :size="15" />保存配置</button></div></form>
    <section class="panel list-panel"><div class="panel-title"><div><span>RUN HISTORY</span><h2>轮转执行记录</h2></div><StatusPill tone="pending">{{ runs.length }} 个批次</StatusPill></div><div class="table-shell"><table><thead><tr><th>开始时间</th><th>触发原因</th><th>平均剩余</th><th>席位</th><th>计划/成功/失败</th><th>状态</th><th>操作</th></tr></thead><tbody><tr v-if="!shownRuns.length"><td colspan="7" class="empty-cell">暂无自动轮转记录</td></tr><tr v-for="run in shownRuns" :key="run.id"><td>{{ new Date(run.started_at).toLocaleString() }}</td><td>{{ run.reason || '-' }}</td><td>{{ run.average_percent < 0 ? '未统计' : `${run.average_percent.toFixed(1)}%` }}</td><td>{{ run.seat_remaining }} / {{ run.seat_total }}</td><td>{{ run.planned }} / {{ run.succeeded }} / {{ run.failed }}</td><td><StatusPill :tone="runStatus(run.status)">{{ run.status }}</StatusPill></td><td><button class="btn ghost compact" @click="viewRun(run)"><ChevronRight :size="14" />查看详情</button></td></tr></tbody></table></div><Pagination :page="page" :page-size="pageSize" :total="runs.length" @update:page="page = $event" @update:page-size="pageSize = $event" /></section>
    <section v-if="selectedRun" class="panel list-panel"><div class="panel-title"><div><span>BATCH DETAILS</span><h2>批次 {{ selectedRun.id }}</h2></div><button class="btn ghost" @click="selectedRun = null">关闭</button></div><div class="table-shell"><table><thead><tr><th>账号</th><th>来源</th><th>母号</th><th>当前步骤</th><th>邀请在途</th><th>状态</th><th>错误</th></tr></thead><tbody><template v-for="task in tasks" :key="task.id"><tr><td>{{ task.email }}</td><td>{{ task.source }}</td><td>{{ task.admin_account_id || '-' }}</td><td>{{ task.current_step || '-' }}</td><td>{{ task.invite_triggered ? '是' : '否' }}</td><td><StatusPill :tone="runStatus(task.status)">{{ task.status }}</StatusPill></td><td>{{ task.error || '-' }}</td></tr><tr><td colspan="7"><div class="task-steps"><span v-for="step in task.steps" :key="step.key" :class="['task-step', `step-${runStatus(step.status)}`]" :title="step.message || step.name">{{ step.name }}：{{ step.status }}</span></div></td></tr></template></tbody></table></div></section>
    <section v-if="selectedRun" class="panel list-panel"><div class="panel-title"><div><span>EVENT LOG</span><h2>执行事件</h2><p class="panel-description">按时间记录本轮的判定、席位、账号步骤、请求耗时和重试，不显示内部原始字段。</p></div><StatusPill tone="pending">{{ events.length }} 条</StatusPill></div><div class="table-shell"><table><thead><tr><th>时间</th><th>事件</th><th>流程阶段</th><th>账号 / 母号</th><th>耗时</th><th>详情</th></tr></thead><tbody><tr v-if="!events.length"><td colspan="6" class="empty-cell">暂无事件</td></tr><tr v-for="event in events" :key="event.id"><td>{{ new Date(event.created_at).toLocaleString() }}</td><td>{{ eventType(event.type) }}</td><td>{{ eventStage(event.stage) }}</td><td>{{ eventSubject(event) }}</td><td>{{ event.duration_ms ? `${event.duration_ms} ms` : '-' }}</td><td>{{ eventMessage(event) }}</td></tr></tbody></table></div></section>
  </section>
</template>
<style scoped>
.auto-config { margin: 14px 0; }
.auto-fields { display: grid; grid-template-columns: repeat(3, minmax(180px, 1fr)); gap: 0 14px; }
.compact { min-height: 28px; padding: 0 8px; }
.auto-countdown { display: inline-flex; align-items: center; min-height: 30px; padding: 0 9px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--muted); font-size: 10px; white-space: nowrap; }
.auto-countdown strong { margin-left: 4px; color: var(--blue); font-variant-numeric: tabular-nums; }
.task-steps { display: flex; flex-wrap: wrap; gap: 6px; padding: 2px 0; }
.task-step { padding: 4px 7px; border: 1px solid var(--line); border-radius: 4px; font-size: 10px; }
.step-success { border-color: rgba(37,143,97,.3); color: var(--green-strong); background: var(--green-bg); }
.step-danger { border-color: rgba(219,112,112,.3); color: var(--red); background: var(--red-bg); }
.step-running { border-color: rgba(54,125,158,.3); color: var(--blue); background: var(--blue-bg); }
@media (max-width: 800px) { .auto-fields { grid-template-columns: 1fr; } }
</style>
