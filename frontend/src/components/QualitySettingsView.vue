<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { RefreshCw, Save, Play, Plus, Pencil, Trash2, ArrowUp, ArrowDown } from 'lucide-vue-next'
import { api } from '../api'
import { formatTime } from '../utils'
import MessageBar from './MessageBar.vue'
const props = defineProps({ provider: { type: String, default: 'sub2' }, status: { type: Object, default: null } })
const emit = defineEmits(['saved'])
const form = reactive({})
const loaded = ref(false)
const busy = ref(false)
const groups = ref([])
const initialStatus = ref(null)
const stats = computed(() => (props.status || initialStatus.value)?.summary || {})
const monitor = computed(() => props.status || initialStatus.value || {})
const message = reactive({ text: '', type: '' })
const tab = ref('questions')
const editingID = ref('')
const editing = computed(() => (form.questions || []).find(q => q.id === editingID.value))
const dailyRequests = computed(() => monitor.value.settings?.question_enabled ? Math.round((Number(stats.value.total) || 0) * 86400 / Math.max(10, Number(monitor.value.settings?.interval_seconds) || 300)) : 0)
function setForm(settings) {
  Object.assign(form, settings)
  if (!Array.isArray(form.questions)) form.questions = [{ id: 'legacy', name: '原有题目', enabled: true, prompt: form.prompt, answer: form.answer, match_mode: form.match_mode, max_duration_ms: form.max_duration_ms }]
}
function addQuestion() {
  const q = { id: 'q-' + Date.now() + '-' + Math.random().toString(36).slice(2, 8), name: '', enabled: true, prompt: '', answer: '', match_mode: 'answer', max_duration_ms: 20000 }
  form.questions.push(q); editingID.value = q.id
}
function moveQuestion(index, delta) {
  const target = index + delta
  if (target < 0 || target >= form.questions.length) return
  const [q] = form.questions.splice(index, 1); form.questions.splice(target, 0, q)
}
function removeQuestion(index) {
  if (!window.confirm('删除这道检测题目？')) return
  form.questions.splice(index, 1)
}
async function load() {
  try { const data = await api('/api/quality/settings'); initialStatus.value = data; setForm(data.settings); loaded.value = true }
  catch (error) { message.text = error.message; message.type = 'error' }
}
async function loadGroups() {
  if (props.provider !== 'sub2') return
  busy.value = true
  try { const result = await api('/api/sub2-settings/test', { method: 'POST', body: {} }); groups.value = result.groups || [] }
  catch (error) { message.text = error.message; message.type = 'error' }
  finally { busy.value = false }
}
async function save() {
  busy.value = true
  try {
    const data = await api('/api/quality/settings', { method: 'PUT', body: form })
    initialStatus.value = data
    setForm(data.settings)
    message.text = '降智检测设置已保存'; message.type = 'success'; emit('saved', data)
  } catch (error) { message.text = error.message; message.type = 'error' }
  finally { busy.value = false }
}
async function run() {
  if (!window.confirm('按已保存设置立即检测；答题消耗实际额度，模型检查只读日志。达到异常阈值会踢出或切组。继续？')) return
  busy.value = true
  try { await api('/api/quality/run', { method: 'POST' }); message.text = '后台检测已启动，可到账号管理查看进度'; message.type = 'success'; emit('saved') }
  catch (error) { message.text = error.message; message.type = 'error' }
  finally { busy.value = false }
}
onMounted(load)
</script>

