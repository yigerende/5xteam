<script setup>
import { computed, onMounted, ref } from 'vue'
import { ChevronDown, RefreshCw } from 'lucide-vue-next'
import { api } from '../api'
import Pagination from './Pagination.vue'
import StatusPill from './StatusPill.vue'

const events = ref([])
const query = ref('')
const filter = ref('all')
const expanded = ref(new Set())
const page = ref(1)
const pageSize = ref(10)
const busy = ref(false)
const error = ref('')
const eventTypes = { audit: '业务审计', exchange: '请求/返回', request: '流程请求', response: '流程响应', retry: '步骤重试', step: '流程步骤', join_trace: '邀请确认诊断', seat_snapshot: '席位快照', seat_query: '席位查询', seat_check: '席位检查', dead_detected: '识别死号', dead_remove_start: '开始移出死号', dead_remove_success: '死号移出成功', dead_remove_failed: '死号移出失败', final: '最终结果' }
const stages = { rotation: '进入轮转', invite: '邀请', accept: '进入空间', oauth: 'OAuth', push: '推送', quota: '额度', status: '401 检测', relogin: '重登', remove: '移出空间' }
function typeName(value) { return eventTypes[value] || value || '系统事件' }
function stageName(value) { return stages[value] || value || '-' }
function stringify(value) { return value == null ? '' : JSON.stringify(value, null, 2) }
function toggle(id) { const next = new Set(expanded.value); if (next.has(id)) next.delete(id); else next.add(id); expanded.value = next }
const filtered = computed(() => events.value.filter((item) => {
  if (filter.value !== 'all' && item.type !== filter.value) return false
  const text = `${item.email || ''} ${item.account_id || ''} ${item.message || ''} ${item.stage || ''} ${item.provider || ''}`.toLowerCase()
  return !query.value.trim() || text.includes(query.value.trim().toLowerCase())
}))
const shown = computed(() => filtered.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
async function load() { busy.value = true; error.value = ''; try { events.value = await api('/api/auto-rotation/events') } catch (e) { error.value = e.message } finally { busy.value = false } }
onMounted(load)
</script>
<template>
  <section class="execution-history-view">
    <div class="panel-title responsive"><div><span>EXECUTION HISTORY</span><h2>执行历史</h2><p class="panel-description">自动轮转、手动操作、401 检测、额度检测和重登共用同一套审计记录。</p></div><div class="heading-actions"><input v-model="query" class="history-search" placeholder="搜索账号、阶段或错误" @input="page = 1" /><select v-model="filter" class="history-filter" @change="page = 1"><option value="all">全部事件</option><option v-for="(label, key) in eventTypes" :key="key" :value="key">{{ label }}</option></select><button class="btn ghost" type="button" :disabled="busy" @click="load"><RefreshCw :class="{ spin: busy }" :size="15" />刷新</button></div></div>
    <div v-if="error" class="history-error">{{ error }}</div>
    <div class="table-shell"><table><thead><tr><th>时间</th><th>来源</th><th>线路</th><th>账号</th><th>事件</th><th>阶段</th><th>尝试</th><th>耗时</th><th>结果</th></tr></thead><tbody><tr v-if="!shown.length"><td colspan="9" class="empty-cell">暂无执行事件</td></tr><template v-for="event in shown" :key="event.id"><tr><td>{{ new Date(event.created_at).toLocaleString() }}</td><td>{{ event.source || '-' }}</td><td>{{ event.provider ? event.provider.toUpperCase() : '-' }}</td><td>{{ event.account_id || event.admin_account_id || '-' }}</td><td>{{ typeName(event.type) }}</td><td>{{ stageName(event.stage) }}</td><td>{{ event.attempt || '-' }}</td><td>{{ event.duration_ms ? `${event.duration_ms} ms` : '-' }}</td><td><button v-if="event.request || event.response || event.details" class="history-expand" type="button" @click="toggle(event.id)"><ChevronDown :class="{ expanded: expanded.has(event.id) }" :size="14" />{{ event.message || '查看详情' }}</button><span v-else>{{ event.message || '-' }}</span></td></tr><tr v-if="expanded.has(event.id)" class="history-detail-row"><td colspan="9"><div class="history-detail"><div v-if="event.message"><strong>消息</strong><p>{{ event.message }}</p></div><div v-if="event.request"><strong>请求参数</strong><pre>{{ stringify(event.request) }}</pre></div><div v-if="event.response"><strong>返回参数</strong><pre>{{ stringify(event.response) }}</pre></div><div v-if="event.details"><strong>详情</strong><pre>{{ stringify(event.details) }}</pre></div></div></td></tr></template></tbody></table></div>
    <Pagination :page="page" :page-size="pageSize" :total="filtered.length" @update:page="page = $event" @update:page-size="pageSize = $event" />
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
