<script setup>
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { RefreshCw, Scale, CheckCircle2, AlertCircle, ChevronRight } from 'lucide-vue-next'
import { api } from '../api'
import { usdToR } from '../ejh-profit'
import { reconciliationStatus, reconciliationCards } from '../ejh-reconciliation-view'
import { isClosedCard } from '../card-status'
import { ejhRefreshGate, ejhPageRefreshSeconds } from '../ejhRefresh'
import { formatTime } from '../utils'
import Pagination from './Pagination.vue'

const props = defineProps({ defaultPageSize: { type: Number, default: 10 } })
const emit = defineEmits(['navigate'])
const data = ref(null), loading = ref(false), refreshing = ref(false), error = ref(''), failures = ref([])
const settings = ref(null), progress = ref({ done: 0, total: 0, label: '' }), now = ref(Date.now())
const page = ref(1), pageSize = ref(props.defaultPageSize || 10)
let disposed = false, timer
const refreshSeconds = computed(() => ejhPageRefreshSeconds(data.value?.page_refresh_seconds))
const report = computed(() => reconciliationStatus(data.value || {}, now.value, (settings.value?.sync_interval_seconds || 300) * 2))
const value = computed(() => report.value.value)
const rows = computed(() => reconciliationCards(data.value || {}))
const visible = computed(() => rows.value.slice((page.value - 1) * pageSize.value, page.value * pageSize.value))
const blocks = computed(() => [
  { label: '充值总额', amount: value.value.gross, note: '累计手动充值总账' },
  { label: '余额及在途', amount: value.value.assets, note: '账户 + 卡片 + 待退本金 + 待销卡退款', sign: '−' },
  { label: '总成本', amount: value.value.earnings.costUSD, note: '开卡 / 充值 / 撤销 / 清算 / 退款费', sign: '−' },
  ...(value.value.externalTransfer !== '0.00' ? [{ label: '平台外转出', amount: value.value.externalTransfer, note: '外部消费，不计平台成本', sign: '−' }] : []),
  { label: '待核对差额', amount: value.value.gap, note: '保留差额，不计作成本', sign: '=' },
])
const assets = computed(() => [
  { label: 'EJH 账户余额', amount: value.value.lossReasons.some(r => r.startsWith('EJH')) ? null : value.value.accountUSD, note: formatTime(data.value?.balance?.at) },
  { label: '卡片余额', amount: value.value.cardUSD.missing ? null : value.value.cardUSD.amount ?? '0.00', note: `${rows.value.length} 张 · 含销卡处理中` },
  { label: '退款待销卡结算', amount: value.value.refundSettlement.amount, note: '已有入账退款流水，销卡后转主余额' },
  { label: '已付款在途', amount: value.value.inFlight.paymentUSD.missing ? null : value.value.inFlight.paymentUSD.amount ?? '0.00', note: '尚未退回的卡内付款本金' },
])
const costs = computed(() => [
  { label: '清算退款手续费', amount: value.value.refundFees.amount, note: '按核对登记，已计入总成本' },
  { label: '开卡费', amount: value.value.openingFee, note: `${value.value.openingCount} 张 × 0.50 USD` },
  { label: '充值手续费', amount: value.value.depositFee, note: '充值总额 × 1%' },
  { label: '授权撤销费', amount: value.value.reversal.usd.amount ?? (value.value.reversal.count ? null : '0.00'), note: '逐笔估算，已回款费率锁定' },
  { label: '清算成本', amount: value.value.platformClearingUSD, note: `清算净额 ${value.value.rawClearingUSD??'—'} USD − 平台外转出 ${value.value.externalTransfer??'—'} USD` },
])
watch(pageSize, () => page.value = 1)
watch(() => props.defaultPageSize, n => { pageSize.value = n || 10 })
watch(() => rows.value.length, n => page.value = Math.min(page.value, Math.max(1, Math.ceil(n / pageSize.value))))
async function load() {
  const result = await api('/api/ejh/dashboard')
  if (!disposed) { data.value = result; now.value = Date.now() }
}
async function readSaved() {
  if (disposed) return
  if (loading.value || refreshing.value) { clearTimeout(timer); timer=setTimeout(readSaved,refreshSeconds.value*1000); return }
  loading.value = true
  try { await load(); error.value = '' } catch (e) { if (!disposed) error.value = e.message }
  finally { if (!disposed) { loading.value = false; clearTimeout(timer); timer = setTimeout(readSaved, refreshSeconds.value * 1000) } }
}
async function refresh() {
  if (refreshing.value || loading.value || disposed) return
  const lease = ejhRefreshGate.begin(false)
  refreshing.value = true; failures.value = []; error.value = ''
  try {
    if (lease.reason) {
      progress.value = { done: 0, total: 0, label: '等待正在进行的卡台刷新…' }
      await lease.done
      if (!disposed) await load()
      return
    }
    progress.value = { done: 0, total: 1, label: '正在同步卡片与交易…' }
    const query = async (path, body, label) => {
      progress.value.label = label
      try { await api(path, { method: 'POST', body }) }
      catch (e) { if (!disposed) failures.value.push(label + '：' + e.message) }
      progress.value.done++
    }
    await query('/api/ejh/sync', {}, '同步卡片与交易')
    if (disposed) return
    await load()
    const cards = data.value.cards.filter(c => c.issuer_linked !== false && !isClosedCard(c))
    progress.value.total = cards.length + 2
    await query('/api/ejh/balance', {}, '查询 EJH 账户余额')
    for (const card of cards) {
      if (disposed) break
      await query('/api/ejh/cards/' + encodeURIComponent(card.id) + '/action', { action: 'balance' }, '查询 ' + (card.local?.name || card.remote.cardNo))
    }
    if (!disposed) { await load(); progress.value.label = failures.value.length ? '刷新结束，部分数据保留上次结果' : '已完成核对' }
  } catch (e) { if (!disposed) error.value = e.message }
  finally { lease.finish?.(); if (!disposed) refreshing.value = false }
}
onMounted(async () => {
  await readSaved()
  try { const cfg = await api('/api/ejh/settings'); if (!disposed) settings.value = cfg }
  catch (e) { if (!disposed) error.value = e.message }

})
onBeforeUnmount(() => { disposed = true; clearInterval(timer) })
</script>