<template>
<section class="quality-overview" aria-label="降智检测统计">
  <div class="quality-stats">
    <article><span>总检测账号</span><strong>{{ stats.total || 0 }}</strong></article>
    <article class="stat-danger"><span>降智</span><strong>{{ stats.degraded || 0 }}</strong><small>模型异常 {{ stats.model_suspect || 0 }}</small></article>
    <article class="stat-success"><span>正常</span><strong>{{ stats.normal || 0 }}</strong><small>模型一致 {{ stats.model_normal || 0 }}</small></article>
    <article><span>检测失败</span><strong>{{ stats.errors || 0 }}</strong><small>模型查询失败 {{ stats.model_errors || 0 }}</small></article>
    <article class="stat-blue"><span>已自动处理</span><strong>{{ stats.processed || 0 }}</strong><small>降智分组 {{ stats.routed || 0 }}</small></article>
    <article class="stat-blue"><span>内容 / 耗时达标</span><strong>{{ stats.content_passed || 0 }} / {{ stats.time_passed || 0 }}</strong></article>
    <article><span>已踢出</span><strong>{{ stats.kicked || 0 }}</strong><small>疑似异常 {{ stats.suspect || 0 }}</small></article>
    <article class="stat-amber"><span>预计每日探测数</span><strong>{{ dailyRequests }}</strong></article>
  </div>
  <div class="quality-schedule"><span>上次答题：{{ formatTime(stats.last_checked_at) }}</span><span>下次答题：{{ monitor.settings?.enabled && monitor.settings?.question_enabled && !monitor.inactive_reason ? formatTime(monitor.next_at) : '已暂停' }}</span><span>下次模型检查：{{ monitor.settings?.enabled && monitor.settings?.model_audit_enabled && !monitor.inactive_reason ? formatTime(monitor.model_next_at) : '已暂停' }}</span><span v-if="monitor.runtime?.running">答题中 {{ monitor.runtime.done }} / {{ monitor.runtime.total }}</span><span v-if="monitor.model_runtime?.running">模型检查 {{ monitor.model_runtime.done }} / {{ monitor.model_runtime.total }}</span></div>
