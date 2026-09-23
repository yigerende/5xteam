<script setup>
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { RefreshCw } from 'lucide-vue-next'
import { api } from '../api'

const props = defineProps({
  enabled: Boolean, ids: { type: Array, default: () => [] }, active: Boolean, disabled: Boolean,
  url: { type: String, default: '' }, email: { type: String, default: '' }, password: { type: String, default: '' },
  endpoint: { type: String, required: true },
})
const emit = defineEmits(['update:enabled', 'update:ids'])
const rows = ref([])
const query = ref('')
const loading = ref(false)
const loaded = ref(false)
const error = ref('')
let controller
function cancel() { controller?.abort(); controller = null; loading.value = false }
function available(p) { return p.status === 'active' && p.account_count != null && p.account_count >= 0 && (!p.expires_at || new Date(p.expires_at).getTime() > Date.now()) }
function state(p) { return p.status !== 'active' ? '未启用' : p.expires_at && new Date(p.expires_at).getTime() <= Date.now() ? '已过期' : p.account_count == null ? '缺少绑定数' : '可用' }
function expiry(p) { return p.expires_at ? new Date(p.expires_at).toLocaleString() : '不过期' }
function urlKey(value) { try { return new URL(value.trim()).href.replace(/\/+$/, '') } catch { return value.trim().replace(/\/+$/, '') } }
const selected = computed(() => new Set(props.ids.map(Number)))
const visible = computed(() => rows.value.filter(p => `${p.name} ${p.host} ${p.port} ${p.id}`.toLowerCase().includes(query.value.trim().toLowerCase())))
const missing = computed(() => loaded.value ? props.ids.filter(id => !rows.value.some(p => Number(p.id) === Number(id))) : [])
function toggle(id, checked) { const ids = new Set(selected.value); checked ? ids.add(Number(id)) : ids.delete(Number(id)); emit('update:ids', [...ids]) }
function selectVisible() { emit('update:ids', [...new Set([...props.ids.map(Number), ...visible.value.filter(available).map(p => Number(p.id))])]) }
async function refresh() {
  if (!props.active || !props.enabled || props.disabled) return
  cancel()
  const request = new AbortController()
  controller = request
  loading.value = true; error.value = ''
  try {
    const result = await api(props.endpoint, { method: 'POST', body: { sub2: { url: props.url.trim(), email: props.email.trim() }, sub2_password: props.password }, signal: request.signal })
    if (request.signal.aborted) return
    rows.value = result || []; loaded.value = true
  } catch (e) { if (!request.signal.aborted) error.value = e.message }
  finally { if (controller === request) { loading.value = false; controller = null } }
}
// Connection edits invalidate displayed data. Only a user URL edit clears IDs;
// saved settings are loaded before this component's settings tab is mounted.
watch(() => props.url, (value, old) => {
  cancel(); rows.value = []; loaded.value = false; error.value = ''
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
    <label class="proxy-toggle"><input type="checkbox" :checked="enabled" :disabled="disabled" @change="emit('update:enabled', $event.target.checked)" /><strong>绑定代理</strong><span>已选 {{ ids.length }} 个</span></label>
    <template v-if="enabled">
      <p v-if="!active">启用 Sub2 后可读取代理列表。</p>
      <div class="proxy-actions"><input v-model="query" aria-label="搜索 Sub2 代理" placeholder="搜索代理名称 / 地址 / ID" /><button class="btn ghost compact" type="button" :disabled="disabled || !active || loading" @click="refresh"><RefreshCw :size="14" :class="{ spin: loading }" />{{ loading ? '读取中…' : '刷新代理' }}</button></div>
      <div class="proxy-actions"><button class="btn ghost compact" type="button" :disabled="disabled || loading || !active || !visible.some(available)" @click="selectVisible">选择筛选结果</button><button class="btn ghost compact" type="button" :disabled="disabled || !ids.length" @click="emit('update:ids', [])">清空选择</button></div>
      <p v-if="error" class="proxy-error" role="alert">{{ error }}</p>
      <div v-if="rows.length" class="proxy-list">
        <label v-for="p in visible" :key="p.id" class="proxy-row">
          <input type="checkbox" :checked="selected.has(Number(p.id))" :disabled="disabled || (!available(p) && !selected.has(Number(p.id)))" @change="toggle(p.id, $event.target.checked)" />
          <span><strong>{{ p.name || `代理 #${p.id}` }}</strong><small>{{ p.protocol }}://{{ p.host }}:{{ p.port }} · #{{ p.id }}</small><small>{{ state(p) }} · {{ expiry(p) }}</small></span>
          <b>{{ p.account_count ?? '—' }} <small>个账号</small></b>
        </label>
        <p v-if="!visible.length">没有匹配的代理</p>
      </div>
      <p v-else-if="loaded">没有可选的 Sub2 代理</p>
      <p v-else-if="!loading && !error">点击“刷新代理”读取列表和绑定账号数。</p>
      <div v-for="id in missing" :key="id" class="proxy-missing">已选代理 #{{ id }} 已禁用、删除或不可见 <button class="btn ghost compact" type="button" :disabled="disabled" @click="toggle(id, false)">取消选择</button></div>
      <p v-if="!ids.length" class="proxy-error">请至少选择一个代理；开启后无可用代理会停止推送。</p>
      <p>计数范围：Sub2 所有未删除账号（含已禁用账号）</p>
    </template>
  </div>
</template>

<style scoped>
.proxy-picker{grid-column:1/-1;border-top:1px solid var(--border,#d8e0eb);padding:14px 0;margin:12px 0;min-width:0}
.proxy-toggle,.proxy-actions{display:flex;align-items:center;gap:9px;flex-wrap:wrap}.proxy-toggle span{margin-left:auto;font-size:12px}
.proxy-picker p{font-size:12px;line-height:1.65;opacity:.85;margin:8px 0}.proxy-actions{margin:8px 0}.proxy-actions>input{flex:1;min-width:130px;width:0;padding:8px;border:1px solid var(--border,#d8e0eb);border-radius:6px;background:transparent;color:inherit}
.proxy-list{max-height:300px;overflow:auto}.proxy-row{display:flex;gap:9px;align-items:center;border-top:1px solid var(--border,#d8e0eb);padding:10px 0}.proxy-row>span{flex:1;min-width:0}.proxy-row strong,.proxy-row small{display:block;overflow-wrap:anywhere}.proxy-row small{font-size:11px;opacity:.75}.proxy-row b{text-align:right;font-size:14px;white-space:nowrap}.proxy-error{color:#d94848}.proxy-missing{font-size:12px;display:flex;align-items:center;justify-content:space-between;gap:8px}
</style>
