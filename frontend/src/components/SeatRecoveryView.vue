<script setup>
import { computed, onMounted, onUnmounted, reactive, ref } from 'vue'
import { RefreshCw, Save, X } from 'lucide-vue-next'
import { api } from '../api'
import { formatTime } from '../utils'
import MessageBar from './MessageBar.vue'

const props = defineProps({ accounts: { type: Array, default: () => [] }, initialAdmin: { type: String, default: '' } })
const settings = reactive({ join_method: 'mother_invite', remove_method: 'mother_kick', dwell_minutes: 5, operation_interval_seconds: 10, next_interval_seconds: 10, concurrency: 2 })
const adminIDs = ref([props.initialAdmin || props.accounts[0]?.id].filter(Boolean))
const adminSearch = ref('')
const visibleAdmins = computed(() => props.accounts.filter(p => `${p.label} ${p.email}`.toLowerCase().includes(adminSearch.value.trim().toLowerCase())))
const message = reactive({ text: '', type: '' })
const candidates = ref([]), selected = ref([]), scanned = ref(false)
const motherProgress = reactive({ done: 0, total: 0, errors: [] })
const tasks = ref([]), running = ref({}), page = ref(1), total = ref(0), activeOnly = ref(true)
const scanning = ref(false), starting = ref(false), saving = ref(false), loading = ref(false)
const controlBusy = ref(new Set())
const taskSelected = ref([])
const batch = reactive({ open: false, busy: false, action: '', items: [], error: '' })
const actionNames = { pause: '暂停', resume: '继续', delete: '删除', mother_kick: '踢出', child_leave: '退出' }
const manualAction = action => ['mother_kick', 'child_leave'].includes(action)
const pageSelected = computed(() => tasks.value.length > 0 && tasks.value.every(p => taskSelected.value.includes(p.id)))
const batchDone = computed(() => batch.items.filter(p => p.done).length)
let batchVersion = 0
const clock = ref(Date.now())
let anchorServer = Date.now(), anchorLocal = performance.now(), timer, pollTimer, disposed = false
const log = reactive({ open: false, taskID: '', email: '', items: [], busy: false, more: false, error: '' })
const eligible = computed(() => candidates.value.filter(p => p.eligible))
const allSelected = computed(() => eligible.value.length > 0 && eligible.value.every(p => selected.value.includes(p.key)))
const pages = computed(() => Math.max(1, Math.ceil(total.value / 50)))
const stageNames = { login: '准备 AT', join: '进入请求', confirm: '同意', dwell: '停留', switch: '切换普通', leave: '移出', verify: '核实', cooldown: '间隔', completed: '完成', manual_remove: '手动移出', manual_cooldown: '移出间隔', manual_done: '手动结束' }
const stages = ['login', 'join', 'confirm', 'dwell', 'switch', 'leave', 'verify', 'cooldown']
function stageName(p, stage = p.stage) {
  if (stage === 'join') return p.settings.join_method === 'child_request' ? '申请' : '邀请'
  if (stage === 'confirm') return p.settings.join_method === 'child_request' ? '母号同意' : '子号同意'
  if (stage === 'leave') return p.settings.remove_method === 'child_leave' ? '退出' : '踢出'
  return stageNames[stage] || stage
}
function elapsed(p) {
  if (!p.joined_at) return '—'
  const end = p.left_at ? Date.parse(p.left_at) : clock.value
  return `${Math.max(0, (end - Date.parse(p.joined_at)) / 60000).toFixed(1)} 分钟`
}
function remaining(p) {
  if (!p.due_at || p.left_at) return '—'
  return `${Math.max(0, (Date.parse(p.due_at) - clock.value) / 60000).toFixed(1)} 分钟`
}
function statusLabel(p) {
  if (p.status === 'failed') return '失败，待重试'
  if (p.finished) return p.status === 'cancelled' ? '已取消' : '已完成'
  if (p.paused) return p.lane ? '暂停已请求 · 继续释放普通席位' : '已暂停'
  if (running.value[p.id]) return '执行中'
  return p.status === 'waiting' ? '等待中' : '排队中'
}
function taskStages(p) { return p.manual_remove_method ? ['manual_remove', 'manual_cooldown', 'manual_done'] : stages }
function togglePage(checked) {
  const ids = new Set(taskSelected.value)
  tasks.value.forEach(p => checked ? ids.add(p.id) : ids.delete(p.id))
  taskSelected.value = [...ids]
}
function toggleAdmins(checked) { adminIDs.value = checked ? visibleAdmins.value.map(p => p.id) : []; resetScan() }
async function loadTasks() {
  if (loading.value || disposed) return
  loading.value = true
  try {
    const data = await api(`/api/seat-recovery/tasks?page=${page.value}&active=${activeOnly.value}`)
    if (disposed) return
    tasks.value = data.items || []; total.value = data.total || 0; running.value = data.running || {}
    anchorServer = Date.parse(data.server_now); anchorLocal = performance.now(); clock.value = anchorServer
    if (page.value > pages.value) page.value = pages.value
  } catch (e) { message.text = e.message; message.type = 'error' }
  finally { loading.value = false }
}
async function save() {
  saving.value = true
  try { Object.assign(settings, await api('/api/seat-recovery/settings', { method: 'PUT', body: settings })); message.text = '配置已保存，仅用于新建的恢复任务'; message.type = 'success' }
  catch (e) { message.text = e.message; message.type = 'error' }
  finally { saving.value = false }
}
function resetScan() { candidates.value = []; selected.value = []; scanned.value = false; motherProgress.errors = [] }
async function forMothers(items, action) {
  let index = 0
  await Promise.all(Array.from({ length: Math.min(4, items.length) }, async () => {
    while (index < items.length) {
      const item = items[index++]
      try { await action(item) } catch (e) { motherProgress.errors.push(`${item.label}：${e.message}`) }
      finally { motherProgress.done++ }
    }
  }))
}
async function scan(execute = false) {
  if (!adminIDs.value.length || scanning.value || starting.value) return
  scanning.value = true; resetScan(); message.text = '正在通过母号专属代理扫描实际临停成员…'; message.type = 'info'
  const mothers = props.accounts.filter(p => adminIDs.value.includes(p.id))
  motherProgress.done = 0; motherProgress.total = mothers.length
  try {
    await forMothers(mothers, async mother => {
      const data = await api(`/api/admin-accounts/${encodeURIComponent(mother.id)}/seat-recovery`)
      candidates.value.push(...(data.items || []).map(p => ({ ...p, admin_id: mother.id, admin_label: mother.label, team_id: data.team_id, key: `${mother.id}:${p.id}` })))
    })
    scanned.value = true
    message.text = `扫描到 ${candidates.value.length} 个临停子号，其中 ${eligible.value.length} 个可恢复`; message.type = 'success'
  } catch (e) { message.text = e.message; message.type = 'error' }
  finally { scanning.value = false }
  if (execute && eligible.value.length) await start(true)
}
async function start(all = false) {
  const targets = eligible.value.filter(p => all || selected.value.includes(p.key))
  if (!targets.length || starting.value) return
  starting.value = true; message.text = '正在重新核实临停名单并创建恢复任务…'; message.type = 'info'
  const groups = [...new Set(targets.map(p => p.admin_id))].map(id => ({ id, label: targets.find(p => p.admin_id === id).admin_label, items: targets.filter(p => p.admin_id === id) }))
  motherProgress.done = 0; motherProgress.total = groups.length
  let count = 0
  try {
    await forMothers(groups, async group => {
      const data = await api(`/api/admin-accounts/${encodeURIComponent(group.id)}/seat-recovery`, { method: 'POST', body: { emails: group.items.map(p => p.email), team_id: group.items[0].team_id } })
      const created = new Set((data.items || []).map(p => p.email))
      count += created.size
      candidates.value = candidates.value.map(p => created.has(p.email) ? { ...p, eligible: false, reason: '已加入恢复任务（同一子号不可同时拉回多个母号）' } : p)
      selected.value = selected.value.filter(key => candidates.value.some(p => p.key === key && p.eligible))
      motherProgress.errors.push(...Object.entries(data.failures || {}).map(([email, reason]) => `${group.label} / ${email}：${reason}`))
    })
    message.text = `已创建 ${count} 个恢复任务，后台继续执行`; message.type = motherProgress.errors.length ? 'error' : 'success'
    page.value = 1; activeOnly.value = true; await loadTasks()
  } catch (e) { message.text = e.message; message.type = 'error' }
  finally { starting.value = false }
}
async function control(p, action) {
  if (manualAction(action)) { await batchControl(action, [p.id]); return }
  controlBusy.value = new Set([...controlBusy.value, p.id])
  try { await api(`/api/seat-recovery/tasks/${encodeURIComponent(p.id)}/control`, { method: 'POST', body: { action } }); await loadTasks() }
  catch (e) { message.text = e.message; message.type = 'error' }
  finally { const next = new Set(controlBusy.value); next.delete(p.id); controlBusy.value = next }
}
async function batchControl(action, ids = [...taskSelected.value]) {
  if (!ids.length || batch.busy) return
  if (action === 'delete' && !window.confirm(`删除选中的 ${ids.length} 条恢复任务及日志？不会删除邮件账号或操作远端成员。未结束且已发起远端操作的任务需先踢出/退出。`)) return
  if (manualAction(action) && !window.confirm(`对选中的 ${ids.length} 个子号执行${actionNames[action]}？将跳过停留和换席，直接移出；5x 席位可能重新临停。`)) return
  Object.assign(batch, { open: true, busy: true, action, items: ids.map(id => ({ id, email: tasks.value.find(p => p.id === id)?.email || id, message: '正在提交', done: false })), error: '' })
  batchVersion++
  try {
    const data = await api('/api/seat-recovery/tasks/batch-control', { method: 'POST', body: { ids, action } })
    batch.items = (data.results || []).map(item => ({ ...item, done: !item.ok || !manualAction(action), message: item.error || (manualAction(action) ? '已排队，等待执行' : `${actionNames[action]}成功`) }))
    if (action === 'delete') { const removed = new Set(batch.items.filter(p => p.ok).map(p => p.id)); taskSelected.value = taskSelected.value.filter(id => !removed.has(id)) }
    await loadTasks()
  } catch (e) { batch.error = e.message }
  finally { batch.busy = false }
}
async function refreshBatch() {
  if (!batch.open || batch.busy || !manualAction(batch.action)) return
  const ids = batch.items.filter(p => p.ok && !p.done).map(p => p.id)
  if (!ids.length) return
  const version = batchVersion
  const data = await api('/api/seat-recovery/tasks/status', { method: 'POST', body: { ids } })
  if (version !== batchVersion || disposed) return
  const byID = new Map((data.items || []).map(p => [p.id, p]))
  batch.items = batch.items.map(item => {
    if (!ids.includes(item.id)) return item
    const p = byID.get(item.id)
    if (!p) return { ...item, done: true, ok: false, message: '任务已不存在' }
    return { ...item, done: p.finished || (p.status === 'failed' && !p.pending_action), ok: p.status !== 'failed', message: p.message }
  })
}
async function loadLogs(more = false) {
  if (log.busy) return
  log.busy = true; log.error = ''
  try {
    const before = more ? log.items[log.items.length - 1]?.id : 0
    const data = await api(`/api/seat-recovery/tasks/${encodeURIComponent(log.taskID)}/logs?before=${before || 0}`)
    log.items = more ? [...log.items, ...(data.items || [])] : data.items || []; log.more = (data.items || []).length === 200
  } catch (e) { log.error = e.message }
  finally { log.busy = false }
}
function openLogs(p) { Object.assign(log, { open: true, taskID: p.id, email: p.email, items: [], more: false, error: '' }); loadLogs() }
async function poll() { await loadTasks(); try { await refreshBatch() } catch (e) { batch.error = e.message }; if (!disposed) pollTimer = setTimeout(poll, 2500) }
onMounted(async () => {
  timer = setInterval(() => { clock.value = anchorServer + performance.now() - anchorLocal }, 1000)
  try { Object.assign(settings, await api('/api/seat-recovery/settings')) } catch (e) { message.text = e.message; message.type = 'error' }
  if (!disposed) poll()
})
onUnmounted(() => { disposed = true; clearInterval(timer); clearTimeout(pollTimer) })
</script>

