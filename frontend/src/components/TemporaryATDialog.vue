<script setup>
import { computed, onBeforeUnmount, reactive, ref } from 'vue'
import { LoaderCircle, X } from 'lucide-vue-next'
import { runMailAccountTask } from '../mailAccountTask'
import { formatTime } from '../utils'

const emit = defineEmits(['busy', 'finished', 'progress'])
const open = ref(false)
const phase = ref('confirm')
const batch = ref(false)
const mode = ref('login')
const tasks = ref([])
const counts = reactive({ done: 0, ok: 0, fail: 0 })
const title = computed(() => mode.value === 'oauth'
  ? (batch.value ? '批量登录获取 RT / AT' : '登录获取 RT / AT')
  : (batch.value ? '批量获取临时 AT' : '获取临时 AT'))
let disposed = false
let controller

function start(accounts, isBatch = false, taskMode = 'login') {
  if (phase.value === 'running' || disposed) return
  const unique = new Map(accounts.map(account => [account.email.toLowerCase(), account]))
  if (!unique.size) return
  tasks.value = [...unique.values()].map(account => ({ email: account.email, status: 'queued', state: 'queued', logs: [], error: '', expanded: unique.size <= 5 }))
  Object.assign(counts, { done: 0, ok: 0, fail: 0 })
  batch.value = isBatch
  mode.value = taskMode === 'oauth' ? 'oauth' : 'login'
  phase.value = 'confirm'
  open.value = true
}
function taskText(task) {
  return ({ queued: '等待执行', running: '执行中', success: '成功', failed: '失败', cancelled: '已取消', challenge: '需要验证' })[task.status] || '执行中'
}
function close() { if (phase.value !== 'running') open.value = false }
async function confirm() {
  if (phase.value !== 'confirm' || disposed) return
  phase.value = 'running'
  controller = new AbortController()
  emit('busy', true)
  await Promise.all(tasks.value.map(async task => {
    task.status = 'running'
    emit('progress', { email: task.email, mode: mode.value, running: true })
    try {
      await runMailAccountTask(task, mode.value, job => {
        if (disposed) return
        // Show progress only; never copy tokens/session from job.result into UI logs.
        Object.assign(task, { status: job.status || job.state || 'running', state: job.state || job.status, logs: Array.isArray(job.logs) ? job.logs : task.logs, error: job.error || job.error_hint || '' })
      }, { signal: controller.signal })
      if (!disposed) { task.status = 'success'; counts.ok++ }
    } catch (error) {
      if (!disposed) { task.status = 'failed'; task.error = error.message; counts.fail++ }
    } finally { if (!disposed) { counts.done++; emit('progress', { email: task.email, mode: mode.value, running: false }) } }
  }))
  if (disposed) return
  phase.value = 'done'
  emit('busy', false)
  emit('finished', { ...counts, mode: mode.value })
}
onBeforeUnmount(() => { disposed = true; controller?.abort() })
defineExpose({ start })
</script>

