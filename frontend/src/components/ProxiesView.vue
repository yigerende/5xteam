<script setup>
import { computed, reactive, ref } from 'vue'
import { CheckCircle2, Pencil, Save, Trash2, X } from 'lucide-vue-next'
import { api } from '../api'
import { maskProxyURL } from '../utils'
import IconButton from './IconButton.vue'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'
import Pagination from './Pagination.vue'

const props = defineProps({ proxies: { type: Array, default: () => [] }, selectedURL: { type: String, default: '' } })
const emit = defineEmits(['reload', 'select'])
const form = reactive({ id: '', name: '', url: '' })
const tests = ref(new Map())
const message = reactive({ text: '', type: '' })
const busy = ref(false)
const page = ref(1); const pageSize = ref(10)
const pagedProxies = computed(() => props.proxies.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
function setMessage(text = '', type = '') { Object.assign(message, { text, type }) }
function parseURL(showMessage = true) {
  const raw = form.url.trim()
  if (!raw) { if (showMessage) setMessage('请输入代理地址', 'error'); return '' }
  if (raw.includes('://')) return raw
  const parts = raw.split(':')
  if (parts.length < 4 || !parts[0] || !parts[1] || !parts[2] || !parts.slice(3).join(':')) { if (showMessage) setMessage('线路格式应为 host:port:username:password', 'error'); return '' }
  const port = Number(parts[1]); const host = parts[0].trim()
  if (!Number.isInteger(port) || port < 1 || port > 65535) { if (showMessage) setMessage('代理端口必须是 1 到 65535 的数字', 'error'); return '' }
  if (!host || /[/?#@\[\]]/.test(host)) { if (showMessage) setMessage('代理主机格式无效', 'error'); return '' }
  form.url = `http://${encodeURIComponent(parts[2])}:${encodeURIComponent(parts.slice(3).join(':'))}@${host}:${port}`
  if (showMessage) setMessage('已解析为 HTTP 代理格式', 'success')
  return form.url
}
function reset() { Object.assign(form, { id: '', name: '', url: '' }); setMessage() }
function edit(proxy) { Object.assign(form, { id: proxy.id, name: proxy.name, url: proxy.url }); setMessage('正在编辑代理配置'); window.scrollTo({ top: 0, behavior: 'smooth' }) }
async function save() {
  const url = parseURL(false)
  if (!form.name.trim() || !url) return setMessage('代理名称和有效地址不能为空', 'error')
  busy.value = true
  try {
    const profile = await api(form.id ? `/api/proxies/${encodeURIComponent(form.id)}` : '/api/proxies', { method: form.id ? 'PUT' : 'POST', body: { name: form.name.trim(), url } })
    reset(); emit('reload'); emit('select', profile.url); setMessage(`${profile.name} 已保存并设为全局代理`, 'success')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}
async function test(proxy = null) {
  const url = proxy?.url || parseURL(false)
  if (!url) return setMessage('请输入有效代理地址', 'error')
  busy.value = true; setMessage('正在测试代理链路...')
  try {
    const result = await api('/api/proxies/test', { method: 'POST', body: { url } })
    if (proxy) { const next = new Map(tests.value); next.set(proxy.id, result); tests.value = next }
    setMessage(`${result.message}，耗时 ${result.latency_ms}ms`, result.reachable ? 'success' : 'error')
  } catch (error) { setMessage(error.message, 'error') }
  finally { busy.value = false }
}
async function remove(proxy) {
  if (!window.confirm(`确认删除代理“${proxy.name}”？`)) return
  try { await api(`/api/proxies/${encodeURIComponent(proxy.id)}`, { method: 'DELETE' }); if (form.id === proxy.id) reset(); emit('reload'); if (props.selectedURL === proxy.url) emit('select', ''); setMessage(`${proxy.name} 已删除`, 'success') }
  catch (error) { setMessage(error.message, 'error') }
}
</script>

<template>
  <section class="view-stack">
    <header class="page-heading"><div><span class="overline">NETWORK ROUTING</span><h1>代理管理</h1><p>保存线路、检测连通性并选择全局出口</p></div><StatusPill :tone="selectedURL ? 'success' : 'pending'">{{ selectedURL ? '代理已启用' : '服务器直连' }}</StatusPill></header>
    <div class="management-grid proxy-layout">
      <form class="panel editor-panel" @submit.prevent="save"><div class="panel-title"><div><span>{{ form.id ? 'EDIT' : 'NEW' }}</span><h2>{{ form.id ? '编辑代理' : '添加代理' }}</h2></div><button v-if="form.id" class="btn ghost" type="button" @click="reset"><X :size="15" />取消</button></div><label class="field"><span>线路名称</span><input v-model="form.name" maxlength="40" placeholder="例如：新加坡线路" required /></label><label class="field"><span>代理地址</span><input v-model="form.url" spellcheck="false" placeholder="http://user:pass@host:port" required /><small>也支持 host:port:user:password</small></label><div class="compact-actions"><button class="btn ghost" type="button" @click="parseURL()">解析线路</button><button class="btn ghost" type="button" :disabled="busy" @click="test()"><CheckCircle2 :size="15" />测试连接</button></div><MessageBar :message="message" /><div class="panel-actions"><button class="btn primary" type="submit" :disabled="busy"><Save :size="15" />{{ form.id ? '保存修改' : '保存代理' }}</button></div></form>
      <section class="panel list-panel"><div class="panel-title"><div><span>PROFILES</span><h2>代理线路</h2></div><span class="muted-count">{{ proxies.length }} 个配置</span></div><div class="table-shell"><table><thead><tr><th>名称</th><th>地址</th><th>连通性</th><th>使用状态</th><th class="actions-column">操作</th></tr></thead><tbody><tr v-if="!proxies.length"><td colspan="5" class="empty-cell">暂无代理配置</td></tr><tr v-for="proxy in proxies" :key="proxy.id"><td><strong>{{ proxy.name }}</strong></td><td class="mono" :title="maskProxyURL(proxy.url)">{{ maskProxyURL(proxy.url) }}</td><td><template v-if="tests.get(proxy.id)"><StatusPill :tone="tests.get(proxy.id).reachable ? 'success' : 'danger'">{{ tests.get(proxy.id).reachable ? '可用' : '不可用' }}</StatusPill><small class="table-note">{{ tests.get(proxy.id).latency_ms }}ms</small></template><StatusPill v-else tone="pending">未测试</StatusPill></td><td><button class="status-select" :class="{ active: selectedURL === proxy.url }" type="button" @click="$emit('select', selectedURL === proxy.url ? '' : proxy.url)"><span></span>{{ selectedURL === proxy.url ? '使用中' : '设为全局' }}</button></td><td><div class="row-actions"><IconButton label="测试连接" :disabled="busy" @click="test(proxy)"><CheckCircle2 :size="15" /></IconButton><IconButton label="编辑代理" @click="edit(proxy)"><Pencil :size="15" /></IconButton><IconButton label="删除代理" danger @click="remove(proxy)"><Trash2 :size="15" /></IconButton></div></td></tr></tbody></table></div></section>
    </div>
  </section>
</template>
