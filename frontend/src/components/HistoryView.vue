<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { RefreshCw, Trash2 } from 'lucide-vue-next'
import { api } from '../api'
import { formatTime, operationText, shortID, statusText } from '../utils'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'
import Pagination from './Pagination.vue'

const props = defineProps({ history: { type: Array, default: () => [] } })
const emit = defineEmits(['reload'])
const page = ref(1)
const size = ref(10)
const rows = ref([])
const total = ref(0)
const summary = reactive({ all: 0, completed: 0, partial: 0, accounts: 0 })
const busy = ref(false)
const message = reactive({ text: '', type: '' })
const pages = computed(() => Math.max(1, Math.ceil(total.value / size.value)))
async function load() {
  busy.value = true
  try {
    const data = await api(`/api/history?page=${page.value}&page_size=${size.value}`)
    rows.value = data.items || []; total.value = Number(data.total || 0); Object.assign(summary, data.summary || {})
    const lastPage = Math.max(1, Math.ceil(total.value / size.value))
    if (page.value > lastPage) { page.value = lastPage; return load() }
  } catch (error) { Object.assign(message, { text: error.message, type: 'error' }) }
  finally { busy.value = false }
}
function setPage(value) { page.value = value; load() }
function setSize(value) { size.value = value; page.value = 1; load() }
async function clear() {
  if (!window.confirm('确认清空本机执行历史？')) return
  try { await api('/api/history', { method: 'DELETE' }); page.value = 1; await load(); emit('reload'); Object.assign(message, { text: '执行历史已清空', type: 'success' }) }
  catch (error) { Object.assign(message, { text: error.message, type: 'error' }) }
}
function emails(entry) { return (entry.results || []).map((item) => item.email).filter(Boolean) }
onMounted(load)
</script>

<template>
  <section class="view-stack">
    <header class="page-heading"><div><span class="overline">AUDIT TRAIL</span><h1>执行历史</h1><p>查看各空间操作的完成状态与子号结果</p></div><div class="heading-actions"><button class="btn ghost" type="button" :disabled="busy" @click="load"><RefreshCw :class="{ spin: busy }" :size="15" />刷新</button><button class="btn danger" type="button" @click="clear"><Trash2 :size="15" />清空</button></div></header>
    <div class="metric-grid history-metrics"><article class="metric-card slate"><div><span>全部任务</span><strong>{{ summary.all }}</strong><small>本机保留记录</small></div></article><article class="metric-card green"><div><span>已完成</span><strong>{{ summary.completed }}</strong><small>完整成功</small></div></article><article class="metric-card amber"><div><span>部分完成</span><strong>{{ summary.partial }}</strong><small>含失败账号</small></div></article><article class="metric-card blue"><div><span>处理账号</span><strong>{{ summary.accounts }}</strong><small>累计子号</small></div></article></div>
    <section class="panel list-panel"><div class="panel-title"><div><span>HISTORY</span><h2>任务记录</h2></div><span class="muted-count">第 {{ page }} / {{ pages }} 页</span></div><div class="table-shell"><table><thead><tr><th>任务 / 操作</th><th>母号</th><th>团队</th><th>状态</th><th>子号邮箱</th><th>总数</th><th>成功</th><th>失败</th><th>完成时间</th></tr></thead><tbody><tr v-if="!rows.length"><td colspan="9" class="empty-cell">暂无执行记录</td></tr><tr v-for="entry in rows" :key="entry.id"><td class="account-cell"><strong>{{ operationText[entry.operation] || entry.operation || '完整流程' }}</strong><small class="mono">{{ shortID(entry.id) }}</small></td><td>{{ entry.admin_email || '-' }}</td><td class="mono" :title="entry.team_account_id">{{ shortID(entry.team_account_id) }}</td><td><StatusPill :tone="entry.status">{{ statusText[entry.status] || entry.status }}</StatusPill></td><td><div v-if="emails(entry).length" class="email-stack"><span v-for="email in emails(entry)" :key="email" :title="email">{{ email }}</span></div><span v-else>-</span></td><td>{{ entry.total }}</td><td class="success-text">{{ entry.succeeded }}</td><td class="danger-text">{{ entry.failed }}</td><td>{{ formatTime(entry.completed_at) }}</td></tr></tbody></table></div><Pagination :page="page" :page-size="size" :total="total" @update:page="setPage" @update:page-size="setSize" /><MessageBar :message="message" /></section>
  </section>
</template>