<template>
  <Teleport to="body">
    <div v-if="open" class="modal-backdrop temporary-at-backdrop" @click.self="close" @keydown.esc="close">
      <section class="modal temporary-at-dialog" role="dialog" aria-modal="true" aria-labelledby="temporary-at-title">
        <header><h2 id="temporary-at-title">{{ title }}</h2><button class="icon-button" :title="mode === 'oauth' ? '关闭 OAuth 进度' : '关闭临时 AT 进度'" :disabled="phase === 'running'" @click="close"><X :size="16" /></button></header>
        <template v-if="phase === 'confirm'">
          <p v-if="mode === 'oauth'">为 {{ tasks.length }} 个账号执行与 Team 轮转第三步相同的 Codex OAuth 登录，按全局配置选择邮箱验证码或密码 + 2FA，成功后保存新的 RT / AT。无需加入 Team 轮转，不会自动推送或执行空间合并。</p>
          <p v-else>为 {{ tasks.length }} 个账号使用与邮件管理相同的登录方式获取临时 AT，成功后保存 AT、Session 并更新 AT 状态，不会获取或替换 RT。</p>
          <p>需要账号具备可用的登录凭据及相应验证方式；仅通过 Google 授权添加的账号不一定能直接登录。</p>
          <ul class="target-list"><li v-for="task in tasks" :key="task.email">{{ task.email }}</li></ul>
          <footer><button class="btn ghost" @click="close">取消</button><button class="btn primary" @click="confirm">确认获取</button></footer>
        </template>
        <template v-else>
          <div class="summary" aria-live="polite"><span><LoaderCircle v-if="phase === 'running'" class="spin" :size="14" />{{ phase === 'running' ? '执行中' : '已完成' }} {{ counts.done }} / {{ tasks.length }}</span><span>成功 {{ counts.ok }}</span><span>失败 {{ counts.fail }}</span></div>
          <progress :value="counts.done" :max="tasks.length" :aria-label="mode === 'oauth' ? 'OAuth RT / AT 获取进度' : '临时 AT 获取进度'" />
          <div class="task-list">
            <article v-for="task in tasks" :key="task.email" class="at-task" :data-email="task.email">
              <button class="task-heading" :aria-expanded="task.expanded" @click="task.expanded = !task.expanded"><strong>{{ task.email }}</strong><span :class="{ 'danger-text': task.status === 'failed' }">{{ taskText(task) }}</span></button>
              <p v-if="task.error" class="danger-text">{{ task.error }}</p>
              <small v-else-if="task.status === 'success'">{{ mode === 'oauth' ? 'RT / AT 已保存' : 'AT 与 Session 已保存' }}</small>
              <small v-else>{{ task.logs.at(-1)?.message || '正在创建登录任务' }}</small>
              <ol v-if="task.expanded" class="task-logs"><li v-for="(log, index) in task.logs" :key="index"><time>{{ formatTime(log.time) }}</time><span>{{ log.message }}</span></li></ol>
            </article>
          </div>
          <footer v-if="phase === 'done'"><button class="btn primary" @click="close">关闭</button></footer>
        </template>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.temporary-at-backdrop { z-index: 1450; }
.temporary-at-dialog { width: min(780px, 100%); max-height: calc(100dvh - 40px); overflow: auto; }
header, footer, .summary, .task-heading { display: flex; align-items: center; gap: 12px; }
header, .task-heading { justify-content: space-between; }
h2 { margin: 0; font-size: 18px; }
p { font-size: 12px; line-height: 1.7; overflow-wrap: anywhere; }
footer { justify-content: flex-end; margin-top: 18px; }
.summary { margin: 18px 0 10px; flex-wrap: wrap; font-size: 12px; }
.summary span { display: inline-flex; align-items: center; gap: 5px; }
progress { width: 100%; accent-color: var(--green); height: 7px; }
.task-list, .target-list { max-height: 50dvh; overflow: auto; }
.target-list { font-size: 12px; overflow-wrap: anywhere; }
.at-task { border: 1px solid var(--line); border-radius: 5px; padding: 12px; margin-top: 10px; }
.task-heading { width: 100%; border: 0; background: transparent; color: var(--text); text-align: left; padding: 0; font-size: 12px; }
.task-heading strong { overflow-wrap: anywhere; min-width: 0; }
.task-heading span { flex-shrink: 0; }
.at-task > small { display: block; color: var(--muted); margin-top: 8px; }
.task-logs { padding: 0; list-style: none; font-size: 11px; }
.task-logs li { display: flex; gap: 10px; margin-top: 6px; }
.task-logs time { color: var(--muted); flex-shrink: 0; }
.task-logs span { overflow-wrap: anywhere; }
.spin { animation: temporary-at-spin 1s linear infinite; }
@keyframes temporary-at-spin { to { transform: rotate(360deg); } }
</style>
