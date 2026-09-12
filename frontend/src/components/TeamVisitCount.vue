<script setup>
import { nextTick, onBeforeUnmount, ref } from 'vue'
import { api } from '../api'
import { formatTime } from '../utils'
const props = defineProps({ email: String, count: Number, uncertain: Boolean, mask: { type: Function, default: (text) => text } })
const open = ref(false), loading = ref(false), items = ref([]), error = ref(''), position = ref({}), popup = ref(null)
let timer, generation = 0
function keep() { clearTimeout(timer) }
function close() { open.value = false; generation++; window.removeEventListener('scroll', dismissOnScroll, true); window.removeEventListener('resize', close) }
function dismissOnScroll(event) { if (!popup.value?.contains(event.target)) close() }
function leave() { timer = setTimeout(close, 150) }
async function show(event) {
  keep()
  const rect = event.currentTarget.getBoundingClientRect(), request = ++generation
  const width = Math.min(420, window.innerWidth - 24)
  position.value = { width: `${width}px`, left: `${Math.max(12, Math.min(rect.left, window.innerWidth - width - 12))}px`, top: `${Math.min(rect.bottom + 6, Math.max(12, window.innerHeight - 360))}px` }
  open.value = true; loading.value = true; error.value = ''
  window.addEventListener('scroll', dismissOnScroll, true); window.addEventListener('resize', close)
  try { const data = await api(`/api/team-visits?email=${encodeURIComponent(props.email)}`); if (request === generation) items.value = data || [] }
  catch (e) { if (request === generation) error.value = e.message }
  finally { if (request === generation) { loading.value = false; await nextTick() } }
}
const outcome = (value) => ({ joined: '已进入', oauth_failed: '授权失败', push_failed: '推送失败', quota_failed: '额度失败', removed: '已移出', cleanup_pending: '下游待清理', monitoring: '监控中', oauth_ready: '授权完成', dead: '死号' }[value] || value || '-')
onBeforeUnmount(() => { keep(); close() })
</script>
<template>
  <span class="visit-count"><button type="button" aria-label="查看进入母号历史" @mouseenter="show" @mouseleave="leave" @focus="show" @blur="leave" @click="show">{{ count || 0 }}</button><small v-if="uncertain">历史待确认</small></span>
  <Teleport to="body"><div v-if="open" ref="popup" class="team-visit-popup" :style="position" role="tooltip" @mouseenter="keep" @mouseleave="leave">
    <strong>进入母号历史 · {{ count || 0 }}</strong>
    <p v-if="loading">正在加载...</p><p v-else-if="error" class="danger-text">{{ error }}</p><p v-else-if="!items.length">暂无已确认进入记录</p>
    <ul v-else><li v-for="item in items" :key="item.team_account_id"><strong>{{ mask(item.admin_label || item.admin_email || item.team_account_id) }}</strong><span v-if="item.admin_label && item.admin_email">{{ mask(item.admin_email) }}</span><span>进入：{{ item.entered_at ? formatTime(item.entered_at) : '历史时间未知' }}</span><span>移出：{{ item.removed_at ? formatTime(item.removed_at) : '-' }}</span><span>{{ outcome(item.outcome) }}</span><span v-if="item.removal_reason" class="danger-text">{{ mask(item.removal_reason) }}</span></li></ul>
  </div></Teleport>
</template>
<style scoped>
.visit-count { display: inline-flex; flex-direction: column; gap: 3px; }
.visit-count button { background: transparent; color: var(--blue); border: 0; padding: 4px 8px; min-width: 32px; cursor: pointer; text-decoration: underline dotted; font-variant-numeric: tabular-nums; }
.visit-count small { color: var(--muted); font-size: 10px; white-space: nowrap; }
.team-visit-popup { position: fixed; z-index: 20000; box-sizing: border-box; max-height: min(340px, calc(100vh - 24px)); overflow: auto; padding: 12px; border: 1px solid var(--line); border-radius: 6px; background: var(--surface, #fff); color: var(--text, #222); box-shadow: 0 5px 22px #0002; font-size: 12px; overflow-wrap: anywhere; }
.team-visit-popup ul { list-style: none; padding: 0; margin: 8px 0 0; }
.team-visit-popup li { display: flex; flex-direction: column; gap: 4px; border-top: 1px solid var(--line); padding: 10px 0; }
.team-visit-popup span { color: var(--muted); }
</style>