</section>
<section class="panel quality-settings">
  <div class="panel-title"><div><span>ACCOUNT QUALITY</span><h2>降智检测设置</h2></div></div>
  <MessageBar v-if="message.text" :message="message" />
  <p v-if="provider !== 'sub2'" class="danger-text">当前启用 CPA，降智检测暂停；仅支持已部署配套探测接口的 Sub2。</p>
  <form v-if="loaded" @submit.prevent="save">
    <label class="field checkbox-field master-switch"><span>开启降智检测</span><input v-model="form.enabled" type="checkbox" :disabled="provider !== 'sub2'" /></label>
    <nav class="quality-tabs" role="tablist" aria-label="降智检测配置">
      <button v-for="item in [{id:'questions',name:'题目检测'},{id:'models',name:'模型一致性'},{id:'actions',name:'异常处理'}]" :key="item.id" type="button" role="tab" :aria-selected="tab === item.id" :class="{active:tab === item.id}" @click="tab = item.id">{{ item.name }}</button>
    </nav>
    <div class="quality-grid">
      <template v-if="tab === 'questions'">
      <label class="field checkbox-field"><span>开启题目检测</span><input v-model="form.question_enabled" type="checkbox" /></label>
      <label class="field"><span>判断方式</span><select v-model="form.mode"><option value="content_time">内容＋耗时</option><option value="time">只看耗时</option><option value="content">只看内容</option></select></label>
      <label class="field"><span>检测模型</span><input v-model="form.model" required maxlength="200" /></label>
      <label class="field"><span>推理强度</span><select v-model="form.reasoning_effort"><option value="low">low</option><option value="medium">medium</option><option value="high">high</option><option value="xhigh">xhigh</option></select></label>
      <label class="field"><span>检测间隔（秒）</span><input v-model.number="form.interval_seconds" type="number" min="10" max="86400" required /></label>
      <label class="field"><span>异常 / 检测失败后复测间隔（秒）</span><input v-model.number="form.retry_seconds" type="number" min="10" max="86400" required /></label>
      <label class="field"><span>并发数</span><input v-model.number="form.concurrency" type="number" min="1" max="8" required /></label>
      <label class="field"><span>请求超时（秒）</span><input v-model.number="form.timeout_seconds" type="number" min="5" max="300" required /></label>
      <div class="quality-wide question-bank">
        <div class="bank-heading"><h3>检测题库（{{ form.questions.length }}）</h3><button class="btn ghost" type="button" :disabled="form.questions.length >= 50" @click="addQuestion"><Plus :size="15" />新增题目</button></div>
        <div class="question-table"><table><thead><tr><th>启用</th><th>题目</th><th>答案</th><th>耗时阈值</th><th>操作</th></tr></thead><tbody>
          <tr v-for="(question, index) in form.questions" :key="question.id">
            <td><input v-model="question.enabled" type="checkbox" :aria-label="'启用题目 ' + (index + 1)" /></td><td>{{ question.name || '未命名题目' }}</td><td class="answer-preview">{{ question.answer || '-' }}</td><td>{{ question.max_duration_ms }} ms</td>
            <td><div class="question-actions"><button type="button" :title="'编辑 ' + question.name" :aria-label="'编辑题目 ' + (index + 1)" @click="editingID = question.id"><Pencil :size="15" /></button><button type="button" title="上移" aria-label="上移" :disabled="index === 0" @click="moveQuestion(index, -1)"><ArrowUp :size="15" /></button><button type="button" title="下移" aria-label="下移" :disabled="index === form.questions.length - 1" @click="moveQuestion(index, 1)"><ArrowDown :size="15" /></button><button type="button" title="删除题目" aria-label="删除题目" @click="removeQuestion(index)"><Trash2 :size="15" /></button></div></td>
          </tr>
        </tbody></table></div>
      </div>
      <template v-if="editing">
        <label class="field"><span>题目名称</span><input v-model="editing.name" maxlength="60" required /></label>
        <label class="field"><span>耗时阈值（ms）</span><input v-model.number="editing.max_duration_ms" type="number" min="1" max="300000" required /></label>
        <label class="field quality-wide"><span>测试题目</span><textarea v-model="editing.prompt" rows="6" required /></label>
        <label class="field"><span>内容匹配方式</span><select v-model="editing.match_mode"><option value="answer">标准答案</option><option value="keyword">包含关键词</option><option value="regex">正则表达式</option></select></label>
        <label class="field"><span>标准答案 / 关键词 / 正则</span><textarea v-model="editing.answer" rows="2" maxlength="2000" :required="form.mode !== 'time'" /></label>
      </template>
      </template>
      <template v-if="tab === 'models'">
        <label class="field checkbox-field"><span>开启模型一致性检测</span><input v-model="form.model_audit_enabled" type="checkbox" /></label>
        <label class="field"><span>实际发送模型</span><input v-model="form.model_audit_model" maxlength="200" required /></label>
        <label class="field"><span>模型检查间隔（秒）</span><input v-model.number="form.model_audit_interval_seconds" type="number" min="10" max="86400" required /></label>
        <dl class="model-audit-limits"><div><dt>每账号日志数</dt><dd>最近 3 条</dd></div><div><dt>每批账号数</dt><dd>10</dd></div><div><dt>批次并发</dt><dd>1</dd></div><div><dt>最小启动间隔</dt><dd>500 ms</dd></div></dl>
      </template>
      <template v-if="tab === 'actions'">
      <label class="field"><span>连续异常次数（两项独立计数）</span><input v-model.number="form.failure_limit" type="number" min="1" max="20" required /></label>
      <label class="field"><span>每账号检测记录显示上限</span><input v-model.number="form.history_limit" type="number" min="1" max="1000" required /></label>
      <label class="field"><span>确认异常后的处理</span><select v-model="form.action"><option value="kick">母号强制踢出并停止复用</option><option value="groups">保留在空间，切换 Sub2 分组</option></select></label>
      <template v-if="form.action === 'groups'">
        <div class="quality-wide"><button class="btn ghost" type="button" :disabled="busy || provider !== 'sub2'" @click="loadGroups"><RefreshCw :size="15" />读取 Sub2 分组</button><small>正常与降智分组不能重复。其余无关分组保留。</small></div>
        <label class="field"><span>移除的正常分组</span><select v-model="form.normal_group_ids" multiple size="5"><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}（{{ group.id }}）</option></select><small>已选：{{ (form.normal_group_ids || []).join(', ') || '无' }}</small></label>
        <label class="field"><span>加入的降智分组</span><select v-model="form.degraded_group_ids" multiple size="5"><option v-for="group in groups" :key="group.id" :value="group.id">{{ group.name }}（{{ group.id }}）</option></select><small>已选：{{ (form.degraded_group_ids || []).join(', ') || '无' }}</small></label>
        <label class="field checkbox-field"><span>自动恢复正常分组</span><input v-model="form.auto_restore" type="checkbox" /></label>
        <label v-if="form.auto_restore" class="field"><span>连续正常几次后恢复</span><input v-model.number="form.recovery_limit" type="number" min="1" max="20" required /></label>
      </template>
      </template>
    </div>
    <div class="heading-actions"><button class="btn primary" :disabled="busy" type="submit"><Save :size="15" />{{ busy ? '处理中…' : '保存降智检测设置' }}</button><button class="btn ghost" :disabled="busy || !form.enabled || provider !== 'sub2'" type="button" @click="run"><Play :size="15" />立即检测全部账号</button></div>
  </form>
