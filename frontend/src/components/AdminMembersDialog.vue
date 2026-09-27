<script setup>
import { computed, onUnmounted, ref, watch } from 'vue'
import { RefreshCw, Search, X } from 'lucide-vue-next'
import { api } from '../api'
import { formatTime } from '../utils'
import IconButton from './IconButton.vue'
import StatusPill from './StatusPill.vue'

const props = defineProps({ account: { type: Object, required: true } })
const emit = defineEmits(['close'])
const search = ref('')
const query = ref('')
const offset = ref(0)
const limit = 25
const rows = ref([])
const total = ref(-1)
const hasMore = ref(false)
const loading = ref(false)
const error = ref('')
const queriedAt = ref('')
const page = computed(() => Math.floor(offset.value / limit) + 1)
let controller

async function loadMembers(nextOffset = offset.value) {
  controller?.abort()
  const request = new AbortController()
  controller = request
  loading.value = true
  error.value = ''
  rows.value = []
  queriedAt.value = ''
  total.value = -1
  hasMore.value = false
  offset.value = nextOffset
  try {
    const params = new URLSearchParams({ offset: String(nextOffset), limit: String(limit), query: query.value })
    const data = await api(`/api/admin-accounts/${encodeURIComponent(props.account.id)}/members?${params}`, { cache: 'no-store', signal: request.signal })
    if (controller !== request || request.signal.aborted) return
    rows.value = data.items || []
    total.value = Number(data.total ?? -1)
    hasMore.value = !!data.has_more
    queriedAt.value = data.queried_at
  } catch (e) {
    if (controller === request && !request.signal.aborted) error.value = e.message
  } finally {
    if (controller === request) loading.value = false
  }
}
function searchMembers() { query.value = search.value.trim(); loadMembers(0) }
function resetSearch() { search.value = ''; searchMembers() }
function seatLabel(seat) { return ({ prolite: '高级席位 5x', default: '普通席位', pro: 'Pro' })[seat] || seat || '未返回' }
function roleLabel(role) { return ({ 'account-owner': '所有者', 'account-admin': '管理员', 'standard-user': '成员', owner: '所有者', admin: '管理员' })[role] || role || '未返回' }
watch(() => props.account.id, () => { search.value = ''; query.value = ''; loadMembers(0) }, { immediate: true })
onUnmounted(() => controller?.abort())
</script>

<template>
  <div class="modal-backdrop members-backdrop" @click.self="emit('close')" @keydown.esc="emit('close')">
    <section class="modal members-dialog" role="dialog" aria-modal="true" aria-labelledby="admin-members-title">
      <header class="members-header">
        <div><h2 id="admin-members-title">空间实际成员</h2><p>{{ account.label }} · {{ account.email }}</p><small class="mono">{{ account.team_account_id }}</small></div>
        <IconButton label="关闭成员窗口" @click="emit('close')"><X :size="16" /></IconButton>
      </header>
      <p class="members-note">通过母号专属代理实时查询 OpenAI，包含管理员及成员。每页 {{ limit }} 条。</p>
      <form class="members-search" @submit.prevent="searchMembers">
        <input v-model="search" aria-label="搜索成员邮箱" placeholder="输入邮箱搜索成员" maxlength="320" />
        <button class="btn ghost" type="submit" :disabled="loading"><Search :size="14" />搜索</button>
        <button v-if="query || search" class="btn ghost" type="button" :disabled="loading" @click="resetSearch">清空</button>
        <button class="btn ghost" type="button" :disabled="loading" @click="loadMembers()"><RefreshCw :size="14" :class="{ spin: loading }" />刷新</button>
      </form>
      <div v-if="loading" class="members-loading" role="status"><RefreshCw class="spin" :size="20" />正在查询 OpenAI 成员…</div>
      <div v-else-if="error" class="members-error" role="alert">查询失败：{{ error }}<button class="btn ghost" type="button" @click="loadMembers()">重试</button></div>
      <template v-else>
        <div class="table-shell members-table"><table><thead><tr><th>邮箱 / 名称</th><th>角色</th><th>席位</th><th>加入时间</th><th>状态</th></tr></thead><tbody>
          <tr v-if="!rows.length"><td colspan="5" class="empty-cell">{{ query ? '未找到匹配的成员' : 'OpenAI 未返回成员' }}</td></tr>
          <tr v-for="(member, index) in rows" :key="member.id || index"><td class="member-email"><strong>{{ member.email || '未返回邮箱' }}</strong><small>{{ member.name || '—' }}</small></td><td>{{ roleLabel(member.role) }}</td><td>{{ seatLabel(member.seat_type) }}</td><td>{{ member.created_time ? formatTime(member.created_time) : '—' }}</td><td><StatusPill :tone="member.active ? 'success' : 'pending'">{{ member.active ? '正常' : '已停用' }}</StatusPill></td></tr>
        </tbody></table></div>
      </template>
      <footer class="members-footer">
        <span>{{ total >= 0 ? `共 ${total} 位成员` : '成员总数待确认' }}<small v-if="queriedAt">查询于 {{ formatTime(queriedAt) }}</small></span>
        <div><button class="btn ghost" type="button" :disabled="loading || offset === 0" @click="loadMembers(Math.max(0, offset - limit))">上一页</button><span>第 {{ page }} 页</span><button class="btn ghost" type="button" :disabled="loading || !hasMore" @click="loadMembers(offset + limit)">下一页</button></div>
      </footer>
    </section>
  </div>
</template>

<style scoped>
.members-backdrop { z-index: 120; }
.members-dialog { display: flex; flex-direction: column; width: min(960px, calc(100vw - 32px)); max-height: calc(100vh - 32px); overflow: auto; }
.members-header, .members-note, .members-search, .members-footer { flex-shrink: 0; }
.members-header { display: flex; justify-content: space-between; gap: 16px; }
.members-header h2 { margin: 0 0 6px; }
.members-header p { margin: 0 0 6px; overflow-wrap: anywhere; }
.members-header small, .members-note { color: var(--muted); font-size: 12px; overflow-wrap: anywhere; }
.members-search { display: flex; flex-wrap: wrap; gap: 8px; margin: 16px 0; }
.members-search input { flex: 1; min-width: 180px; }
.members-loading { display: flex; justify-content: center; align-items: center; gap: 10px; min-height: 180px; color: var(--muted); }
.members-error { padding: 16px; color: var(--red); overflow-wrap: anywhere; }
.members-error button { margin-left: 12px; }
.members-table { flex: 1 1 auto; min-height: 0; max-height: 55vh; overflow: auto; }
.members-table th { position: sticky; top: 0; background: var(--surface); }
.member-email { min-width: 210px; white-space: normal; overflow-wrap: anywhere; }
.member-email small { display: block; margin-top: 4px; color: var(--muted); }
.members-footer, .members-footer > div { display: flex; align-items: center; gap: 12px; flex-wrap: wrap; }
.members-footer { justify-content: space-between; margin-top: 16px; font-size: 12px; }
.members-footer small { display: block; margin-top: 4px; color: var(--muted); }
.spin { animation: members-spin .9s linear infinite; }
@keyframes members-spin { to { transform: rotate(360deg); } }
</style>
