<script setup>
import { nextTick, onBeforeUnmount, reactive, ref, watch } from 'vue'
import { Download, LoaderCircle, RefreshCw, X } from 'lucide-vue-next'
import { api, downloadFile } from '../api'
import { formatTime } from '../utils'
import IconButton from './IconButton.vue'

const view = reactive({ account: null, events: [], cursor: '', hasMore: false, loading: false, error: '', exporting: false })
const root = ref(null)
const sentinel = ref(null)
const expanded = ref(new Set())
let generation = 0
let request
let observer
const labels = { invite: '邀请空间', accept: '进入空间', transfer: '合并空间', remove: '移出空间', space_merge: '四步流程', credentials: '准备凭据', push: '推送', quota: '额度查询', stage: '手动修正' }
const statuses = { running: '执行中', completed: '成功', failed: '失败', pending: '待处理', not_started: '未开始' }
const label = event => labels[event.stage] || labels[event.operation] || event.stage || event.type
const path = account => `/api/pro-accounts/${encodeURIComponent(account.email)}/events`
watch([root, sentinel], ([container, target]) => {
  observer?.disconnect()
  if (!container || !target) return
  observer = new IntersectionObserver(entries => {
    if (entries.some(entry => entry.isIntersecting) && view.hasMore && !view.loading && !view.error) loadMore()
  }, { root: container, rootMargin: '160px' })
  observer.observe(target)
})
function close() {
  generation++
  request?.abort()
  observer?.disconnect()
  view.account = null
}
async function open(account) {
  close()
  expanded.value = new Set()
  Object.assign(view, { account, events: [], cursor: '', hasMore: false, loading: false, error: '', exporting: false })
  await loadMore(true)
  await nextTick()
  root.value?.focus()
}
async function loadMore(initial = false) {
  if (!view.account || view.loading || (!initial && !view.hasMore)) return
  const current = generation
  const controller = new AbortController()
  request = controller
  view.loading = true
  view.error = ''
  try {
    const params = new URLSearchParams({ limit: '50' })
    if (!initial && view.cursor) params.set('cursor', view.cursor)
    const result = await api(`${path(view.account)}?${params}`, { signal: controller.signal })
    if (current !== generation) return
    view.account = result.account || view.account
    view.events.push(...(result.events || []))
    view.cursor = result.next_cursor || ''
    view.hasMore = !!result.has_more && !!view.cursor
  } catch (error) {
    if (current === generation && !controller.signal.aborted) view.error = error.message
  } finally { if (current === generation) view.loading = false }
}
function expand(id, isOpen) {
  const next = new Set(expanded.value)
  isOpen ? next.add(id) : next.delete(id)
  expanded.value = next
}
async function exportLogs() {
  const current = generation
  view.exporting = true
  try { await downloadFile(`${path(view.account)}/export`, 'pro-account-logs.json') }
  catch (error) { if (current === generation) view.error = error.message }
  finally { if (current === generation) view.exporting = false }
}
onBeforeUnmount(close)
defineExpose({ open })
</script>

