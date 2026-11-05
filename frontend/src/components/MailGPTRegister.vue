<script setup>
import { computed, onBeforeUnmount, onMounted, reactive, ref, watch } from 'vue';
import { api } from '../api';
import { formatTime } from '../utils';
import MessageBar from './MessageBar.vue';

const props = defineProps({ visible: Boolean });
const emit = defineEmits(['imported', 'show-settings']);
const base = '/api/mail/gptregister';
const intentStorage = 'space.gptregister.pending-intent.v1';
const settings = reactive({ url: '', api_key: '', key_present: false, enter_pro: false });
const catalog = ref(null);
const jobs = ref([]);
const notice = ref({ text: '', type: '' });
const action = ref('');
const listLoading = ref(false);
const modal = ref(false);
const form = reactive({ quantity: 1, need_oauth: false });
const pending = ref(null);
const receipts = ref(null);
const receiptTitle = ref('');
let timer, disposed = false, importedSeen;
const activeJobs = computed(() => jobs.value.filter(j => ['queued', 'syncing', 'retrying'].includes(j.state)));
const selectedProduct = computed(() => catalog.value?.catalog.products.find(p => p.need_oauth === form.need_oauth));
const maximum = computed(() => catalog.value?.catalog.max_quantity || 1000);
const stateNames = { queued: '等待处理', syncing: '正在同步', retrying: '等待重试', paused: '同步暂停', completed: '同步完成' };
const remoteNames = { queued: '排队中', running: '注册中', review: '平台核实中', completed: '全部交付', partial: '部分交付', failed: '注册失败', cancelled: '已取消' };

