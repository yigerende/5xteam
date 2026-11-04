<script setup>
import PaymentOnlySummary from './PaymentOnlySummary.vue'
import { computed, nextTick, ref } from 'vue'
import { RefreshCw, Wallet, Pencil } from 'lucide-vue-next'
import { api } from '../api'
import { summarizeCardBalances } from '../ejh'
import { summarizeCardBalancesUSD } from '../ejh-card-funding'
import { summarizeInFlightPayments } from '../ejh-account-amounts'
import { formatTime } from '../utils'
import EJHRefundFees from './EJHRefundFees.vue'
import EJHFinanceTotals from './EJHFinanceTotals.vue'
import EJHReconciliationBar from './EJHReconciliationBar.vue'
import EJHExternalTransfer from './EJHExternalTransfer.vue'

const props = defineProps({ data: { type: Object, required: true }, busy: Boolean, ready: Boolean, enabled: Boolean, progress: { type: Object, default: null }, failures: { type: Array, default: () => [] } })
const display = value => props.enabled ? value : '---'
const emit = defineEmits(['refresh', 'retry-failed', 'ledger-updated', 'refund-fee-saved'])
const balances = computed(() => props.data._overview?.balances || summarizeCardBalances(props.data.cards))
const usd = computed(() => props.data._overview?.usd || summarizeCardBalancesUSD(props.data.cards, props.data.operations))
const inFlight = computed(() => props.data._overview?.inFlight || summarizeInFlightPayments(props.data.cases, props.data.cards, props.data.operations, props.data.trades))
const ledger = computed(() => props.data.recharge_ledger || { amount: '' })
const editing = ref(false), saving = ref(false), draft = ref(''), error = ref(''), amountInput = ref(null)
const valid = computed(() => draft.value.trim() === '' || /^\d{1,12}(\.\d{1,2})?$/.test(draft.value.trim()))
async function editLedger() {
  if (props.busy || saving.value) return
  draft.value = ledger.value.amount || ''; error.value = ''; editing.value = true
  await nextTick(); amountInput.value?.focus()
}
async function saveLedger() {
  if (props.busy || saving.value || !valid.value) return
  saving.value = true; error.value = ''
  try {
    const result = await api('/api/ejh/recharge-ledger', { method: 'PUT', body: { amount: draft.value.trim() } })
    emit('ledger-updated', result); editing.value = false
  } catch (e) { error.value = e.message }
  finally { saving.value = false }
}
</script>