<template>
  <Teleport to="body">
    <div v-if="view.account" class="modal-backdrop" @click.self="close" @keydown.esc="close">
      <section ref="root" class="modal pro-log-dialog" role="dialog" aria-modal="true" aria-label="Pro 账号执行日志" tabindex="-1">
        <header class="modal-heading"><div><h2>{{ view.account.email }}</h2><small>{{ view.hasMore ? '已加载' : '共' }} {{ view.events.length }} 条 · 最近 2 天</small></div><div class="heading-actions"><IconButton label="刷新账号日志" :disabled="view.loading" @click="open(view.account)"><RefreshCw :size="15" /></IconButton><button class="btn ghost" :disabled="view.exporting" @click="exportLogs"><Download :size="15" />导出账号日志</button><IconButton label="关闭日志" @click="close"><X :size="16" /></IconButton></div></header>
        <div class="log-summary"><span v-for="stage in ['invite', 'accept', 'transfer', 'remove']" :key="stage">{{ labels[stage] }}：{{ statuses[view.account['pro_' + stage + '_status']] || view.account['pro_' + stage + '_status'] || '待处理' }}</span><span>空间：{{ view.account.target_team_id || '未关联' }}</span></div>
        <div class="log-timeline">
          <article v-for="event in view.events" :key="event.id" class="log-event" :class="{ failed: event.level === 'error' || event.to_status === 'failed' }">
            <div class="event-heading"><time>{{ formatTime(event.created_at) }}</time><strong>{{ label(event) }}</strong><span>{{ statuses[event.to_status] || '' }}</span></div>
            <p>{{ event.message }}</p>
            <small v-if="event.details?.error || event.response?.error" class="danger-text">{{ event.details?.error || event.response?.error }}</small>
            <small v-if="event.from_status">{{ statuses[event.from_status] || event.from_status }} → {{ statuses[event.to_status] || event.to_status }}</small>
            <small v-if="event.http_status || event.attempt || event.duration_ms">{{ event.http_status ? `HTTP ${event.http_status} · ` : '' }}{{ event.attempt ? `第 ${event.attempt} 次 · ` : '' }}{{ event.duration_ms || 0 }} ms</small>
            <small v-if="event.details?.proxy?.name">代理：{{ event.details.proxy.name }}</small>
            <small v-if="event.details?.execution_id">本次执行：{{ event.details.execution_id }}</small>
            <details v-if="event.request || event.response || event.details" @toggle="expand(event.id, $event.target.open)"><summary>查看请求 / 返回参数</summary><template v-if="expanded.has(event.id)"><pre v-if="event.request">请求：{{ JSON.stringify(event.request, null, 2) }}</pre><pre v-if="event.response">返回：{{ JSON.stringify(event.response, null, 2) }}</pre><pre v-if="event.details">详情：{{ JSON.stringify(event.details, null, 2) }}</pre></template></details>
          </article>
        </div>
        <p v-if="!view.events.length && !view.loading && !view.error" class="empty-cell">暂无执行记录</p>
        <div ref="sentinel" class="log-more" role="status"><span v-if="view.error" class="danger-text">{{ view.error }}</span><button v-if="view.error" class="btn ghost" @click="loadMore(!view.events.length)">重试</button><span v-else-if="view.loading"><LoaderCircle class="spin" :size="14" /> 正在加载</span><button v-else-if="view.hasMore" class="btn ghost" @click="loadMore()">加载更多</button><span v-else>已加载全部记录</span></div>
      </section>
    </div>
  </Teleport>
</template>

<style scoped>
.pro-log-dialog { width: min(900px, calc(100vw - 32px)); max-height: calc(100vh - 32px); overflow: auto; }
.modal-heading { display: flex; align-items: center; justify-content: space-between; flex-wrap: wrap; gap: 12px; }
.modal-heading h2 { font-size: 16px; overflow-wrap: anywhere; }
.modal-heading > div { min-width: 0; }
.heading-actions { flex-wrap: wrap; }
.log-summary { display: flex; flex-wrap: wrap; gap: 10px 16px; padding: 14px 0; border-bottom: 1px solid var(--line); font-size: 11px; color: var(--muted); overflow-wrap: anywhere; }
.log-event { padding: 13px 0; border-bottom: 1px solid var(--line); display: grid; gap: 5px; overflow-wrap: anywhere; }
.event-heading { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; font-size: 12px; }
.log-event p { margin: 0; font-size: 12px; }
small, time { font-size: 11px; color: var(--muted); }
.failed .event-heading strong { color: var(--red); }
summary { cursor: pointer; color: var(--blue); font-size: 11px; }
pre { max-height: 240px; overflow: auto; padding: 10px; background: var(--surface-2); white-space: pre-wrap; word-break: break-word; font: 11px/1.5 monospace; }
.log-more { padding: 16px 0; display: flex; justify-content: center; align-items: center; flex-wrap: wrap; gap: 10px; font-size: 12px; color: var(--muted); }
</style>
