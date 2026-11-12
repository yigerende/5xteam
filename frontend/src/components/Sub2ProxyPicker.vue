<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { api } from '../api'

const props = defineProps({
  releaseMinutes: {type:Number,default:0}, enabled: Boolean, ids: { type: Array, default: () => [] }, active: Boolean, disabled: Boolean,
  url: { type: String, default: '' }, email: { type: String, default: '' }, password: { type: String, default: '' },
  endpoint: { type: String, required: true }, authMode: { type: String, default: '' },
})
const emit = defineEmits(['update:enabled', 'update:ids', 'update:releaseMinutes'])
const rows = ref([])
const query = ref('')
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
const limit = ref(0), savedLimit = ref(0), savingLimit = ref(false)
const editing = ref(''), editCount = ref(0), editRevision = ref(0), savingCount = ref(false), message = ref('')
let controller
function cancel() { controller?.abort(); controller = null; loading.value = false }
function exhausted(p) { return savedLimit.value > 0 && (!p.binding_key || Number(p.cumulative_bindings || 0)+Number(p.pending_bindings || 0)>=savedLimit.value) }
function available(p) { return p.status === 'active' && p.account_count != null && p.account_count >= 0 && (!p.expires_at || new Date(p.expires_at).getTime() > Date.now()) && !exhausted(p) }
function state(p) { return p.status !== 'active' ? '未启用' : p.expires_at && new Date(p.expires_at).getTime() <= Date.now() ? '已过期' : p.account_count == null ? '缺少绑定数' : exhausted(p) ? (p.binding_key ? '累计额度已用完' : '连接信息不完整') : '可用' }
function expiry(p) { return p.expires_at ? new Date(p.expires_at).toLocaleString() : '不过期' }
function urlKey(value) { try { return new URL(value.trim()).href.replace(/\/+$/, '') } catch { return value.trim().replace(/\/+$/, '') } }
const selectedIDs = computed(() => props.ids || [])
const selected = computed(() => new Set(selectedIDs.value.map(Number)))
const visible = computed(() => rows.value.filter(p => `${p.name} ${p.host} ${p.port} ${p.id}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const missing = computed(() => loaded.value ? selectedIDs.value.filter(id => !rows.value.some(p => Number(p.id) === Number(id))) : [])
function toggle(id, checked) { const ids = new Set(selected.value); checked ? ids.add(Number(id)) : ids.delete(Number(id)); emit('update:ids', [...ids]) }
function selectVisible() { emit('update:ids', [...new Set([...selectedIDs.value.map(Number), ...visible.value.filter(available).map(p => Number(p.id))])]) }
async function refresh() {
  if (!props.active || !props.enabled || props.disabled) return
  cancel()
  const request = new AbortController()
  controller = request
  loading.value = true; error.value = ''
  try {
    const [result,policy] = await Promise.all([
      api(props.endpoint, { method: 'POST', body: { sub2: { auth_mode: props.authMode, url: props.url.trim(), email: props.email.trim() }, sub2_password: props.password }, signal: request.signal }),
      api(`/api/sub2-proxy-binding-policy?url=${encodeURIComponent(props.url.trim())}`, {signal: request.signal}),
    ])
    if (request.signal.aborted) return
    rows.value = result || []; loaded.value = true
    savedLimit.value = Number(policy.max_bindings || 0); limit.value = savedLimit.value
  } catch (e) { if (!request.signal.aborted) error.value = e.message }
  finally { if (controller === request) { loading.value = false; controller = null } }
}
function validCount(value) { return Number.isSafeInteger(Number(value)) && String(value).trim()!=='' && Number(value)>=0 && Number(value)<=1000000000 }
async function saveLimit() {
  if(props.disabled||savingLimit.value||!props.active)return
  if(!validCount(limit.value)){error.value='上限须为非负整数，0 表示不限制';return}
  const url=props.url
  savingLimit.value=true;error.value='';message.value=''
  try{
    const result=await api('/api/sub2-proxy-binding-policy',{method:'PUT',body:{url:url.trim(),max_bindings:Number(limit.value)}})
    if(props.url!==url)return
    savedLimit.value=Number(result.max_bindings);message.value='累计绑定上限已保存，对当前下游的所有新推送生效。'
  }catch(e){if(props.url===url)error.value=e.message}finally{savingLimit.value=false}
}
function beginEdit(p) { editing.value=p.binding_key;editCount.value=p.cumulative_bindings;editRevision.value=p.binding_revision;message.value='';error.value='' }
async function saveCount(p) {
  if(props.disabled||savingCount.value||!p.binding_key)return
  if(!validCount(editCount.value)){error.value='累计次数须为非负整数';return}
  const url=props.url,count=Number(editCount.value)
  savingCount.value=true;error.value='';message.value=''
  try{
    await api('/api/sub2-proxy-binding-usage',{method:'PUT',body:{url:url.trim(),binding_key:p.binding_key,count,expected_revision:editRevision.value}})
    if(props.url!==url)return
    editing.value='';await refresh();message.value='累计次数已修改并保存。'
  }catch(e){if(props.url===url)error.value=e.message}finally{savingCount.value=false}
}
// Connection edits invalidate displayed data. Only a user URL edit clears IDs;
// saved settings are loaded before this component's settings tab is mounted.
watch(() => props.url, (value, old) => {
  cancel(); rows.value = []; loaded.value = false; error.value = '';editing.value='';message.value='';limit.value=0;savedLimit.value=0
  if (urlKey(value) !== urlKey(old) && old.trim()) emit('update:ids', [])
})
watch(() => [props.email, props.password], () => { cancel(); rows.value = []; loaded.value = false })
watch(() => [props.active, props.enabled, props.disabled], () => {
  cancel()
  if (props.active && props.enabled && props.url && props.email) void refresh()
}, { immediate: true })
onBeforeUnmount(cancel)
</script>

<template>
  <div class="proxy-picker">
    <label class="proxy-toggle"><input type="checkbox" :checked="enabled" :disabled="disabled" @change="emit('update:enabled', $event.target.checked)" /><strong>绑定代理</strong><span>已选 {{ selectedIDs.length }} 个</span></label>
    <template v-if="enabled">
      <div class="proxy-retention"><label>代理保留方式 <select :value="releaseMinutes !== 0 ? 'timed' : 'permanent'" :disabled="disabled" @change="emit('update:releaseMinutes', $event.target.value === 'timed' ? 30 : 0)"><option value="permanent">一直绑定</option><option value="timed">定时解绑</option></select></label><label v-if="releaseMinutes !== 0">绑定后 <input :value="releaseMinutes" type="number" required min="1" max="43200" step="1" :disabled="disabled" aria-label="代理绑定分钟数" @input="emit('update:releaseMinutes', $event.target.value === '' ? null : Number($event.target.value))" /> 分钟解绑</label><small>随推送设置保存，仅用于之后的新绑定；重试和重启不重置计时，不影响原推送时长。解绑后沿用 Sub2 默认出站。</small></div>
      <p v-if="!active">启用 Sub2 后可读取代理列表。</p>
      <div class="proxy-limit"><label>每个代理累计绑定上限 <input v-model.number="limit" type="number" min="0" max="1000000000" step="1" :disabled="disabled||savingLimit||loading||!loaded" aria-label="每个代理累计绑定上限" /></label><button class="btn ghost compact" type="button" :disabled="disabled||savingLimit||loading||!active||!loaded" @click="saveLimit">{{savingLimit?'保存中…':'保存上限'}}</button><small>0 为不限制；达到上限后不再新绑定，已提交请求继续确认。</small></div>
      <div class="proxy-actions"><input v-model="query" aria-label="搜索 Sub2 代理" placeholder="搜索代理名称 / 地址 / ID" /><button class="btn ghost compact" type="button" :disabled="disabled || !active || loading" @click="refresh"><RefreshCw :size="14" :class="{ spin: loading }" />{{ loading ? '读取中…' : '刷新代理' }}</button></div>
      <div class="proxy-actions"><button class="btn ghost compact" type="button" :disabled="disabled || loading || !active || !visible.some(available)" @click="selectVisible">选择筛选结果</button><button class="btn ghost compact" type="button" :disabled="disabled || !selectedIDs.length" @click="emit('update:ids', [])">清空选择</button></div>
      <p v-if="error" class="proxy-error" role="alert">{{ error }}</p>
      <p v-if="message" role="status">{{ message }}</p>
      <div v-if="rows.length" class="proxy-list">
        <div v-for="p in visible" :key="p.id" class="proxy-row">
          <input type="checkbox" :aria-label="`选择代理 ${p.name || p.id}`" :checked="selected.has(Number(p.id))" :disabled="disabled || (!available(p) && !selected.has(Number(p.id)))" @change="toggle(p.id, $event.target.checked)" />
          <span><strong>{{ p.name || `代理 #${p.id}` }}</strong><small>{{ p.protocol }}://{{ p.host }}:{{ p.port }} · #{{ p.id }}</small><small>{{ state(p) }} · {{ expiry(p) }}</small></span>
          <b>{{ p.account_count ?? '—' }} <small>当前绑定账号</small></b>
          <div class="proxy-total"><b>{{ p.binding_key ? p.cumulative_bindings : '—' }} <small>累计绑定次数</small></b><small v-if="p.pending_bindings">{{p.pending_bindings}} 次待确认</small>
            <div v-if="editing===p.binding_key&&p.binding_key" class="proxy-edit"><input v-model.number="editCount" type="number" min="0" max="1000000000" step="1" aria-label="修改累计绑定次数" :disabled="savingCount" /><button class="btn ghost compact" type="button" :disabled="savingCount||disabled" @click="saveCount(p)">{{savingCount?'保存中…':'保存'}}</button><button class="btn ghost compact" type="button" :disabled="savingCount" @click="editing=''">取消</button></div>
            <button v-else class="btn ghost compact" type="button" :disabled="disabled||loading||!p.binding_key||savingCount" @click="beginEdit(p)">修改累计次数</button>
          </div>
        </div>
        <p v-if="!visible.length">没有匹配的代理</p>
      </div>
      <p v-else-if="loaded">没有可选的 Sub2 代理</p>
      <p v-else-if="!loading && !error">点击“刷新代理”读取列表和绑定账号数。</p>
      <div v-for="id in missing" :key="id" class="proxy-missing">已选代理 #{{ id }} 已禁用、删除或不可见 <button class="btn ghost compact" type="button" :disabled="disabled" @click="toggle(id, false)">取消选择</button></div>
      <p v-if="!selectedIDs.length" class="proxy-error">请至少选择一个代理；开启后无可用代理会停止推送。</p>
      <p>推送时先选当前绑定账号最少的可用代理，再在其中选累计绑定次数最少的；两项都相同时随机。待确认预占计入两项数量。</p>
      <p>当前绑定：Sub2 所有未删除账号（含已禁用账号）。累计次数由 Space 保存，删除账号、代理改名或重新导入不会清零。</p>
      <p>首次记录以现有绑定数量为基数，之前已删除的历史绑定可手动补录。同一下游、协议、地址、端口和用户名视为同一个代理。</p>
    </template>
  </div>
</template>

<style scoped>
.proxy-retention{display:flex;align-items:center;gap:12px;flex-wrap:wrap;margin:12px 0}.proxy-retention label{display:flex;align-items:center;gap:7px}.proxy-retention input{width:90px}.proxy-retention small{flex-basis:100%;font-size:12px;opacity:.8}.proxy-retention select,.proxy-retention input{padding:7px;background:transparent;color:inherit;border:1px solid var(--border,#d8e0eb);border-radius:6px}
.proxy-picker{grid-column:1/-1;border-top:1px solid var(--border,#d8e0eb);padding:14px 0;margin:12px 0;min-width:0}
.proxy-toggle,.proxy-actions{display:flex;align-items:center;gap:9px;flex-wrap:wrap}.proxy-toggle span{margin-left:auto;font-size:12px}
.proxy-picker p{font-size:12px;line-height:1.65;opacity:.85;margin:8px 0}.proxy-actions{margin:8px 0}.proxy-actions>input{flex:1;min-width:130px;width:0;padding:8px;border:1px solid var(--border,#d8e0eb);border-radius:6px;background:transparent;color:inherit}
.proxy-list{max-height:300px;overflow:auto}.proxy-row{display:flex;gap:9px;align-items:center;border-top:1px solid var(--border,#d8e0eb);padding:10px 0}.proxy-row>span{flex:1;min-width:0}.proxy-row strong,.proxy-row small{display:block;overflow-wrap:anywhere}.proxy-row small{font-size:11px;opacity:.75}.proxy-row b{text-align:right;font-size:14px;white-space:nowrap}.proxy-error{color:#d94848}.proxy-missing{font-size:12px;display:flex;align-items:center;justify-content:space-between;gap:8px}
.proxy-limit{display:flex;align-items:center;gap:10px;flex-wrap:wrap;margin:12px 0}.proxy-limit label{display:flex;align-items:center;gap:8px}.proxy-limit input,.proxy-edit input{width:105px;padding:7px;border:1px solid var(--border,#d8e0eb);border-radius:6px;background:transparent;color:inherit}.proxy-limit small{font-size:12px;opacity:.75}.proxy-total{min-width:125px;text-align:right}.proxy-edit{display:flex;gap:4px;align-items:center;flex-wrap:wrap;justify-content:flex-end;margin-top:6px}@media(max-width:600px){.proxy-row{flex-wrap:wrap}.proxy-total{margin-left:auto}.proxy-row>span{min-width:55%}}
</style>