<template>
 <section class="ejh-balance-overview" aria-label="余额总览">
  <header class="overview-header"><div><Wallet :size="18"/><h2>余额总览</h2></div><div class="overview-actions"><EJHRefundFees :trades="data.trades||[]" :cards="data.cards||[]" :records="data.refund_fees||[]" :disabled="busy||!ready" @saved="emit('refund-fee-saved',$event)"/><button v-if="failures.length" class="btn ghost" type="button" :disabled="busy||saving||!ready" @click="emit('retry-failed')"><RefreshCw :size="14"/>重试失败项（{{failures.length}}）</button><button class="btn primary" type="button" :disabled="busy||saving||!ready" @click="emit('refresh')"><RefreshCw :size="14" :class="{spin:progress}"/>{{progress?'刷新中 '+progress.done+' / '+progress.total:'刷新数据'}}</button></div></header>
  <p v-if="!enabled" class="overview-disabled">卡台已关闭，可手动查询和操作。</p>
  <EJHReconciliationBar :data="data" :masked="!enabled"/>
  <div class="overview-grid">
   <div class="overview-ledger">
    <div class="overview-label"><span>充值总账</span><button v-if="!editing" type="button" class="ledger-edit" :disabled="busy||saving||!ready" aria-label="修改充值总账 USD" @click="editLedger"><Pencil :size="12"/>{{ledger.amount===''?'填写':'修改'}}</button></div>
    <form v-if="editing" class="ledger-form" aria-label="充值总账 USD" @submit.prevent="saveLedger">
     <label class="ledger-input"><input ref="amountInput" v-model="draft" type="text" inputmode="decimal" maxlength="15" placeholder="输入累计充值金额" aria-label="充值总账金额 USD" :disabled="saving" :aria-invalid="!valid"/><span>USD</span></label>
     <div class="ledger-actions"><button class="btn primary compact" type="submit" :disabled="busy||saving||!valid">{{saving?'保存中…':'保存'}}</button><button class="btn ghost compact" type="button" :disabled="saving" @click="editing=false">取消</button></div>
     <small v-if="!valid" class="danger-text">请输入非负金额，最多两位小数。</small><small v-else>填写累计总额；留空可清除。</small>
     <p v-if="error" role="alert" class="danger-text">{{error}}</p>
    </form>
    <template v-else><div class="overview-values"><div><strong>{{display(ledger.amount===''?'—':ledger.amount)}}</strong><span>USD</span></div></div><small>手动记录<span v-if="ledger.updated_at"> · {{display(formatTime(ledger.updated_at))}} 更新</span></small></template>
    <EJHExternalTransfer :ledger="ledger" :disabled="busy||saving||editing||!ready" :masked="!enabled" @saved="emit('ledger-updated',$event)"/>
    <PaymentOnlySummary issuer="ejh" :data="data" :masked="!enabled"/>
   </div>
   <div>
    <div class="overview-label"><span>EJH 账户余额</span></div>
    <div v-if="data.balance?.data?.abroadList?.length" class="overview-values"><div v-for="b in data.balance.data.abroadList" :key="b.currency"><strong>{{display(b.balance)}}</strong><span>{{b.currency}}</span></div></div><div v-else class="overview-placeholder">{{display('尚未查询')}}</div>
    <small>查询于 {{display(formatTime(data.balance?.at))}}</small>
   </div>
   <div>
    <div class="overview-label"><span>卡片余额汇总</span><span>{{display(balances.known)}} / {{display(balances.total)}} 张</span></div>
    <div v-if="balances.totals.length" class="overview-values"><div v-for="b in balances.totals" :key="b.currency"><strong>{{display(b.amount)}}</strong><span>{{b.currency}}</span></div></div><div v-else class="overview-placeholder">{{display(balances.total?'尚未查询':'暂无未销卡卡片')}}</div>
    <small>正常卡片 · 含销卡处理中</small>
    <div class="overview-usd"><span>{{usd.estimated?'折合 USD（估算）':'USD 合计'}}</span><strong>{{enabled&&usd.estimated?'≈ ':''}}{{display(usd.amount??'—')}} <em>USD</em></strong></div>
    <small v-if="enabled&&usd.missing">{{display(usd.missing)}} 张缺少余额或 USD 折算依据，未计入 USD 合计。</small>
    <small v-if="enabled&&balances.failed" class="danger-text">{{display(balances.failed)}} 张查询失败，保留上次余额。</small>
   </div>
   <div class="overview-inflight" aria-label="已付款在途">
    <div class="overview-label"><span>已付款在途</span><span>{{display(inFlight.count)}} 笔待回款</span></div>
    <dl class="inflight-amounts">
     <div v-for="[kind,label,missing] in [['payment','卡内付款',inFlight.missingPayment],['settlement','结算金额',inFlight.missingSettlement]]" :key="kind">
      <dt>{{label}}</dt><dd><strong v-for="amount in inFlight[kind]" :key="amount.currency">{{display(amount.amount)}} <em>{{amount.currency}}</em></strong><strong v-if="!inFlight[kind].length">{{display(missing?'待核实':kind==='settlement'&&inFlight.pendingSettlement?'待结算':'0.00')}}</strong><small v-if="missing">{{display(missing)}} 笔金额待核实，未计入</small><small v-if="kind==='settlement'&&inFlight.pendingSettlement&&(inFlight.settlement.length||missing)">{{display(inFlight.pendingSettlement)}} 笔待结算，未计入</small></dd>
     </div>
    </dl>
    <div class="overview-usd" title="按各卡初始开卡的 USD 结算比例估算"><span>卡内付款折合 USD{{inFlight.paymentUSD.missing&&inFlight.paymentUSD.known?'（部分）':''}}</span><strong>{{enabled&&inFlight.paymentUSD.estimated?'≈ ':''}}{{display(inFlight.paymentUSD.amount??(inFlight.paymentUSD.missing?'—':'0.00'))}} <em>USD</em></strong></div>
    <small v-if="enabled&&inFlight.paymentUSD.missing">{{display(inFlight.paymentUSD.missing)}} 笔缺少金额或 USD 折算依据，未计入</small>
    <small>扣除已清算成本后的待退金额<span v-if="inFlight.authorizations"> · 含授权占用</span></small>
   </div>
  </div>
  <EJHFinanceTotals collapsible :data="data" :masked="!enabled"/>
  <footer class="overview-footer"><details><summary>统计说明</summary><p>实际到账＝充值总额 × 99%；充值手续费为 1%。实际损耗＝实际到账 − 平台外转出 − 只付款支出 − EJH 账户余额 − 卡片余额折合 USD − 在途卡内付款折合 USD。它是资金差额，授权撤销费和开卡费不能再重复加上。消费收益来自下游用户消耗；只付款本金从清算成本分离并单列，已有退款冲减本金。净利润＝消费收益 − 开卡费 − 充值手续费 − 授权撤销费 − 清算成本 − 清算退款手续费，全部折算为 R。1 USD = 6.7 R，开卡费按 1 USDT = 1 USD 记账。单卡结余未分摊充值手续费，由总账统一扣除。</p><p>充值总账为手动填写的累计金额。平台外转出登记与本平台无关的累计消费，按 USD 从清算成本扣除，同时在资金核对单列转出，不重复计入成本；充值手续费仍按原充值总额计算。账户余额与卡内余额按最近查询结果分别展示；卡内余额、USD 折算和张数仅统计正常及销卡中的卡片，已销卡成功的历史余额不计入。统计不受分页与筛选影响。</p><p>非 USD 卡内余额和卡内付款按各卡初始开卡的 USD 结算金额 ÷ 原币额度折算，为历史比例估算，不代表当前汇率或实际销卡到账金额。缺少记录的卡片或付款不参与 USD 折算。</p><p>已付款在途统计账号回款中“已开通”和“已申请待回款”的付款流水。卡内付款为实际扣款或授权占用，不代表最终可回款金额；同笔付款只计一次；小额清算费用仍计成本；全额清算且已申请退款的本金计入在途，按已入账退款逐笔冲减，不计清算成本。卡内付款与结算按各自币种汇总。授权已有占用、尚无结算金额时显示待结算。未开通、已回款不计入；金额缺失不按零处理。统计不受搜索与分页影响。</p></details><details v-if="failures.length"><summary class="danger-text">{{display(failures.length)}} 项查询失败</summary><p v-for="(f,i) in failures" :key="i">{{f.label}}：{{f.error}}</p></details></footer>
 </section>
