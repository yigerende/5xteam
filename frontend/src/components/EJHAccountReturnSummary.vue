<script setup>
import ProOAICaseFlow from './ProOAICaseFlow.vue'
import { paymentOnlySummary, paymentMoney } from '../payment-only'
import { computed } from 'vue'
import { accountAmounts } from '../ejh-account-amounts'
import { accountConsumptionText, accountConsumptionForProfit } from '../ejh-consumption'
import { formatTime } from '../utils'
import EJHCaseState from './EJHCaseState.vue'
import { casePaymentTrades, summarizeClearingCosts } from '../ejh-clearing'
import EJHClearingCost from './EJHClearingCost.vue'
import EJHReversalFee from './EJHReversalFee.vue'

const props = defineProps({ row: { type: Object, required: true }, cards: { type: Array, default: () => [] }, operations: { type: Array, default: () => [] }, trades: { type: Array, default: () => [] }, feePercent: { type: Number, default: 6 } })
defineEmits(['updated'])
const amounts = computed(() => accountAmounts(props.row,props.trades))
const clearing = computed(() => {
 const raw=summarizeClearingCosts(casePaymentTrades(props.row,props.trades),props.cards,props.operations,[props.row])
 if(!props.row.payment_only)return raw
 const id=props.row.ejh_card_id||props.row.card_id
 const summary=paymentOnlySummary({payment_policies:[{card_id:id,mode:'payment_only'}],cards:props.cards,cases:[props.row],operations:props.operations,trades:casePaymentTrades(props.row,props.trades)},'ejh')
 return {...raw,usd:{...raw.usd,amount:raw.usd.amount===null?null:paymentMoney(Math.round(Number(raw.usd.amount)*100)-summary.costExcluded)},missing:raw.missing+summary.missing}
})
</script>

<template>
 <article class="account-return-summary">
  <header><strong>{{row.email}}</strong><EJHCaseState :row="row"/></header>
  <dl class="return-amounts">
   <div><dt>付款<span v-if="amounts.authorization">（授权）</span></dt><dd>{{amounts.paid===null?'待识别':amounts.paid+' '+amounts.currency}}</dd></div>
   <div><dt>回款</dt><dd v-for="r in amounts.returned" :key="r.currency">{{r.amount}} {{r.currency}}</dd><dd v-if="!amounts.returned.length">{{amounts.invalid||amounts.pending?'待核实':'0.00'+(amounts.currency?' '+amounts.currency:'')}}</dd></div>
   <div><dt>差额</dt><dd>{{amounts.difference===null?'—':amounts.difference+' '+amounts.currency}}</dd></div>
   <div class="consumption-total" title="Pro 管理中的用户消耗统计，按账号显示"><dt>消费收益</dt><dd>{{accountConsumptionText(row)}}</dd></div>
  </dl>
  <details class="fee-details"><summary>费用与回款明细</summary>
  <p v-if="['opened','waiting'].includes(row.state)" class="return-note">尚待回款：{{amounts.remaining??'待核实'}} {{amounts.currency}}（已扣清算）</p><EJHClearingCost :summary="clearing"/><EJHReversalFee :merchant-fee-u-s-d="row.merchant_fee_usd||'0.00'" :clearing="clearing" show-profit :consumption="accountConsumptionForProfit(row)" :records="row.returns||[]" :fee-percent="feePercent" :cards="cards" :operations="operations" confirmed-only/>
  <p v-if="amounts.pending||amounts.invalid" class="return-note">{{amounts.pending?amounts.pending+' 笔回款待核实，未计入金额':'回款金额待核实'}}</p>
  <p v-else-if="amounts.paid!==null&&amounts.reason" class="return-note">{{amounts.reason}}</p>
  <p v-if="row.state_override?.state==='returned'&&!amounts.returned.length" class="return-note">人工标记已回款，尚无已确认金额</p>
  </details>
  <ProOAICaseFlow :row="row" @updated="$emit('updated')"/><footer><span>开通时间 {{row.opened_at?formatTime(row.opened_at):'—'}}</span><span>申请时间 {{row.requested_at?formatTime(row.requested_at):'—'}}</span><span title="优先取卡台记账时间；无有效记账时间时取交易时间">回款时间 {{amounts.returnedAt?formatTime(amounts.returnedAt):'—'}}</span></footer>
 </article>
</template>

<style scoped>
.fee-details{margin:10px 0}.fee-details>summary{cursor:pointer;color:var(--green-strong);font-size:12px}
.account-return-summary{padding:14px 0;border-bottom:1px solid var(--line);min-width:0}.account-return-summary:last-child{border-bottom:0}.account-return-summary header{display:flex;align-items:center;justify-content:space-between;flex-wrap:wrap;gap:10px}.account-return-summary header>strong{font-size:13px;overflow-wrap:anywhere;min-width:0}.return-amounts{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:12px;margin:14px 0 10px}.return-amounts dt{color:var(--muted);font-size:11px;line-height:1.7}.return-amounts dd{margin:6px 0 0;font-size:15px;font-weight:650;overflow-wrap:anywhere;line-height:1.5;font-variant-numeric:tabular-nums}.return-note{margin:6px 0;color:var(--muted);font-size:11px}.account-return-summary footer{display:flex;flex-wrap:wrap;gap:5px 20px;color:var(--muted);font-size:11px;line-height:1.7}@media(max-width:480px){.return-amounts{grid-template-columns:repeat(2,minmax(0,1fr));gap:9px}.return-amounts dd{font-size:13px}.account-return-summary footer{flex-direction:column}}
</style>