</section>
</template>

<style scoped>
.quality-overview { padding: 4px 0 18px; }
.quality-stats { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); grid-template-rows: repeat(2, minmax(100px, auto)); gap: 10px; }
.quality-stats article { border: 1px solid var(--line, #dce5e7); border-radius: 8px; padding: 14px; min-height: 88px; background: var(--surface, #fff); min-width: 0; }
.quality-stats span, .quality-stats small { display: block; color: var(--muted, #64748b); font-size: 12px; overflow-wrap: anywhere; }
.quality-stats strong { display: block; font-size: 24px; line-height: 1.4; overflow-wrap: anywhere; }
.stat-danger strong { color: #dc3939; }
.stat-success strong { color: #078568; }
.stat-blue strong { color: #2478ba; }
.stat-amber strong { color: #bc6909; }
.quality-schedule { display: flex; flex-wrap: wrap; gap: 8px 18px; margin-top: 12px; font-size: 12px; color: var(--muted, #64748b); }
.quality-settings { padding: 24px; }
.master-switch { max-width: 200px; margin: 18px 0; }
.master-switch input { width: 18px; height: 18px; }
.quality-tabs { display: flex; border-bottom: 1px solid var(--line, #dce5e7); gap: 20px; }
.quality-tabs button { padding: 12px 0; border: 0; border-bottom: 2px solid transparent; background: transparent; color: var(--muted, #64748b); }
.quality-tabs button.active { color: var(--text, #1f3430); border-bottom-color: #078568; }
.bank-heading { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
.bank-heading h3 { font-size: 15px; margin: 0; }
.question-table { overflow-x: auto; margin-top: 12px; }
.question-table table { width: 100%; min-width: 570px; }
.question-table th, .question-table td { padding: 10px 8px; text-align: left; border-bottom: 1px solid var(--line, #dce5e7); }
.question-table input[type=checkbox] { width: 18px; height: 18px; }
.answer-preview { max-width: 180px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.question-actions { display: flex; gap: 4px; }
.question-actions button { width: 30px; height: 30px; display: grid; place-items: center; background: transparent; border: 1px solid var(--line, #dce5e7); border-radius: 4px; }
.question-actions button:disabled { opacity: .35; }
.model-audit-limits { margin: 0; font-size: 13px; }
.model-audit-limits div { display: flex; justify-content: space-between; padding: 6px 0; border-bottom: 1px solid var(--line, #dce5e7); }
.quality-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 18px; margin: 20px 0; }
.quality-grid > * { min-width: 0; }
.quality-wide { grid-column: 1 / -1; }
.quality-help, small { color: var(--text-muted, #8e9aaa); font-size: 12px; line-height: 1.8; }
.quality-grid textarea { width: 100%; resize: vertical; }
.quality-grid select[multiple] { min-height: 125px; }
.quality-grid .checkbox-field input { width: 18px; height: 18px; min-height: 18px; padding: 0; box-shadow: none; align-self: start; }
@media (max-width: 700px) { .quality-grid { grid-template-columns: minmax(0, 1fr); } }
@media (max-width: 600px) {
  .quality-stats { gap: 6px; }
  .quality-stats article { padding: 8px; }
  .quality-stats strong { font-size: 18px; }
  .quality-stats span, .quality-stats small { font-size: 11px; }
}
</style>
