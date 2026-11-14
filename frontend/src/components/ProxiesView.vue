<script setup>
import { computed, onMounted, onBeforeUnmount, reactive, ref } from 'vue'
import { CheckCircle2, Gauge, LoaderCircle, Pencil, Save, ShieldCheck, Trash2, X } from 'lucide-vue-next'
import { api } from '../api'
import {globalProxyHealthText} from '../globalProxyHealth'
import { maskProxyURL } from '../utils'
import IconButton from './IconButton.vue'
import MessageBar from './MessageBar.vue'
import Pagination from './Pagination.vue'
import StatusPill from './StatusPill.vue'

const props = defineProps({ proxies: { type: Array, default: () => [] }, selectedURL: { type: String, default: '' }, defaultPageSize: { type: Number, default: 10 } })
const emit = defineEmits(['reload', 'select'])
const sessionDefaults = () => ({vendor: '', country: 'PH', session_minutes: 60})
const vendors = {'':'原地址 / 静态',iproyal:'皇冠（IPRoyal）',cliproxy:'CLI（Cliproxy）','1024proxy':'1024','711proxy':'711',other:'自定义厂商'}
const sessionMax = computed(() => ({cliproxy:120,'1024proxy':120,'711proxy':180}[form.vendor] || 10080))
const form = reactive({ id: '', name: '', url: '', ...sessionDefaults() })
const tests = ref(new Map())
const qualityTests = ref(new Map())
const testingIDs = ref(new Set())
const qualityTestingIDs = ref(new Set())
const batch = reactive({ mode: '', completed: 0, total: 0 })
const message = reactive({ text: '', type: '' })
const busy = ref(false)
const page = ref(1)
const pageSize = ref(props.defaultPageSize)
const rows = ref([])
const total = ref(0)
const pagedProxies = computed(() => rows.value)
const health=ref({}),healthNow=ref(Date.now()),healthError=ref('')
let healthRequest,healthTimer,lastHealthRead=0,healthOffset=0
async function loadHealth(){
 if(document.hidden||healthRequest)return
 const request=new AbortController();healthRequest=request;lastHealthRead=Date.now()
 try{const d=await api('/api/proxies/health',{signal:request.signal});if(!request.signal.aborted){health.value=d.items||{};healthError.value='';const at=Date.parse(d.server_time);if(Number.isFinite(at))healthOffset=at-Date.now()}}
 catch(e){if(!request.signal.aborted)healthError.value='冷却状态读取失败'}
 finally{if(healthRequest===request)healthRequest=null}
}
onMounted(()=>{loadHealth();healthTimer=setInterval(()=>{if(document.hidden)return;healthNow.value=Date.now()+healthOffset;if(Date.now()-lastHealthRead>=10000)loadHealth()},1000)})
onBeforeUnmount(()=>{clearInterval(healthTimer);healthRequest?.abort()})

async function loadPage() {
  const data = await api(`/api/proxies?page=${page.value}&page_size=${pageSize.value}`)
  rows.value = data.items || []; total.value = Number(data.total || 0)
  const lastPage = Math.max(1, Math.ceil(total.value / pageSize.value))
  if (page.value > lastPage) { page.value = lastPage; return loadPage() }
}
function setPage(value) { page.value = value; loadPage() }
function setPageSize(value) { pageSize.value = value; page.value = 1; loadPage() }

function setMessage(text = '', type = '') { Object.assign(message, { text, type }) }

function parseURL(showMessage = true) {
  const raw = form.url.trim()
  if (!raw) { if (showMessage) setMessage('请输入代理地址', 'error'); return '' }
  if (raw.includes('://')) return raw
  const parts = raw.split(':')
  if (parts.length < 4 || !parts[0] || !parts[1] || !parts[2] || !parts.slice(3).join(':')) {
    if (showMessage) setMessage('线路格式应为 host:port:username:password', 'error')
    return ''
  }
  const port = Number(parts[1])
  const host = parts[0].trim()
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    if (showMessage) setMessage('代理端口必须是 1 到 65535 的数字', 'error')
    return ''
  }
  if (!host || /[/?#@\[\]]/.test(host)) {
    if (showMessage) setMessage('代理主机格式无效', 'error')
    return ''
  }
  form.url = `http://${encodeURIComponent(parts[2])}:${encodeURIComponent(parts.slice(3).join(':'))}@${host}:${port}`
  if (showMessage) setMessage('已解析为 HTTP 代理格式', 'success')
  return form.url
}