<template>
  <div class="recovery-view">
    <form class="panel recovery-settings" @submit.prevent="save">
      <div class="panel-title"><div><h2>席位恢复配置</h2><p>将原临停子号拉回，停留后依次切换普通席位并移出。</p></div><button class="btn primary" :disabled="saving"><Save :size="15" />{{ saving ? '保存中' : '保存配置' }}</button></div>
      <div class="recovery-fields">
        <label class="field"><span>进入方式</span><select v-model="settings.join_method"><option value="mother_invite">母号邀请 · 子号同意</option><option value="child_request">子号申请 · 母号同意</option></select></label>
        <label class="field"><span>移出方式</span><select v-model="settings.remove_method"><option value="mother_kick">母号踢出</option><option value="child_leave">子号退出</option></select></label>
        <label class="field"><span>拉回后停留（分钟）</span><input v-model.number="settings.dwell_minutes" type="number" min="0" max="10080" required /></label>
        <label class="field"><span>同母号操作间隔（秒）</span><input v-model.number="settings.operation_interval_seconds" type="number" min="0" max="3600" required /></label>
        <label class="field"><span>释放后下一个间隔（秒）</span><input v-model.number="settings.next_interval_seconds" type="number" min="0" max="3600" required /></label>
        <label class="field"><span>AT 并发数</span><input v-model.number="settings.concurrency" type="number" min="1" step="1" required /><small>先检测 AT，有效直接使用；失效再登录，无固定上限</small></label>
      </div>
      <p class="recovery-note">匹配邮件管理的未进入空间和已使用过账号，排除死号、在空间内以及正在被其他任务占用的账号。AT 检测和登录按并发数准备。同母号逐个完成拉回，不同母号可并行；执行后可切换母号继续添加任务。配置只影响新任务，实际操作间隔取本配置与已有同母号间隔的较大值。暂停后，已开始占用普通席位的任务仍会继续清理。</p>
    </form>
    <MessageBar :message="message" />
    <section class="panel">
      <div class="panel-title"><h2>扫描临停</h2><span>已选 {{ adminIDs.length }} 个母号</span></div>
      <div class="recovery-actions">
        <input v-model="adminSearch" placeholder="搜索母号名称或邮箱" aria-label="搜索恢复母号" />
        <button class="btn ghost" :disabled="scanning || starting" @click="toggleAdmins(true)">全选母号</button>
        <button class="btn ghost" :disabled="scanning || starting" @click="toggleAdmins(false)">清空母号</button>
        <button class="btn ghost" :disabled="!adminIDs.length || scanning || starting" @click="scan(false)"><RefreshCw :size="15" :class="{ spin: scanning }" />{{ scanning ? '扫描中…' : '扫描临停' }}</button>
        <button class="btn primary" :disabled="!adminIDs.length || scanning || starting" @click="scan(true)">扫描并执行所选母号</button>
      </div>
      <div class="recovery-mothers">
        <label v-for="p in visibleAdmins" :key="p.id"><input v-model="adminIDs" type="checkbox" :value="p.id" :aria-label="`选择母号 ${p.label}`" :disabled="scanning || starting" @change="resetScan" /><span>{{ p.label }}<small>{{ p.email }}</small></span></label>
        <span v-if="!visibleAdmins.length">没有匹配的母号</span>
      </div>
      <p v-if="motherProgress.total" class="recovery-note">{{ scanning ? '扫描' : starting ? '创建任务' : '母号处理' }}进度：{{ motherProgress.done }} / {{ motherProgress.total }}</p>
      <ul v-if="motherProgress.errors.length" class="recovery-errors"><li v-for="(error, i) in motherProgress.errors" :key="i">{{ error }}</li></ul>
      <div class="recovery-actions"><button class="btn primary" :disabled="!selected.length || starting || scanning" @click="start(false)">{{ starting ? '正在创建…' : `执行选中 (${selected.length})` }}</button><button class="btn ghost" :disabled="!eligible.length || starting || scanning" @click="start(true)">执行全部可恢复 ({{ eligible.length }})</button></div>
      <div v-if="scanned" class="table-shell recovery-candidates">
        <table><thead><tr><th><input type="checkbox" aria-label="选择全部可恢复" :checked="allSelected" :disabled="starting || scanning" @change="selected = $event.target.checked ? eligible.map(p => p.key) : []" /></th><th>母号</th><th>子号邮箱</th><th>临停时间</th><th>可恢复席位</th><th>匹配结果</th></tr></thead>
          <tbody><tr v-for="p in candidates" :key="p.key"><td><input v-model="selected" type="checkbox" :value="p.key" :disabled="!p.eligible || starting || scanning" :aria-label="`选择 ${p.admin_label} / ${p.email}`" /></td><td>{{ p.admin_label }}</td><td>{{ p.email }}</td><td>{{ formatTime(p.deactivated_time) }}</td><td>5x</td><td>{{ p.eligible ? '已匹配邮件管理的未进入空间 / 已使用过账号' : p.reason }}</td></tr><tr v-if="!candidates.length"><td colspan="6" class="empty-cell">没有实际临停子号</td></tr></tbody>
        </table>
      </div>
    </section>
    <section class="panel">
      <div class="panel-title"><h2>恢复任务 <small>{{ total }}</small></h2><div class="recovery-actions"><label><input v-model="activeOnly" type="checkbox" @change="page = 1; taskSelected = []; loadTasks()" />只看未结束</label><button class="btn ghost" :disabled="loading" @click="loadTasks"><RefreshCw :size="15" :class="{ spin: loading }" />刷新</button></div></div>
      <div class="recovery-actions recovery-bulk">
        <span>已选 {{ taskSelected.length }} 个</span><button class="btn ghost" @click="togglePage(true)">选择本页</button><button class="btn ghost" @click="taskSelected = []">清空选择</button>
        <button v-for="action in ['pause','resume','delete','mother_kick','child_leave']" :key="action" class="btn ghost" :disabled="!taskSelected.length || batch.busy" @click="batchControl(action)">批量{{ actionNames[action] }}</button>
      </div>
      <div class="table-shell"><table class="recovery-table"><thead><tr><th><input type="checkbox" aria-label="选择本页恢复任务" :checked="pageSelected" @change="togglePage($event.target.checked)" /></th><th>子号 / 母号</th><th>执行阶段</th><th>状态 / 进度</th><th>实际席位</th><th>拉回时间</th><th>已停留 / 剩余</th><th>操作</th></tr></thead><tbody>
        <tr v-for="p in tasks" :key="p.id"><td><input v-model="taskSelected" type="checkbox" :value="p.id" :aria-label="`选择任务 ${p.email}`" /></td><td><strong>{{ p.email }}</strong><small>{{ p.admin_label }}</small></td>
          <td><div class="recovery-stages"><span v-for="stage in taskStages(p)" :key="stage" :class="{ current: p.stage === stage, done: p.stage === 'completed' || taskStages(p).indexOf(stage) < taskStages(p).indexOf(p.stage), failed: p.stage === stage && p.status === 'failed' }"><RefreshCw v-if="p.stage === stage && running[p.id]" :size="11" class="spin" />{{ stage === 'manual_remove' ? (p.manual_remove_method === 'child_leave' ? '手动退出' : '手动踢出') : stageName(p, stage) }}</span></div></td>
          <td class="recovery-progress"><strong :class="{ error: p.status === 'failed' }">{{ statusLabel(p) }}</strong><small>{{ p.message }}</small></td>
          <td>{{ ({ held_prolite: '临停 5x', prolite: '5x', default: '普通席位', outside: '已离开' })[p.seat_type] || p.seat_type }}<small v-if="p.lane">普通席位串行位置已保留</small></td><td>{{ p.joined_at ? formatTime(p.joined_at) : '—' }}</td><td>{{ elapsed(p) }}<small>剩余 {{ remaining(p) }}</small></td>
          <td><div class="recovery-actions"><button class="btn ghost" @click="openLogs(p)">日志</button><template v-if="!p.finished">
            <button v-if="p.status === 'failed'" class="btn ghost" :disabled="controlBusy.has(p.id) || running[p.id]" @click="control(p, 'retry')">重试</button>
            <button class="btn ghost" :disabled="controlBusy.has(p.id)" @click="control(p, p.paused ? 'resume' : 'pause')">{{ p.paused ? '继续' : '暂停' }}</button>
            <button class="btn ghost" :disabled="batch.busy || !!p.pending_action" @click="control(p, 'mother_kick')">踢出</button>
            <button class="btn ghost" :disabled="batch.busy || !!p.pending_action" @click="control(p, 'child_leave')">退出</button>
            <button v-if="p.stage === 'login' && !running[p.id]" class="btn ghost" :disabled="controlBusy.has(p.id) || !!p.pending_action" @click="control(p, 'cancel')">取消</button>
          </template></div></td></tr>
        <tr v-if="!tasks.length"><td colspan="8" class="empty-cell">暂无恢复任务</td></tr>
      </tbody></table></div>
      <div class="recovery-pages"><button class="btn ghost" :disabled="page <= 1 || loading" @click="page--; loadTasks()">上一页</button><span>{{ page }} / {{ pages }}</span><button class="btn ghost" :disabled="page >= pages || loading" @click="page++; loadTasks()">下一页</button></div>
    </section>
    <Teleport to="body"><div v-if="batch.open" class="recovery-backdrop" @click.self="batch.open = false"><section class="recovery-log panel" role="dialog" aria-modal="true" :aria-busy="batch.busy" aria-label="恢复批量操作进度">
      <div class="panel-title"><h2>{{ actionNames[batch.action] }}进度：{{ batchDone }} / {{ batch.items.length }}</h2><button class="btn ghost" aria-label="关闭操作进度" @click="batch.open = false"><X :size="18" /></button></div>
      <p v-if="manualAction(batch.action)" class="recovery-note">提交后由后台执行，关闭弹窗不影响任务；可在恢复任务及日志查看后续进度。</p>
      <p v-if="batch.error" class="error">{{ batch.error }}</p>
      <ul class="recovery-batch-results"><li v-for="p in batch.items" :key="p.id"><RefreshCw v-if="!p.done" :size="14" class="spin" /><strong>{{ p.email }}</strong><span :class="{ error: p.ok === false }">{{ p.message }}</span></li></ul>
    </section></div></Teleport>
    <Teleport to="body"><div v-if="log.open" class="recovery-backdrop" @click.self="log.open = false"><section class="recovery-log panel" role="dialog" aria-modal="true" aria-label="席位恢复日志"><div class="panel-title"><div><h2>席位恢复日志</h2><small>{{ log.email }}</small></div><div class="recovery-actions"><button class="btn ghost" :disabled="log.busy" @click="loadLogs(false)">刷新</button><button class="btn ghost" aria-label="关闭日志" @click="log.open = false"><X :size="18" /></button></div></div><p v-if="log.error" class="error">{{ log.error }}</p><p v-if="log.busy">正在读取…</p><ol><li v-for="item in log.items" :key="item.id"><time>{{ formatTime(item.at) }}</time><span>{{ stageNames[item.stage] || item.stage }}</span><pre>{{ item.message }}</pre></li></ol><button v-if="log.more" class="btn ghost" :disabled="log.busy" @click="loadLogs(true)">加载更早日志</button></section></div></Teleport>
  </div>
