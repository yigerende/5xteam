<script setup>
import { computed, nextTick, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { LoaderCircle, RefreshCw, X } from 'lucide-vue-next'
import { api } from '../api'
import { gptPlanLabel, gptCreatedTime, gptInfoStatus } from '../mailGPTInfo'
import Pagination from './Pagination.vue'
import IconButton from './IconButton.vue'
const props = defineProps({ mask: { type: Function, default: value => value }, defaultPageSize: { type: Number, default: 10 } })
const emit = defineEmits(['updated'])
const open = ref(false), running = ref(false), submitting = ref(false), jobID = ref(''), error = ref('')
const dialog = ref(null)
let previousFocus
watch(open, async value => {
  if (value) { previousFocus = document.activeElement; await nextTick(); dialog.value?.focus() }
  else if (previousFocus?.isConnected) previousFocus.focus()
})
function dialogKey(event) {
  if (event.key === 'Escape') { event.preventDefault(); open.value = false; return }
  if (event.key !== 'Tab') return
  const elements = [...dialog.value.querySelectorAll('button:not(:disabled), select:not(:disabled), [tabindex="0"]')]
  const first = elements[0], last = elements.at(-1)
  if (!first) { event.preventDefault(); return }
  if (event.shiftKey && (document.activeElement === first || document.activeElement === dialog.value)) { event.preventDefault(); last.focus() }
  else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus() }
}
const job = ref({ total: 0, done: 0, ok: 0, partial: 0, fail: 0, status: '', results: [] })
const page = ref(1), pageSize = ref(props.defaultPageSize)
const progress = computed(() => job.value.total ? Math.round(job.value.done * 100 / job.value.total) : 0)
const storageKey = 'mail-gpt-info-job'
let timer, disposed = false, revision = 0, controller
function remember(id) { try { id ? sessionStorage.setItem(storageKey, id) : sessionStorage.removeItem(storageKey) } catch {} }
function show() { open.value = true }
async function poll() {
  clearTimeout(timer)
  controller?.abort()
  controller = new AbortController()
  const current = ++revision
  try {
    const params = new URLSearchParams({ job_id: jobID.value, page: String(page.value), page_size: String(pageSize.value) })
    const data = await api('/api/mail/accounts/refresh-info/status?' + params, { signal: controller.signal, cache: 'no-store' })
    if (disposed || current !== revision) return
    const previous = job.value.done
    job.value = data.job
    error.value = ''
    running.value = data.job.status !== 'done'
    if (previous !== data.job.done || !running.value) emit('updated')
    if (!running.value) remember('')
  } catch (e) {
    if (disposed || current !== revision || e.name === 'AbortError') return
    error.value = e.message
    if ([401, 403, 404].includes(e.status)) { running.value = false; remember(''); emit('updated') }
  }
  if (!disposed && current === revision && running.value) timer = window.setTimeout(poll, 1500)
}
async function start(emails) {
  open.value = true
  if (running.value || submitting.value) return
  submitting.value = true
  jobID.value = ''
  error.value = ''
  job.value = { total: emails.length, done: 0, ok: 0, partial: 0, fail: 0, status: 'queued', results: [] }
  page.value = 1
  try {
    const data = await api('/api/mail/accounts/refresh-info', { method: 'POST', body: { emails } })
    if (disposed) return
    jobID.value = data.job_id
    remember(data.job_id)
    running.value = true
    await poll()
  } catch (e) { error.value = e.message }
  finally { submitting.value = false }
}
function setPage(value) { page.value = value; if (jobID.value) poll() }
function setSize(value) { pageSize.value = value; page.value = 1; if (jobID.value) poll() }
function endpointText(item) {
  if (!item) return '-'
  return [item.ok ? '成功' : '未获取', item.http_status ? 'HTTP ' + item.http_status : '', item.attempts ? item.attempts + ' 次' : ''].filter(Boolean).join(' · ')
}
onMounted(() => {
  try { jobID.value = sessionStorage.getItem(storageKey) || '' } catch {}
  if (jobID.value) { running.value = true; poll() }
})
onBeforeUnmount(() => { disposed = true; revision++; clearTimeout(timer); controller?.abort() })
defineExpose({ start, show, running })
</script>
<template>
  <Teleport to="body">
    <div v-if="open" class="modal-backdrop gpt-info-backdrop" @click.self="open = false">
      <section ref="dialog" class="modal gpt-info-dialog" role="dialog" aria-modal="true" aria-labelledby="gpt-info-title" tabindex="-1" @keydown="dialogKey">
        <header class="gpt-info-heading"><h2 id="gpt-info-title">刷新 GPT 信息</h2><IconButton label="关闭 GPT 信息进度" @click="open = false"><X :size="16" /></IconButton></header>
        <div class="gpt-info-counters" aria-live="polite"><span><LoaderCircle v-if="running || submitting" class="spin" :size="14" />{{ running || submitting ? '处理中' : job.status === 'done' ? '已完成' : '未开始' }} {{ job.done }} / {{ job.total }}</span><span>成功 {{ job.ok }}</span><span>部分更新 {{ job.partial }}</span><span>失败 {{ job.fail }}</span></div>
        <progress :value="job.done" :max="job.total || 1" :aria-label="'刷新进度 ' + progress + '%'" />
        <p v-if="error" class="danger-text gpt-info-error">{{ error }} <button v-if="jobID" class="icon-button" title="重新读取进度" @click="poll"><RefreshCw :size="14" /></button></p>
        <div class="table-shell"><table class="gpt-info-table"><thead><tr><th>账号</th><th>状态</th><th>套餐 / GPT 创建时间</th><th>接口结果</th></tr></thead><tbody>
          <tr v-if="!job.results.length"><td colspan="4" class="empty-cell">{{ submitting ? '正在创建任务' : '暂无结果' }}</td></tr>
          <tr v-for="item in job.results" :key="item.email"><td>{{ props.mask(item.email) }}</td><td :class="{ 'danger-text': item.status === 'failed' }">{{ gptInfoStatus(item.status) }}</td><td>{{ gptPlanLabel(item.plan_type) }}<small>{{ gptCreatedTime(item.created_at_openai) }}</small></td><td><small>套餐：{{ endpointText(item.check?.plan) }}</small><small>账号：{{ endpointText(item.check?.me) }}</small><small v-if="item.error" class="danger-text">{{ item.error }}</small></td></tr>
        </tbody></table></div>
        <Pagination :page="page" :page-size="pageSize" :total="job.total" @update:page="setPage" @update:page-size="setSize" />
      </section>
    </div>
  </Teleport>