function reset() {
  Object.assign(form, { id: '', name: '', url: '', ...sessionDefaults() })
  setMessage()
}

function edit(proxy) {
  Object.assign(form, { id: proxy.id, name: proxy.name, url: proxy.url, vendor:proxy.vendor||'',country:proxy.country||'PH',session_minutes:proxy.session_minutes||60 })
  setMessage('正在编辑代理配置')
  window.scrollTo({ top: 0, behavior: 'smooth' })
}

async function save() {
  const url = parseURL(false)
  if (!form.name.trim() || !url) return setMessage('代理名称和有效地址不能为空', 'error')
  const updatingGlobal = Boolean(form.id) && props.proxies.some(proxy => proxy.id === form.id && proxy.url === props.selectedURL)
  busy.value = true
  try {
    const profile = await api(form.id ? `/api/proxies/${encodeURIComponent(form.id)}` : '/api/proxies', {
      method: form.id ? 'PUT' : 'POST',
      body: { name: form.name.trim(), url, vendor:form.vendor,country:form.country.toUpperCase(),session_minutes:form.session_minutes },
    })
    reset()
    emit('reload')
    if (updatingGlobal) emit('select', profile.url)
    await loadPage()
    setMessage(`${profile.name} 已保存`, 'success')
  } catch (error) {
    setMessage(error.message, 'error')
  } finally {
    busy.value = false
  }
}

function updateSet(source, id, active) {
  const next = new Set(source.value)
  if (active) next.add(id); else next.delete(id)
  source.value = next
}

function saveMapResult(source, id, result) {
  const next = new Map(source.value)
  next.set(id, result)
  source.value = next
}

async function runConnectivityTest(proxy, notify = false) {
  updateSet(testingIDs, proxy.id, true)
  try {
    const result = await api('/api/proxies/test', { method: 'POST', body: { url: proxy.url, vendor:proxy.vendor,country:proxy.country,session_minutes:proxy.session_minutes } })
    saveMapResult(tests, proxy.id, result)
    if (notify) setMessage(`${proxy.name}：${result.message}，耗时 ${result.latency_ms}ms`, result.reachable ? 'success' : 'error')
    return result
  } catch (error) {
    const result = { reachable: false, latency_ms: 0, message: error.message }
    saveMapResult(tests, proxy.id, result)
    if (notify) setMessage(`${proxy.name}：${error.message}`, 'error')
    return result
  } finally {
    updateSet(testingIDs, proxy.id, false)
  }
}

async function test(proxy = null) {
  const url = proxy?.url || parseURL(false)
  if (!url) return setMessage('请输入有效代理地址', 'error')
  if (proxy) return runConnectivityTest(proxy, true)
  busy.value = true
  setMessage('正在测试代理出口...')
  try {
    const result = await api('/api/proxies/test', { method: 'POST', body: { url, vendor:form.vendor,country:form.country,session_minutes:form.session_minutes } })
    const exit = result.ip_address ? `，出口 ${result.ip_address}` : ''
    setMessage(`${result.message}${exit}，耗时 ${result.latency_ms}ms`, result.reachable ? 'success' : 'error')
  } catch (error) {
    setMessage(error.message, 'error')
  } finally {
    busy.value = false
  }
}

async function runQualityTest(proxy, notify = false) {
  updateSet(qualityTestingIDs, proxy.id, true)
  try {
    const result = await api('/api/proxies/openai-quality', { method: 'POST', body: { url: proxy.url, vendor:proxy.vendor,country:proxy.country,session_minutes:proxy.session_minutes } })
    saveMapResult(qualityTests, proxy.id, result)
    if (notify) setMessage(`${proxy.name}：${result.summary}，${result.message}`, result.status === 'pass' ? 'success' : 'error')
    return result
  } catch (error) {
    const result = { status: 'fail', score: 0, grade: 'F', summary: 'OpenAI 检测失败', message: error.message }
    saveMapResult(qualityTests, proxy.id, result)
    if (notify) setMessage(`${proxy.name}：${error.message}`, 'error')
    return result
  } finally {
    updateSet(qualityTestingIDs, proxy.id, false)
  }
}