</template>

<style scoped>
.recovery-mothers{display:flex;flex-wrap:wrap;gap:10px;max-height:220px;overflow:auto;margin:12px 0}.recovery-mothers label{display:flex;align-items:center;gap:8px;border:1px solid var(--line);border-radius:8px;padding:8px 12px}.recovery-mothers small{display:block;color:var(--muted);font-size:11px}.recovery-errors{max-height:160px;overflow:auto;color:#df5757;font-size:12px}.recovery-bulk{margin-bottom:12px}.recovery-batch-results{padding:0;list-style:none}.recovery-batch-results li{display:flex;flex-wrap:wrap;align-items:center;gap:10px;border-bottom:1px solid var(--line);padding:10px 0}.recovery-batch-results span{overflow-wrap:anywhere;flex:1;min-width:160px}
.recovery-view{display:grid;gap:16px;min-width:0}.panel{padding:18px;min-width:0}.panel-title p,.recovery-note{font-size:12px;color:var(--muted)}.recovery-fields{display:grid;grid-template-columns:repeat(3,minmax(180px,1fr));gap:12px}.recovery-actions{display:flex;flex-wrap:wrap;align-items:center;gap:8px}.recovery-actions select{max-width:360px}.recovery-candidates{max-height:360px;margin-top:12px}.recovery-table{min-width:1100px}.recovery-table small{display:block;font-size:11px;color:var(--muted);margin-top:4px}.recovery-progress{min-width:210px;max-width:320px;white-space:normal;overflow-wrap:anywhere}.recovery-stages{display:flex;flex-wrap:wrap;gap:4px;width:230px}.recovery-stages span{display:inline-flex;align-items:center;gap:3px;border:1px solid var(--line);border-radius:4px;padding:3px 5px;color:var(--muted);font-size:11px}.recovery-stages .current{color:#448deb;border-color:#448deb}.recovery-stages .done{color:#24a56f}.recovery-stages .failed,.error{color:#df5757}.recovery-pages{display:flex;align-items:center;justify-content:flex-end;gap:12px;margin-top:12px}.recovery-backdrop{position:fixed;inset:0;background:#0008;z-index:1500;display:grid;place-items:center;padding:24px}.recovery-log{width:min(960px,95vw);max-height:85vh;overflow:auto;background:var(--surface);border-radius:10px}.recovery-log ol{padding-left:20px}.recovery-log li{border-bottom:1px solid var(--line);padding:10px 0}.recovery-log time{font-size:12px;color:var(--muted);margin-right:12px}.recovery-log pre{white-space:pre-wrap;overflow-wrap:anywhere;font:12px/1.6 ui-monospace,monospace}.spin{animation:recovery-spin 1s linear infinite}@keyframes recovery-spin{to{transform:rotate(360deg)}}@media(max-width:800px){.recovery-fields{grid-template-columns:1fr}.panel-title{flex-wrap:wrap;gap:10px}.recovery-actions select{max-width:100%}}
</style>