<template>
  <div class="reconciliation-page">
    <header class="page-heading"><div><span class="eyebrow">EJH · 财务</span><h1>财务对账</h1><p>钱在哪里、花了多少、还差多少。</p></div><button class="btn primary" :disabled="loading || refreshing || !settings?.enabled" @click="refresh"><RefreshCw :size="15" :class="{spin:refreshing}"/>{{refreshing?'核对中…':'刷新并核对'}}</button></header>
    <div v-if="error" class="notice error" role="alert">{{error}} <button @click="readSaved">重新读取</button></div>
    <div v-if="refreshing" class="refresh-progress" role="status"><span>{{progress.label}}</span><b v-if="progress.total">{{progress.done}} / {{progress.total}}</b><progress v-if="progress.total" :value="progress.done" :max="progress.total"/></div>
    <div v-if="!data" class="empty">{{loading?'正在读取对账数据…':'暂无对账数据'}}</div>
    <template v-else>
      <section class="result-banner" :class="failures.length || error ? 'stale' : report.state" aria-label="对账结果">
        <CheckCircle2 v-if="['balanced','small'].includes(report.state) && !failures.length && !error" :size="24"/><AlertCircle v-else :size="24"/>
        <div><h2>{{failures.length || error ? '刷新未完成，保留上次数据' : report.label}}</h2><p>{{report.state==='unknown'?'补齐数据后才能计算完整差额。':'差额为估算值，正数表示少于充值总额，负数表示多于充值总额。'}}</p></div>
        <span class="tolerance">≤ {{report.tolerance}} USD 为小额差异</span>
      </section>
      <section class="equation" aria-label="资金核对公式"><article v-for="(block,index) in blocks" :key="block.label" :class="{difference:block.sign==='='}"><span v-if="block.sign" class="math-sign">{{block.sign}}</span><span class="block-label">{{block.label}}</span><strong>{{block.amount??'—'}} <em>USD</em></strong><small>{{usdToR(block.amount)??'—'}} R</small><p>{{block.note}}</p></article></section>
      <div v-if="report.issues.length || failures.length" class="notice" role="status"><p v-for="issue in [...new Set([...report.issues, ...failures])]" :key="issue">{{issue}}</p></div>
      <div class="breakdowns">
        <section class="panel"><header><h2>钱在哪里</h2><span>余额及在途</span></header><div v-for="item in assets" :key="item.label" class="amount-row"><div><b>{{item.label}}</b><small>{{item.note}}</small></div><div class="amount"><strong>{{item.amount??'—'}} <em>USD</em></strong><small>{{usdToR(item.amount)??'—'}} R</small></div></div><footer>实际到账 {{value.net??'—'}} USD · 已扣充值手续费</footer></section>
        <section class="panel"><header><h2>成本构成</h2><span>{{value.earnings.costIncomplete?'部分待核实':'含估算'}}</span></header><div v-for="item in costs" :key="item.label" class="amount-row"><div><b>{{item.label}}</b><small>{{item.note}}</small></div><div class="amount"><strong>{{item.amount??'—'}} <em>USD</em></strong><small>{{usdToR(item.amount)??'—'}} R</small></div></div></section>
      </div>
      <section class="supplement"><Scale :size="18"/><div><strong>实际损耗 {{value.loss??'—'}} USD <span> / {{value.lossR??'—'}} R</span></strong><p>实际到账 − 平台外转出 − 账户余额 − 卡片余额 − 在途本金 − 退款待销卡结算。已含上述部分成本，不再重复扣减。</p></div><button class="text-link" @click="emit('navigate','ejh-finance')">查看成本与利润 <ChevronRight :size="14"/></button></section>
      <details class="panel explanation"><summary>撤销金额差异 <b>{{value.reconciliation.shortfallUSD.amount??'0.00'}} USD</b> · 不计成本</summary><p>原付款与撤销回款的差额，按销卡退回原金额的口径展示。</p><p>未销卡 {{value.reconciliation.pendingUSD.amount??'0.00'}} USD（{{usdToR(value.reconciliation.pendingUSD.amount??'0.00')}} R）；已销卡历史 {{value.reconciliation.completedUSD.amount??'0.00'}} USD（{{usdToR(value.reconciliation.completedUSD.amount??'0.00')}} R）。</p><p>其中 {{value.closurePending??'—'}} USD 尚未计入在途；扣除此项后，其余待核对 {{value.unexplained??'—'}} USD。已计在途的金额不会再扣一次。</p></details>
      <details class="panel card-details"><summary>逐卡余额与在途 <span>{{rows.length}} 张</span></summary><div class="table-wrap"><table><thead><tr><th>卡片</th><th>卡内余额</th><th>余额 USD</th><th>在途 USD</th><th>退款待结算 USD</th><th>合计 USD</th><th>余额查询时间</th></tr></thead><tbody><tr v-for="row in visible" :key="row.id"><td>{{row.name}}<small v-if="row.processing">销卡处理中</small></td><td>{{row.original??'—'}} {{row.currency}}</td><td>{{row.balance??'—'}}</td><td>{{row.pending??'—'}}</td><td>{{row.refund??'—'}}</td><td><b>{{row.total??'—'}}</b></td><td>{{formatTime(row.at)}}<small v-if="row.error" class="error">查询失败，保留旧数据</small></td></tr><tr v-if="!visible.length"><td colspan="7" class="empty">暂无未销卡卡片</td></tr></tbody></table></div><Pagination :page="page" :page-size="pageSize" :total="rows.length" @update:page="page=$event" @update:page-size="pageSize=$event"/></details>
      <footer class="footnote">按累计数据核对 · 1 USD = 6.7 R · 卡币按开卡结算比例折算，撤销费按配置比例估算。<br>数据读取每 {{refreshSeconds}} 秒更新；“刷新并核对”会查询卡台最新余额及交易。消费收益不参与充值资金对账。</footer>
    </template>
  </div>