async function testAll(mode) {
  if (batch.mode || !props.proxies.length) return
  const targets = [...props.proxies]
  batch.mode = mode
  batch.completed = 0
  batch.total = targets.length
  const concurrency = mode === 'quality' ? 3 : 5
  let index = 0
  const summary = { pass: 0, warn: 0, challenge: 0, fail: 0 }
  const worker = async () => {
    while (index < targets.length) {
      const proxy = targets[index++]
      const result = mode === 'quality' ? await runQualityTest(proxy) : await runConnectivityTest(proxy)
      const status = mode === 'quality' ? result.status : (result.reachable ? 'pass' : 'fail')
      summary[status] = (summary[status] || 0) + 1
      batch.completed += 1
    }
  }
  try {
    await Promise.all(Array.from({ length: Math.min(concurrency, targets.length) }, () => worker()))
    if (mode === 'quality') {
      setMessage(`OpenAI 质量检测完成：通过 ${summary.pass}，告警 ${summary.warn}，挑战 ${summary.challenge}，失败 ${summary.fail}`, summary.fail || summary.challenge ? 'error' : 'success')
    } else {
      setMessage(`线路检测完成：可用 ${summary.pass}，不可用 ${summary.fail}`, summary.fail ? 'error' : 'success')
    }
  } finally {
    batch.mode = ''
    batch.completed = 0
    batch.total = 0
  }
}

function proxyExit(proxy) { return qualityTests.value.get(proxy.id) || tests.value.get(proxy.id) || null }
function proxyLocation(proxy) {
  const result = proxyExit(proxy)
  return [result?.country, result?.region, result?.city].filter(Boolean).join(' / ')
}
function qualityTone(status) { return status === 'pass' ? 'success' : status === 'warn' ? 'partial' : 'danger' }
function qualityLabel(status) { return ({ pass: '可用', warn: '频控', challenge: 'CF 挑战', fail: '失败' })[status] || '未测试' }
function qualityTitle(result) {
  if (!result) return '点击执行 OpenAI 质量检测'
  return [result.summary, result.message, result.cf_ray ? `cf-ray: ${result.cf_ray}` : ''].filter(Boolean).join('；')
}

async function remove(proxy) {
  if (!window.confirm(`确认删除代理“${proxy.name}”？`)) return
  try {
    await api(`/api/proxies/${encodeURIComponent(proxy.id)}`, { method: 'DELETE' })
    if (form.id === proxy.id) reset()
    emit('reload')
    if (props.selectedURL === proxy.url) emit('select', '')
    await loadPage()
    setMessage(`${proxy.name} 已删除`, 'success')
  } catch (error) {
    setMessage(error.message, 'error')
  }
}
onMounted(loadPage)
</script>

