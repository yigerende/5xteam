<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { Brain, LoaderCircle, Play, RefreshCw, Save } from 'lucide-vue-next'
import { api } from '../api'
import MessageBar from './MessageBar.vue'
import ProSIWCSettings from './ProSIWCSettings.vue'
const emit = defineEmits(['updated'])
const form = reactive({ normal_group_ids: [], degraded_group_ids: [] })
const data = ref(null), groups = ref([]), busy = ref(''), loaded = ref(false)
const message = reactive({ text: '', type: '' })
const running = computed(() => !!data.value?.runtime?.running)
const supported = computed(() => data.value?.provider === 'sub2')
const metrics = [['total', '监控账号'], ['normal', '正常'], ['degraded', '降智'], ['routed', '已切组'], ['stale', '已过期'], ['error', '查询失败']]
let timer, disposed = false
function show(text, type = 'error') { Object.assign(message, { text, type }) }
async function load(reset = false) {
  try {
    const result = await api('/api/pro-quality/settings')
    if (disposed) return
    data.value = result
    if (reset) { Object.assign(form, result.settings); loaded.value = true }
  } catch (e) { if (!disposed) show(e.message) }
}
async function poll() {
  await load()
  if (!disposed) timer = setTimeout(poll, 5000)
}
async function save() {
  busy.value = 'save'
  try {
    const result = await api('/api/pro-quality/settings', { method: 'PUT', body: form })
    data.value = result; Object.assign(form, result.settings)
    show('Pro 降智检测设置已保存', 'success'); emit('updated')
  } catch (e) { show(e.message) }
  finally { busy.value = '' }
}
async function loadGroups() {
  busy.value = 'groups'
  try { const result = await api('/api/pro-settings/sub2/groups'); groups.value = result.groups || result || [] }
  catch (e) { show(e.message) }
  finally { busy.value = '' }
}
async function run() {
  if (!window.confirm('立即读取 Sub2 最新降智结果，并按已保存的配置切换分组或按配置执行合并/删除/API 商户，继续？')) return
  busy.value = 'run'
  try { await api('/api/pro-quality/run', { method: 'POST' }); await load(); show('已开始查询并处理，可在账号列表查看结果', 'success'); emit('updated') }
  catch (e) { show(e.message) }
  finally { busy.value = '' }
}
onMounted(async () => { await load(true); if (!disposed) timer = setTimeout(poll, 5000) })
onBeforeUnmount(() => { disposed = true; clearTimeout(timer) })
</script>