function message(text, type = 'error') { notice.value = { text, type }; }
function readIntent() {
  const raw = localStorage.getItem(intentStorage);
  if (!raw) return null;
  const value = JSON.parse(raw);
  if (!value.request_key || !Number.isInteger(value.quantity) || typeof value.need_oauth !== 'boolean') throw new Error('本地待确认请求无效，请先查看注册记录确认原任务');
  return value;
}
async function loadSettings() {
  const value = await api(`${base}/settings`);
  Object.assign(settings, value, { api_key: '' });
}
async function loadCatalog() {
  catalog.value = await api(`${base}/catalog`);
}
function schedule() {
  clearTimeout(timer);
  if (!disposed && activeJobs.value.length) timer = setTimeout(() => {
    if (document.hidden) schedule();
    else loadJobs().catch(e => { message(e.message); schedule(); });
  }, 5000);
}
async function loadJobs() {
  if (listLoading.value || disposed) return;
  listLoading.value = true;
  try {
    const next = await api(`${base}/jobs`);
    const counts = new Map(next.map(j => [j.id, j.imported]));
    if (importedSeen && next.some(j => j.imported > (importedSeen.get(j.id) || 0))) emit('imported');
    importedSeen = counts;
    jobs.value = next;
  } finally { listLoading.value = false; schedule(); }
}
async function refresh() {
  try { await loadJobs(); } catch (e) { message(e.message); }
}
async function save() {
  if (action.value) return;
  action.value = 'save';
  try {
    const value = await api(`${base}/settings`, { method: 'PUT', body: { url: settings.url, api_key: settings.api_key, enter_pro: settings.enter_pro } });
    Object.assign(settings, value, { api_key: '' });
    catalog.value = null;
    message('配置已保存，已提交任务继续使用提交时的设置。', 'success');
  } catch (e) { message(e.message); } finally { action.value = ''; }
}
async function test() {
  if (action.value) return;
  action.value = 'test';
  try {
    catalog.value = await api(`${base}/test`, { method: 'POST', timeoutMs: 30000, body: { url: settings.url, api_key: settings.api_key, enter_pro: settings.enter_pro } });
    message('连接成功，余额和价格已更新。测试不会创建注册订单。', 'success');
  } catch (e) { message(e.message); } finally { action.value = ''; }
}
async function openRegister() {
  if (action.value) return;
  action.value = 'open';
  try {
    pending.value = readIntent();
    if (pending.value) Object.assign(form, { quantity: pending.value.quantity, need_oauth: pending.value.need_oauth });
    else Object.assign(form, { quantity: 1, need_oauth: false });
    await loadSettings();
    modal.value = true;
    notice.value = { text: '', type: '' };
    try { await loadCatalog(); } catch (e) { catalog.value = null; message(e.message); }
  } catch (e) { message(e.message); emit('show-settings'); } finally { action.value = ''; }
}
async function submit() {
  if (action.value) return;
  action.value = 'submit';
  try {
    // Persist the exact intent before the POST. Lost responses and reloads
    // replay that intent, never create another paid registration order.
    let intent = readIntent();
    if (!intent) {
      if (!Number.isInteger(form.quantity) || form.quantity < 1 || form.quantity > maximum.value) throw new Error(`数量须为 1 至 ${maximum.value} 的整数`);
      const bytes = crypto.getRandomValues(new Uint8Array(16));
      intent = { request_key: `space-${Array.from(bytes, b => b.toString(16).padStart(2, '0')).join('')}`, quantity: form.quantity, need_oauth: form.need_oauth };
      localStorage.setItem(intentStorage, JSON.stringify(intent));
    }
    pending.value = intent;
    const job = await api(`${base}/jobs`, { method: 'POST', body: intent, timeoutMs: 20000 });
    // The server acknowledgement is the only point where a new intent is safe.
    localStorage.removeItem(intentStorage);
    pending.value = null;
    modal.value = false;
    message(`注册任务已接收：${job.quantity} 个${job.need_oauth ? ' OAuth' : ''}账号，交付后自动导入。`, 'success');
    emit('show-settings');
    await loadJobs();
  } catch (e) {
    if (e.status === 400 && pending.value) { localStorage.removeItem(intentStorage); pending.value = null; }
    message(e.message);
  } finally { action.value = ''; }
}
async function retry(job) {
  if (action.value) return;
  action.value = job.id;
  try {
    await api(`${base}/jobs/${encodeURIComponent(job.id)}/retry`, { method: 'POST', body: {} });
    await loadJobs();
    message('已安排继续原任务，不会重复创建订单。', 'success');
  } catch (e) { message(e.message); } finally { action.value = ''; }
}
async function details(job) {
  try {
    receipts.value = await api(`${base}/jobs/${encodeURIComponent(job.id)}/receipts`);
    receiptTitle.value = job.id;
  } catch (e) { message(e.message); }
}
watch(() => props.visible, visible => { if (visible) refresh(); });
onMounted(() => { Promise.all([loadSettings(), loadJobs()]).catch(e => message(e.message)); });
onBeforeUnmount(() => { disposed = true; clearTimeout(timer); });
defineExpose({ openRegister, refresh });
</script>

