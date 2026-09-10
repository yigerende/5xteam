<script setup>
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { ChevronDown, Download, RefreshCw } from 'lucide-vue-next'
import { api, downloadFile } from '../api'
import Pagination from './Pagination.vue'
import StatusPill from './StatusPill.vue'
import { formatTime } from '../utils'

const events = ref([])
const query = ref('')
const filter = ref('all')
const expanded = ref(new Set())
const page = ref(1)
const pageSize = ref(10)
const total = ref(0)
const busy = ref(false)
const exportBusy = ref(false)
const error = ref('')
const eventTypes = { audit: '业务审计', exchange: '请求/返回', request: '流程请求', response: '流程响应', retry: '步骤重试', step: '流程步骤', join_trace: '邀请确认诊断', remove_trace: '移出空间诊断', oauth_protocol: 'OAuth 协议诊断', seat_snapshot: '席位快照', seat_query: '席位查询', seat_check: '席位检查', dead_detected: '识别死号', dead_remove_start: '开始移出死号', dead_remove_success: '死号移出成功', dead_remove_failed: '死号移出失败', auto_failure_remove_start: '失败后开始移出', auto_failure_remove_success: '失败后移出成功', auto_failure_remove_failed: '失败后移出失败', final: '最终结果' }
const stages = { rotation: '进入轮转', invite: '邀请', accept: '进入空间', oauth: 'OAuth', push: '推送', quota: '额度', status: '401 检测', relogin: '重登', remove: '移出空间' }
function typeName(value) { return eventTypes[value] || value || '系统事件' }
function stageName(value) { return stages[value] || value || '-' }
function stringify(value) { return value == null ? '' : JSON.stringify(value, null, 2) }
function toggle(id) { const next = new Set(expanded.value); if (next.has(id)) next.delete(id); else next.add(id); expanded.value = next }
const shown = events
let searchTimer
async function load() {
  busy.value = true; error.value = ''
  try {
    const params = new URLSearchParams({ page: String(page.value), page_size: String(pageSize.value), query: query.value.trim(), type: filter.value === 'all' ? '' : filter.value })
    const data = await api(`/api/auto-rotation/events?${params}`)
    events.value = data.items || []
    total.value = Number(data.total || 0)
    const lastPage = Math.max(1, Math.ceil(total.value / pageSize.value))
    if (page.value > lastPage) { page.value = lastPage; return load() }
  } catch (e) { error.value = e.message } finally { busy.value = false }
}
function setPage(value) { page.value = value; load() }
function setPageSize(value) { pageSize.value = value; page.value = 1; load() }
watch(query, () => { window.clearTimeout(searchTimer); page.value = 1; searchTimer = window.setTimeout(load, 250) })
watch(filter, () => { page.value = 1; load() })
async function exportLogs() { exportBusy.value = true; error.value = ''; try { await downloadFile('/api/auto-rotation/events/export', 'team-execution-logs.json') } catch (e) { error.value = e.message } finally { exportBusy.value = false } }
onMounted(load)
onBeforeUnmount(() => window.clearTimeout(searchTimer))
</script>
<template>
  <section class="execution-history-view">
    <div class="panel-title responsive"><div><span>EXECUTION HISTORY</span><h2>执行历史</h2><p class="panel-description">自动轮转、手动操作、401 检测、额度检测和重登共用同一套审计记录。</p></div><div class="heading-actions"><input v-model="query" class="history-search" placeholder="搜索账号、阶段或错误" /><select v-model="filter" class="history-filter"><option value="all">全部事件</option><option v-for="(label, key) in eventTypes" :key="key" :value="key">{{ label }}</option></select><button class="btn ghost" type="button" :disabled="exportBusy" @click="exportLogs"><Download :size="15" />{{ exportBusy ? '导出中…' : '一键导出日志' }}</button><button class="btn ghost" type="button" :disabled="busy" @click="load"><RefreshCw :class="{ spin: busy }" :size="15" />刷新</button></div></div>
    <div v-if="error" class="history-error">{{ error }}</div>
    <div class="table-shell"><table><thead><tr><th>时间</th><th>来源</th><th>线路</th><th>账号</th><th>事件</th><th>阶段</th><th>尝试</th><th>耗时</th><th>结果</th></tr></thead><tbody><tr v-if="!shown.length"><td colspan="9" class="empty-cell">暂无执行事件</td></tr><template v-for="event in shown" :key="event.id"><tr><td>{{ formatTime(event.created_at) }}</td><td>{{ event.source || '-' }}</td><td>{{ event.provider ? event.provider.toUpperCase() : '-' }}</td><td>{{ event.account_id || event.admin_account_id || '-' }}</td><td>{{ typeName(event.type) }}</td><td>{{ stageName(event.stage) }}</td><td>{{ event.attempt || '-' }}</td><td>{{ event.duration_ms ? `${event.duration_ms} ms` : '-' }}</td><td><button v-if="event.request || event.response || event.details" class="history-expand" type="button" @click="toggle(event.id)"><ChevronDown :class="{ expanded: expanded.has(event.id) }" :size="14" />{{ event.message || '查看详情' }}</button><span v-else>{{ event.message || '-' }}</span></td></tr><tr v-if="expanded.has(event.id)" class="history-detail-row"><td colspan="9"><div class="history-detail"><div v-if="event.message"><strong>消息</strong><p>{{ event.message }}</p></div><div v-if="event.request"><strong>请求参数</strong><pre>{{ stringify(event.request) }}</pre></div><div v-if="event.response"><strong>返回参数</strong><pre>{{ stringify(event.response) }}</pre></div><div v-if="event.details"><strong>详情</strong><pre>{{ stringify(event.details) }}</pre></div></div></td></tr></template></tbody></table></div>
    <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="setPage" @update:page-size="setPageSize" />
  </section>
</template>
<style scoped>
.execution-history-view { display: grid; gap: 12px; }
.history-search { min-width: 190px; min-height: 30px; padding: 0 9px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--text); font-size: 11px; }
.history-filter { min-height: 30px; padding: 0 8px; border: 1px solid var(--line); border-radius: 5px; background: var(--surface-2); color: var(--text); font-size: 11px; }
.history-expand { display: inline-flex; align-items: center; gap: 4px; max-width: 340px; border: 0; background: transparent; color: var(--text-2); cursor: pointer; text-align: left; }
.history-expand svg { transition: transform .15s ease; }.history-expand svg.expanded { transform: rotate(180deg); }
.history-detail-row td { padding: 0 10px 10px; background: var(--surface-2); }.history-detail { display: grid; grid-template-columns: repeat(3, minmax(0, 1fr)); gap: 10px; padding: 10px; border: 1px solid var(--line); border-radius: 5px; }.history-detail strong { display: block; color: var(--muted); font-size: 10px; }.history-detail p, .history-detail pre { margin: 5px 0 0; max-height: 240px; overflow: auto; white-space: pre-wrap; word-break: break-word; font: 11px/1.5 ui-monospace, SFMono-Regular, Consolas, monospace; }.history-error { padding: 10px; border: 1px solid var(--red); color: var(--red); }
@media (max-width: 800px) { .heading-actions { flex-wrap: wrap; }.history-detail { grid-template-columns: 1fr; } }
</style>