</template>

<style scoped>
.reconciliation-page{display:grid;gap:20px;color:var(--text);font-variant-numeric:tabular-nums}.page-heading{display:flex;justify-content:space-between;align-items:center;gap:16px}.eyebrow{font-size:10px;letter-spacing:1.8px;color:var(--green-strong)}h1{font-size:26px;margin:6px 0}h2{font-size:15px;margin:0}.page-heading p,.result-banner p{margin:7px 0 0;font-size:12px;color:var(--muted)}.btn,.text-link{display:inline-flex;align-items:center;gap:7px}.result-banner{display:flex;align-items:center;gap:14px;padding:20px 24px;border:1px solid var(--line);border-radius:12px;background:var(--surface)}.result-banner>svg{flex:none}.result-banner.balanced,.result-banner.small{background:var(--green-bg);color:var(--green-strong)}.result-banner.difference{border-color:#d49a4c;color:#ab6b17;background:color-mix(in srgb,var(--surface) 93%,#eda22c)}.result-banner.stale,.result-banner.unknown{color:var(--muted)}.tolerance{margin-left:auto;font-size:11px;white-space:nowrap}.equation{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:18px}.equation article{position:relative;padding:24px;border:1px solid var(--line);border-radius:12px;background:var(--surface);min-width:0}.equation .difference{background:var(--green-bg)}.math-sign{position:absolute;left:-18px;top:48px;width:18px;text-align:center;font-size:20px;color:var(--muted)}.block-label{display:block;font-size:12px;color:var(--muted)}.equation strong{display:block;font-size:32px;margin:18px 0 7px;overflow-wrap:anywhere;letter-spacing:-.6px}em{font-size:11px;font-style:normal;font-weight:400;color:var(--muted)}.equation small{font-size:13px}.equation p{font-size:10px;color:var(--muted);margin:16px 0 0}.breakdowns{display:grid;grid-template-columns:1fr 1fr;gap:20px}.panel{background:var(--surface);border:1px solid var(--line);border-radius:12px;overflow:hidden}.panel>header{display:flex;align-items:center;justify-content:space-between;gap:10px;padding:20px 24px;border-bottom:1px solid var(--line)}.panel header span,.panel footer{font-size:11px;color:var(--muted)}.amount-row{display:flex;justify-content:space-between;align-items:center;gap:16px;margin:0 24px;padding:17px 0}.amount-row+.amount-row{border-top:1px solid var(--line)}.amount-row b{font-size:12px;font-weight:500}small{display:block;color:var(--muted)}.amount-row small{font-size:10px;margin-top:5px;line-height:1.5}.amount{text-align:right}.amount strong{font-size:19px;font-weight:600}.panel footer{padding:15px 24px;border-top:1px solid var(--line)}.supplement{display:flex;align-items:center;gap:12px;padding:18px 24px;border:1px solid var(--line);border-radius:12px;background:var(--surface)}.supplement strong{font-size:13px}.supplement span{font-weight:400;color:var(--muted)}.supplement p,.explanation p{font-size:11px;color:var(--muted);line-height:1.8;margin:5px 0 0}.text-link{color:var(--green-strong);font-size:12px;margin-left:auto;white-space:nowrap}summary{padding:18px 24px;cursor:pointer;font-size:13px}summary span,summary b{font-size:12px;margin-left:8px}.explanation p{padding:0 24px}.explanation p:last-child{padding-bottom:18px}.table-wrap{overflow:auto}table{width:100%;border-collapse:collapse;white-space:nowrap}th,td{text-align:left;padding:14px 20px;border-top:1px solid var(--line);font-size:12px}th{font-size:10px;color:var(--muted);font-weight:500;background:var(--bg)}td small{margin-top:5px;font-size:10px}.notice{padding:14px 20px;border-left:3px solid #d49a4c;background:var(--surface);font-size:12px;line-height:1.8}.notice p{margin:0}.error{color:var(--red)}.notice button{color:var(--green-strong);margin-left:12px}.empty{text-align:center;padding:32px;color:var(--muted);font-size:13px}.footnote{font-size:10px;color:var(--muted);line-height:1.9}.refresh-progress{display:flex;align-items:center;gap:12px;flex-wrap:wrap;font-size:12px;padding:16px 20px;background:var(--surface);border:1px solid var(--line);border-radius:10px}.refresh-progress b{margin-left:auto}.refresh-progress progress{width:100%;height:6px;accent-color:var(--green-strong)}.spin{animation:spin 1s linear infinite}@keyframes spin{to{transform:rotate(360deg)}}@media(max-width:1050px){.equation{grid-template-columns:repeat(2,minmax(0,1fr))}.equation article:nth-child(3) .math-sign{display:none}.equation strong{font-size:28px}}@media(max-width:700px){.breakdowns{grid-template-columns:1fr}.result-banner{flex-wrap:wrap}.tolerance{margin-left:38px}.equation article{padding:18px}.equation strong{font-size:23px}.supplement{align-items:flex-start;flex-wrap:wrap}.supplement .text-link{margin-left:30px}.result-banner,.panel>header{padding:18px}.amount-row{margin:0 18px}.page-heading h1{font-size:24px}}
</style>