<template>
  <div v-show="visible" class="register-stack">
    <section class="panel register-config">
      <div class="panel-title responsive"><div><span>GPT REGISTER</span><h2>注册平台配置</h2></div><button class="btn primary" :disabled="!!action" @click="openRegister">注册账号</button></div>
      <p class="register-muted">连接 GPT Register，成功交付的账号会逐条自动导入。选择 OAuth 时，等待该账号的 OAuth 资料齐全后导入。</p>
      <div class="register-fields">
        <label class="field">平台地址<input v-model="settings.url" placeholder="https://register.example.com" autocomplete="off" /></label>
        <label class="field">API Key<input v-model="settings.api_key" type="password" autocomplete="new-password" :placeholder="settings.key_present ? '已保存，留空保持原密钥' : '填写平台开放 API 密钥'" /></label>
      </div>
      <p class="register-muted">密钥需具备 orders:write、orders:read、accounts:read、credentials:read 权限。连接测试只验证账户和价格读取。</p>
      <label class="register-check"><input v-model="settings.enter_pro" type="checkbox" />导入后直接进入 Pro 管理</label>
      <p class="register-muted">勾选后，账号归属 Pro 管理，不显示在邮件账号列表。只导入账号，不自动发起开通；已存在邮箱保留原资料和归属。</p>
      <div class="heading-actions"><button class="btn primary" :disabled="!!action" @click="save">{{ action === 'save' ? '保存中…' : '保存配置' }}</button><button class="btn ghost" :disabled="!!action" @click="test">{{ action === 'test' ? '测试中…' : '测试连接 / 刷新余额' }}</button></div>
      <MessageBar :message="notice" />
      <div v-if="catalog" class="register-metrics">
        <div><small>平台账户</small><strong>{{ catalog.user.email }}</strong></div>
        <div><small>可用积分</small><strong>{{ catalog.user.points }}</strong></div>
        <div><small>冻结积分</small><strong>{{ catalog.user.frozen }}</strong></div>
        <div v-for="p in catalog.catalog.products" :key="String(p.need_oauth)"><small>{{ p.need_oauth ? 'OAuth 账号单价' : '标准账号单价' }}</small><strong>{{ p.unit_price }} 积分</strong></div>
        <div><small>服务状态</small><strong>{{ catalog.catalog.orders_open ? '开放注册' : '暂停注册' }}</strong><small>单次最多 {{ catalog.catalog.max_quantity }} 个</small></div>
      </div>
    </section>
    <section class="panel register-config">
      <div class="panel-title responsive"><div><span>DELIVERY HISTORY</span><h2>注册与导入记录</h2></div><button class="btn ghost" :disabled="listLoading" @click="refresh">{{ listLoading ? '刷新中…' : '刷新记录' }}</button></div>
      <p class="register-muted">进行中的任务每 5 秒更新进度，关闭页面仍由后台同步。已处理交付永久去重，删除邮件账号后也不会重新拉取。显示最近 100 条记录及全部未完成任务。</p>
      <div class="register-table"><table><thead><tr><th>任务 / 时间</th><th>数量 / 类型</th><th>导入位置</th><th>进度</th><th>积分</th><th>状态</th><th>操作</th></tr></thead><tbody>
        <tr v-for="job in jobs" :key="job.id"><td><code>{{ job.remote_order_id || job.id }}</code><small>{{ formatTime(job.created_at) }}</small><small>{{ job.platform_url }}</small></td><td>{{ job.quantity }} 个<small>{{ job.need_oauth ? 'OAuth 账号' : '标准账号' }}</small></td><td>{{ job.enter_pro ? 'Pro 管理' : '邮件账号' }}</td><td>交付 {{ job.delivered }} / {{ job.quantity }}<small>导入 {{ job.imported }} · 跳过 {{ job.skipped }}</small></td><td>单价 {{ job.unit_price }}<small>扣除 {{ job.charged }} · 释放 {{ job.released }}</small></td><td><strong :class="{ 'register-error': job.state === 'paused' }">{{ stateNames[job.state] || job.state }}</strong><small>{{ remoteNames[job.remote_status] || '等待平台接单' }}</small><small>{{ job.message }}</small></td><td><button class="btn ghost" @click="details(job)">导入明细</button><button v-if="job.state === 'paused' || job.state === 'retrying'" class="btn ghost" :disabled="!!action" @click="retry(job)">继续原任务</button></td></tr>
        <tr v-if="!jobs.length"><td colspan="7" class="register-muted">暂无注册任务。保存平台配置后，可在邮件账号页点击“注册”。</td></tr>
      </tbody></table></div>
    </section>
  </div>
  <Teleport to="body">
    <div v-if="modal" class="register-backdrop" @click.self="!action && (modal = false)">
      <form class="panel register-dialog" role="dialog" aria-modal="true" aria-labelledby="register-title" @submit.prevent="submit">
        <div class="panel-title"><h2 id="register-title">注册账号</h2><button class="btn ghost" type="button" :disabled="!!action" @click="modal = false">关闭</button></div>
        <label class="field">注册数量<input v-model.number="form.quantity" type="number" min="1" :max="maximum" step="1" required :disabled="!!pending || !!action" /></label>
        <label class="register-check"><input v-model="form.need_oauth" type="checkbox" :disabled="!!pending || !!action" />获取 OAuth（包含 RT / AT）</label>
        <p v-if="selectedProduct">单价 {{ selectedProduct.unit_price }} 积分 · 预计 {{ selectedProduct.unit_price * form.quantity }} 积分</p>
        <p>交付后逐条导入到<strong>{{ catalog?.settings.enter_pro ?? settings.enter_pro ? ' Pro 管理' : '邮件账号' }}</strong>。</p>
        <p v-if="pending" class="register-muted">存在待确认的提交，继续会查询或恢复同一任务；数量与 OAuth 选项保持原样。</p>
        <MessageBar :message="notice" />
        <div class="heading-actions"><button class="btn ghost" type="button" :disabled="!!action" @click="modal = false; emit('show-settings')">平台配置与记录</button><button class="btn primary" type="submit" :disabled="!!action || (!pending && !catalog?.catalog.orders_open)">{{ action === 'submit' ? '提交中…' : pending ? '确认原任务' : '提交注册' }}</button></div>
      </form>
    </div>
    <div v-if="receipts !== null" class="register-backdrop" @click.self="receipts = null"><section class="panel register-dialog register-wide" role="dialog" aria-modal="true" aria-label="导入明细"><div class="panel-title"><h2>导入明细</h2><button class="btn ghost" @click="receipts = null">关闭</button></div><small>{{ receiptTitle }}</small><div class="register-table"><table><thead><tr><th>邮箱</th><th>结果</th><th>说明</th></tr></thead><tbody><tr v-for="r in receipts" :key="r.account_id"><td>{{ r.email }}</td><td>{{ r.result === 'imported' ? '已导入' : '已跳过' }}</td><td>{{ r.reason }}</td></tr><tr v-if="!receipts.length"><td colspan="3">暂未收到可导入的交付账号。</td></tr></tbody></table></div></section></div>
  </Teleport>