<template>
  <section class="panel pro-quality-panel" aria-label="Pro 降智检测">
    <div class="panel-title"><div><h2><Brain :size="18" />降智检测</h2><p class="table-note">读取 Pro 推送 Sub2 的检测结果，按本页配置处理。</p></div></div>
    <div class="quality-summary"><div v-for="[key, label] in metrics" :key="key"><small>{{ label }}</small><strong>{{ data?.summary?.[key] || 0 }}</strong></div></div>
    <MessageBar v-if="message.text" :message="message" />
    <p v-if="data?.inactive_reason" class="danger-text">{{ data.inactive_reason }}</p>
    <p v-if="data?.runtime?.error" class="danger-text">{{ data.runtime.error }}</p>
    <p v-if="running" class="quality-progress" role="status"><LoaderCircle class="spin" :size="15" />正在查询并处理 {{ data.runtime.done }} / {{ data.runtime.total }}</p>
    <form v-if="loaded" @submit.prevent="save">
      <label class="quality-check"><input v-model="form.enabled" type="checkbox" :disabled="!supported && !form.enabled" />启用 Pro 降智检测与处理</label>
      <div class="quality-fields">
        <label class="field"><span>查询间隔（秒）</span><input v-model.number="form.poll_interval_seconds" type="number" min="10" max="86400" required /></label>
        <label class="field"><span>结果有效期（秒）</span><input v-model.number="form.max_result_age_seconds" type="number" min="30" max="86400" required /></label>
        <label class="field"><span>连续检测失败次数</span><input v-model.number="form.error_failure_limit" aria-label="连续检测失败次数" type="number" min="1" max="20" step="1" required /><small>默认 2 次，达限后先测试该账号连接；正常则恢复并清零，复核命中配置状态码才执行降智处理。</small></label>
        <label class="field"><span>复核降智状态码</span><input v-model="form.connection_failure_http_statuses" aria-label="复核降智状态码" type="text" placeholder="429,502,503" /><small>默认 429,502,503，多个用英文逗号分隔，留空使用默认值。复核上游状态码命中才按降智处理；其他错误记录后等待下次检测。</small></label>
        <div class="field"><span>检测条件</span><label class="quality-check"><input v-model="form.question_enabled" type="checkbox" />答题已确认异常</label><label class="quality-check"><input v-model="form.model_audit_enabled" type="checkbox" />模型已确认不一致</label></div>
        <label class="field"><span>触发条件</span><select v-model="form.condition_mode"><option value="any">任一勾选条件满足</option><option value="all">所有勾选条件同时满足</option></select></label>
        <label class="field"><span>降智后处理</span><select v-model="form.action" aria-label="降智后处理"><option value="groups">切换分组</option><option value="merge">按配置执行合并/删除/API 商户</option><option value="siwc">推送 SIWC</option></select></label>
        <div class="field"><span>处理前暂停调度</span><label class="quality-check"><input v-model="form.pause_before_action" type="checkbox" />达到降智条件后，先暂停该账号的 Sub2 调度</label><small>确认暂停后才执行降智后处理；暂停失败记录原因并在下轮检测重试。仅暂停当前账号，恢复分组不会自动恢复调度。</small></div>
        <label class="field"><span>结果处理并发</span><input v-model.number="form.concurrency" type="number" min="1" max="8" required /><small>空间合并仍按合并并发执行，同母号串行。</small></label>
      </div>
      <div v-if="form.action === 'groups' || form.action === 'siwc'" class="quality-groups">
        <button class="btn ghost compact" type="button" :disabled="!!busy || !supported" @click="loadGroups"><LoaderCircle v-if="busy === 'groups'" class="spin" :size="14" /><RefreshCw v-else :size="14" />{{ busy === 'groups' ? '读取中…' : '读取 Pro Sub2 分组' }}</button>
        <div class="quality-fields">
          <div v-for="field in [{ key: 'normal_group_ids', label: '移除的正常分组' }, { key: 'degraded_group_ids', label: '加入的降智分组' }]" :key="field.key" v-show="form.action === 'groups' || field.key === 'normal_group_ids'" class="field">
            <span>{{ field.label }}</span>
            <div class="quality-picker" :aria-label="field.label"><label v-for="group in groups" :key="group.id" class="quality-check"><input v-model="form[field.key]" type="checkbox" :value="Number(group.id)" />{{ group.name }} <small>#{{ group.id }}</small></label><small v-if="!groups.length">点击上方按钮读取分组</small></div>
            <small>已选：{{ form[field.key]?.join(', ') || '无' }}</small>
          </div>
        </div>
        <label v-if="form.action === 'groups'" class="quality-check"><input v-model="form.auto_restore" type="checkbox" />恢复正常后自动恢复原分组</label>
        <label v-if="form.auto_restore" class="field recovery-count"><span>连续正常次数</span><input v-model.number="form.recovery_limit" type="number" min="1" max="20" required /></label>
      </div>
      <p class="table-note">{{ form.action === 'siwc' ? '降智后转入 SIWC。已直接推送 SIWC 的账号只更新检测结果，不重复转入；未选择移除分组时移除原推送分组。' : form.action === 'merge' ? '确认降智后，跳过额度等待，执行全自动配置的最后一步。' : '确认降智后切组，保留其他分组；切组失败下次查询重试。' }} 连续检测失败达限后，先用 gpt-6.1-sol 测试该账号连接：成功则恢复正常并清零，仅复核上游状态码命中配置才执行处理。网络超时、Sub2 管理接口错误、响应不完整或状态码未命中时，仅记录错误并等待下次检测。接口查询失败按查询轮次累计，同一条 Sub2 检测失败结果不重复计数；未确认、过期结果不触发。</p>
      <div class="panel-actions"><button class="btn primary" type="submit" :disabled="!!busy"><LoaderCircle v-if="busy === 'save'" class="spin" :size="15" /><Save v-else :size="15" />{{ busy === 'save' ? '保存中…' : '保存设置' }}</button><button class="btn ghost" type="button" :disabled="!!busy || running || !data?.settings?.enabled || !!data?.inactive_reason" @click="run"><LoaderCircle v-if="busy === 'run' || running" class="spin" :size="15" /><Play v-else :size="15" />立即查询并处理</button></div>
    <ProSIWCSettings v-if="form.action === 'siwc'"/>
    </form>
    <p v-else class="table-note"><LoaderCircle class="spin" :size="15" />正在加载配置…</p>
  </section>
</template>

<style scoped>
.pro-quality-panel{padding:22px}.panel-title h2,.quality-progress{display:flex;align-items:center;gap:8px}.quality-summary{display:grid;grid-template-columns:repeat(6,minmax(0,1fr));gap:12px;margin:18px 0}.quality-summary>div{border:1px solid var(--line);border-radius:6px;padding:12px}.quality-summary small{color:var(--muted)}.quality-summary strong{display:block;font-size:23px;margin-top:4px}.quality-fields{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px;margin:20px 0}.quality-check{display:flex;align-items:center;gap:9px;font-size:13px;margin:8px 0}.quality-check input{width:16px;min-height:16px;height:16px;padding:0;flex-shrink:0}.quality-picker{border:1px solid var(--line);padding:8px 12px;border-radius:6px;min-height:80px;max-height:200px;overflow:auto}.quality-picker small{color:var(--muted)}.quality-groups{border-top:1px solid var(--line);padding-top:18px}.recovery-count{max-width:240px;margin:16px 0}.quality-progress{color:var(--green-strong)}@media(max-width:750px){.quality-fields{grid-template-columns:1fr}.quality-summary{grid-template-columns:repeat(3,minmax(0,1fr))}}
</style>