</template>
<style scoped>
.gpt-info-backdrop { z-index: 1400; }
.gpt-info-dialog { width: min(920px, 100%); max-height: calc(100dvh - 40px); overflow: auto; min-width: 0; padding: 20px; border-radius: 8px; }
.gpt-info-dialog:focus { outline: none; }
.table-shell { max-height: min(55dvh, 520px); overflow: auto; }
.gpt-info-heading { display: flex; justify-content: space-between; align-items: center; gap: 12px; }
.gpt-info-heading h2 { margin: 0; font-size: 18px; }
.gpt-info-counters { display: flex; flex-wrap: wrap; gap: 12px; margin: 16px 0 8px; font-size: 12px; }
.gpt-info-counters span { display: inline-flex; align-items: center; gap: 5px; }
progress { width: 100%; height: 6px; accent-color: var(--green); margin-bottom: 12px; }
.gpt-info-table { width: 100%; min-width: 650px; table-layout: fixed; }
.gpt-info-table th:nth-child(1) { width: 26%; }
.gpt-info-table th:nth-child(2) { width: 12%; }
.gpt-info-table th:nth-child(3) { width: 25%; }
.gpt-info-table td, .gpt-info-error { white-space: normal; overflow-wrap: anywhere; }
.gpt-info-table th, .gpt-info-table td { padding: 10px 12px; }
.gpt-info-table small { display: block; font-size: 11px; line-height: 1.6; }
.spin { animation: gpt-info-spin 1s linear infinite; }
@keyframes gpt-info-spin { to { transform: rotate(360deg); } }
</style>