</template>

<style scoped>
.register-stack{display:grid;gap:20px}.register-config{padding:24px}.register-fields{display:grid;grid-template-columns:1fr 1fr;gap:18px;margin:20px 0 8px}.register-muted{color:var(--muted,#6b7280);font-size:13px;line-height:1.8;margin:10px 0 16px}.register-check{display:flex;gap:9px;align-items:center;margin:16px 0}.register-check input{width:16px;height:16px}.register-metrics{display:grid;grid-template-columns:repeat(auto-fit,minmax(155px,1fr));gap:12px;margin-top:18px}.register-metrics>div{padding:16px;border:1px solid var(--border,#e5e7eb);border-radius:12px;display:grid;gap:8px}.register-metrics strong{overflow-wrap:anywhere}.register-metrics small,.register-table small{display:block;color:var(--muted,#6b7280);font-size:12px;line-height:1.7}.register-table{overflow:auto}.register-table table{width:100%;border-collapse:collapse;font-size:13px}.register-table td,.register-table th{padding:13px 10px;text-align:left;border-bottom:1px solid var(--border,#e5e7eb);vertical-align:top}.register-table td:first-child{min-width:190px;max-width:260px;overflow-wrap:anywhere}.register-table td:nth-child(6){min-width:200px;max-width:320px}.register-table code{font-size:11px;overflow-wrap:anywhere}.register-error{color:#b45309}.register-backdrop{position:fixed;inset:0;z-index:1300;background:#0f172a66;display:grid;place-items:center;padding:24px}.register-dialog{background:var(--surface,#fff);padding:24px;width:min(520px,100%);max-height:90vh;overflow:auto;display:grid;gap:14px;border-radius:16px;box-shadow:0 20px 70px #0f172a33}.register-dialog .panel-title{display:flex;align-items:center;justify-content:space-between}.register-dialog p{margin:0;font-size:14px;line-height:1.8}.register-wide{width:min(860px,100%)}@media(max-width:700px){.register-fields{grid-template-columns:1fr}.register-config{padding:16px}.register-backdrop{padding:12px}}
</style>