<template>
  <section class="view-stack proxies-view">
    <header class="page-heading">
      <div><span class="overline">NETWORK ROUTING</span><h1>代理管理</h1><p>保存线路、检测出口 IP 与 OpenAI 质量，并选择全局出口</p></div>
      <StatusPill :tone="selectedURL ? 'success' : 'pending'">{{ selectedURL ? '代理已启用' : '服务器直连' }}</StatusPill>
    </header>
    <div class="management-grid proxy-layout">
      <form class="panel editor-panel" @submit.prevent="save">
        <div class="panel-title">
          <div><span>{{ form.id ? 'EDIT' : 'NEW' }}</span><h2>{{ form.id ? '编辑代理' : '添加代理' }}</h2></div>
          <button v-if="form.id" class="btn ghost" type="button" @click="reset"><X :size="15" />取消</button>
        </div>
        <label class="field"><span>线路名称</span><input v-model="form.name" maxlength="40" placeholder="例如：新加坡线路" required /></label>
        <label class="field"><span>代理厂商</span><select v-model="form.vendor"><option v-for="(label,key) in vendors" :key="key" :value="key">{{label}}</option></select></label>
        <template v-if="form.vendor"><label class="field"><span>国家代码（默认菲律宾）</span><input v-model="form.country" maxlength="2" placeholder="PH" required /></label><label class="field"><span>会话有效期（分钟）</span><input v-model.number="form.session_minutes" type="number" :min="form.vendor==='711proxy'?5:1" :max="sessionMax" required /><small>默认 60 分钟。同一次登录及授权复用会话；切备用后重新登录。</small></label><p v-if="form.vendor==='other'" class="table-note" v-pre>在账号或密码中使用 {country}、{session}、{minutes} 占位符。</p></template>
        <label class="field"><span>代理地址</span><input v-model="form.url" spellcheck="false" placeholder="http://user:pass@host:port" required /><small>也支持 host:port:username:password</small></label>
        <div class="compact-actions">
          <button class="btn ghost" type="button" @click="parseURL()">解析线路</button>
          <button class="btn ghost" type="button" :disabled="busy || Boolean(batch.mode)" @click="test()"><CheckCircle2 :size="15" />测试连接</button>
        </div>
        <MessageBar :message="message" />
        <div class="panel-actions"><button class="btn primary" type="submit" :disabled="busy || Boolean(batch.mode)"><Save :size="15" />{{ form.id ? '保存修改' : '保存代理' }}</button></div>
      </form>

      <section class="panel list-panel">
        <div class="panel-title responsive">
          <div><span>PROFILES</span><h2>代理线路</h2></div>
          <div class="heading-actions">
            <span class="muted-count">{{ total }} 个配置</span>
            <button class="btn ghost" type="button" :disabled="Boolean(batch.mode) || !proxies.length" @click="testAll('connectivity')"><Gauge :size="15" />测试全部线路</button>
            <button class="btn ghost" type="button" :disabled="Boolean(batch.mode) || !proxies.length" @click="testAll('quality')"><ShieldCheck :size="15" />测试全部 OpenAI 质量</button>
          </div>
        </div>
        <div v-if="batch.mode" class="proxy-batch-progress">
          <LoaderCircle class="spin" :size="15" />
          <span>{{ batch.mode === 'quality' ? '正在检测 OpenAI 质量' : '正在检测线路出口' }}</span>
          <strong>{{ batch.completed }} / {{ batch.total }}</strong>
          <i><b :style="{ width: `${batch.total ? batch.completed / batch.total * 100 : 0}%` }"></b></i>
        </div>
        <div class="table-shell">
          <table class="proxy-list-table">
            <thead><tr><th>名称</th><th>地址</th><th>出口 IP / 地区</th><th>连通性</th><th>OpenAI 质量</th><th>使用状态</th><th class="actions-column">操作</th></tr></thead>
            <tbody>
              <tr v-if="!pagedProxies.length"><td colspan="7" class="empty-cell">暂无代理配置</td></tr>
              <tr v-for="proxy in pagedProxies" :key="proxy.id">
                <td><strong>{{ proxy.name }}</strong><small v-if="proxy.vendor" class="table-note">{{ vendors[proxy.vendor] }} · {{proxy.country}} · 会话 {{proxy.session_minutes}} 分钟</small><small class="table-note" :title="health[proxy.id]?.message">{{healthError||globalProxyHealthText(health[proxy.id],healthNow)}}</small><small v-if="health[proxy.id]?.cooldown_until" class="table-note">恢复时间 {{new Date(health[proxy.id].cooldown_until).toLocaleString()}}</small></td>
                <td class="mono" :title="maskProxyURL(proxy.url)">{{ maskProxyURL(proxy.url) }}</td>
                <td>
                  <div v-if="proxyExit(proxy)?.exit_ip || proxyExit(proxy)?.ip_address" class="proxy-result">
                    <strong class="mono">{{ proxyExit(proxy).exit_ip || proxyExit(proxy).ip_address }}</strong>
                    <small :title="proxyLocation(proxy)">{{ proxyLocation(proxy) || proxyExit(proxy).country_code || '地区未知' }}</small>
                  </div>
                  <span v-else class="muted-count">—</span>
                </td>
                <td>
                  <template v-if="tests.get(proxy.id)">
                    <StatusPill :tone="tests.get(proxy.id).reachable ? 'success' : 'danger'">{{ tests.get(proxy.id).reachable ? '可用' : '不可用' }}</StatusPill>
                    <small class="table-note" :title="tests.get(proxy.id).message">{{ tests.get(proxy.id).latency_ms }}ms · {{ tests.get(proxy.id).message }}</small>
                  </template>
                  <StatusPill v-else tone="pending">未测试</StatusPill>
                </td>
                <td :title="qualityTitle(qualityTests.get(proxy.id))">
                  <template v-if="qualityTests.get(proxy.id)">
                    <StatusPill :tone="qualityTone(qualityTests.get(proxy.id).status)">{{ qualityLabel(qualityTests.get(proxy.id).status) }}</StatusPill>
                    <small class="table-note">{{ qualityTests.get(proxy.id).grade }} · {{ qualityTests.get(proxy.id).score }} 分 · HTTP {{ qualityTests.get(proxy.id).http_status || '—' }}</small>
                    <small class="table-note">{{ qualityTests.get(proxy.id).latency_ms || 0 }}ms · {{ qualityTests.get(proxy.id).message }}</small>
                    <small v-if="qualityTests.get(proxy.id).cf_ray" class="table-note">cf-ray · {{ qualityTests.get(proxy.id).cf_ray }}</small>
                  </template>
                  <StatusPill v-else tone="pending">未测试</StatusPill>
                </td>
                <td><button class="status-select" :class="{ active: selectedURL === proxy.url }" type="button" :disabled="Boolean(batch.mode)" @click="$emit('select', selectedURL === proxy.url ? '' : proxy.url)"><span></span>{{ selectedURL === proxy.url ? '主用代理' : '设为全局' }}</button></td>
                <td><div class="row-actions">
                  <IconButton label="测试连接与出口 IP" :disabled="testingIDs.has(proxy.id) || Boolean(batch.mode)" @click="test(proxy)"><LoaderCircle v-if="testingIDs.has(proxy.id)" class="spin" :size="15" /><CheckCircle2 v-else :size="15" /></IconButton>
                  <IconButton label="测试 OpenAI 质量" :disabled="qualityTestingIDs.has(proxy.id) || Boolean(batch.mode)" @click="runQualityTest(proxy, true)"><LoaderCircle v-if="qualityTestingIDs.has(proxy.id)" class="spin" :size="15" /><ShieldCheck v-else :size="15" /></IconButton>
                  <IconButton label="编辑代理" :disabled="Boolean(batch.mode)" @click="edit(proxy)"><Pencil :size="15" /></IconButton>
                  <IconButton label="删除代理" :disabled="Boolean(batch.mode)" danger @click="remove(proxy)"><Trash2 :size="15" /></IconButton>
                </div></td>
              </tr>
            </tbody>
          </table>
        </div>
        <Pagination :page="page" :page-size="pageSize" :total="total" @update:page="setPage" @update:page-size="setPageSize" />
      </section>
    </div>
  </section>