</template>

<style scoped>
.overview-actions{display:flex;gap:8px;flex-wrap:wrap}
.overview-disabled{margin:12px 20px;color:var(--muted);font-size:12px}.ejh-balance-overview{border:1px solid var(--line);border-radius:12px;background:var(--surface);overflow:hidden}.overview-header{display:flex;align-items:center;justify-content:space-between;gap:12px;padding:16px 20px;border-bottom:1px solid var(--line)}.overview-header>div{display:flex;align-items:center;gap:9px;color:var(--green-strong)}.overview-header h2{margin:0;font-size:15px;font-weight:600}.overview-header .btn{display:inline-flex;align-items:center;gap:6px}.overview-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr))}.overview-grid>div{padding:20px;min-width:0}.overview-grid>div+div{border-left:1px solid var(--line)}.overview-ledger{background:var(--green-bg)}.overview-label{display:flex;align-items:center;justify-content:space-between;gap:10px;font-size:12px;color:var(--muted);min-height:22px}.overview-label>span:last-child:not(:first-child){font-size:10px;white-space:nowrap}.ledger-edit{display:inline-flex;align-items:center;gap:4px;color:var(--green-strong);font-size:11px;white-space:nowrap}.ledger-edit:disabled{opacity:.5}.overview-values{display:flex;flex-wrap:wrap;gap:10px 20px;margin:14px 0 10px;font-variant-numeric:tabular-nums}.overview-values>div{display:flex;align-items:baseline;gap:6px;min-width:0}.overview-values strong{font-size:25px;font-weight:600;overflow-wrap:anywhere;line-height:1.3}.overview-values span{font-size:11px;color:var(--muted)}.overview-grid small,.ledger-form small{display:block;font-size:10px;color:var(--muted);line-height:1.6}.overview-placeholder{font-size:20px;color:var(--muted);margin:15px 0}.overview-usd{display:flex;align-items:baseline;justify-content:space-between;gap:12px;font-size:11px;margin:14px 0 7px;padding-top:10px;border-top:1px solid var(--line)}.overview-usd>span{color:var(--muted)}.overview-usd strong{font-size:16px;color:var(--green-strong);font-variant-numeric:tabular-nums}.overview-usd em{font-size:10px;font-style:normal;font-weight:400}.ledger-form{margin-top:12px}.ledger-input{display:flex;align-items:center;gap:8px;margin:0}.ledger-input input{min-width:0;width:100%;font-size:17px;padding:8px 10px}.ledger-input>span{font-size:11px;color:var(--muted)}.ledger-actions{display:flex;gap:8px;margin:10px 0 6px}.ledger-form p{font-size:11px;margin:8px 0}.overview-footer{display:flex;flex-wrap:wrap;gap:10px 24px;padding:10px 20px;border-top:1px solid var(--line);color:var(--muted);font-size:10px}.overview-footer details{max-width:100%}.overview-footer summary{cursor:pointer}.overview-footer p{font-size:11px;line-height:1.7;margin:8px 0;overflow-wrap:anywhere}.overview-grid .danger-text,.danger-text{color:var(--red)}.spin{animation:balance-spin 1s linear infinite}@keyframes balance-spin{to{transform:rotate(360deg)}}.inflight-amounts{display:grid;gap:12px;margin:16px 0 12px}.inflight-amounts>div{display:flex;align-items:baseline;justify-content:space-between;gap:10px}.inflight-amounts dt{font-size:11px;color:var(--muted);white-space:nowrap}.inflight-amounts dd{margin:0;text-align:right;min-width:0}.inflight-amounts strong{display:block;font-size:18px;font-weight:600;overflow-wrap:anywhere;font-variant-numeric:tabular-nums}.inflight-amounts em{font-size:11px;color:var(--muted);font-style:normal;font-weight:400}@media(max-width:1200px){.overview-grid{grid-template-columns:repeat(2,minmax(0,1fr))}.overview-grid>div:nth-child(3){border-left:0}.overview-grid>div:nth-child(n+3){border-top:1px solid var(--line)}}@media(max-width:640px){.overview-grid{grid-template-columns:minmax(0,1fr)}.overview-grid>div+div{border-left:0;border-top:1px solid var(--line)}.overview-grid>div{padding:16px 20px}.overview-values{margin:10px 0}.overview-header{flex-wrap:wrap}}
</style>