</template>

<style scoped>
.proxies-view .proxy-layout{display:grid;grid-template-columns:minmax(0,1fr)!important;gap:24px}.proxies-view .editor-panel{display:grid;grid-template-columns:minmax(160px,.6fr) minmax(240px,1.4fr) auto;gap:18px;align-items:start;padding:24px;border-radius:16px}.proxies-view .editor-panel>.panel-title{grid-column:1/-1;margin:0}.proxies-view .editor-panel>.field{margin:0}.proxies-view .editor-panel>.compact-actions{padding-top:24px;flex-wrap:wrap}.proxies-view .editor-panel>.message-bar{grid-column:1/-1;margin:0}.proxies-view .editor-panel>.panel-actions{grid-column:1/-1;margin:0;border-top:1px solid var(--line);padding-top:16px}.proxies-view .list-panel{border-radius:16px;padding:24px;min-width:0}.proxies-view .table-shell{overflow:auto}.proxies-view .table-note{max-width:230px;white-space:normal;line-height:1.7}.proxies-view .proxy-list-table td{padding-top:18px;padding-bottom:18px}.proxies-view .proxy-list-table td.mono{max-width:260px;overflow-wrap:anywhere;white-space:normal}.proxies-view .panel-title.responsive{gap:16px;flex-wrap:wrap}@media(max-width:900px){.proxies-view .editor-panel{grid-template-columns:minmax(0,1fr)}.proxies-view .editor-panel>.compact-actions{padding-top:0}.proxies-view .editor-panel,.proxies-view .list-panel{padding:18px}}
.proxy-list-table { min-width: 1120px !important; }
.proxy-result { display: grid; min-width: 125px; max-width: 190px; gap: 3px; }
.proxy-result strong, .proxy-result small { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.proxy-result small { color: var(--muted); font-size: 9px; }
.proxy-batch-progress { display: grid; grid-template-columns: auto 1fr auto; align-items: center; gap: 9px; margin: -3px 0 14px; padding: 9px 11px; border: 1px solid var(--line-soft); border-radius: 5px; background: var(--surface-2); color: var(--text-2); font-size: 10px; }
.proxy-batch-progress > i { grid-column: 1 / -1; height: 4px; overflow: hidden; border-radius: 2px; background: var(--surface-3); }
.proxy-batch-progress > i > b { display: block; height: 100%; border-radius: inherit; background: var(--green); transition: width .2s; }
.spin { animation: proxy-spin .9s linear infinite; }
@keyframes proxy-spin { to { transform: rotate(360deg); } }
@media (min-width: 1261px) {
  .proxy-layout { grid-template-columns: minmax(330px, .45fr) minmax(620px, 1.55fr); }
}
</style>
