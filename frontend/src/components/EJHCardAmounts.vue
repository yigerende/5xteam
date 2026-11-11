<script setup>
import { ejhDeadLoss } from '../dead-account-loss'
import { paymentOnlySummary, paymentMoney } from '../payment-only'
import { computed } from 'vue'
import { cardAmounts, summarizeInFlightPayments } from '../ejh-account-amounts'
import { cardConsumption } from '../ejh-consumption'
import { summarizeRefundFees } from '../ejh-refund-cost'
import { summarizeReversalFees } from '../ejh-reversal-fees'
import { cents, money } from '../ejh-finance'
import { financeProfit } from '../ejh-profit'
import { summarizeClearingCosts } from '../ejh-clearing'
import EJHClearingCost from './EJHClearingCost.vue'
import EJHReversalFee from './EJHReversalFee.vue'
const props=defineProps({card:{type:Object,required:true},trades:{type:Array,default:()=>[]},cases:{type:Array,default:()=>[]},operations:{type:Array,default:()=>[]},feePercent:{type:Number,default:6}})
const amounts=computed(()=>cardAmounts(props.card,props.trades))
const consumption=computed(()=>cardConsumption(props.cases))
const reversalTrades=computed(()=>props.trades.filter(t=>t.card_id===props.card.id))
const fee=computed(()=>summarizeReversalFees(reversalTrades.value,props.feePercent,false,[props.card],props.operations))
const refund=computed(()=>summarizeRefundFees(reversalTrades.value,props.feePercent,false,[props.card],props.operations))
const clearing=computed(()=>summarizeClearingCosts(reversalTrades.value,[props.card],props.operations,props.cases))
const paymentOnly=computed(()=>paymentOnlySummary({payment_policies:props.card.payment_policy?[props.card.payment_policy]:[],cards:[props.card],cases:props.cases,trades:props.trades,operations:props.operations},'ejh'))
const inFlight=computed(()=>summarizeInFlightPayments(props.cases,[props.card],props.operations,props.trades))
const merchantFee=computed(()=>money(props.cases.reduce((sum,row)=>sum+(cents(row.merchant_fee_usd||'0')||0n),0n)))
const dead=computed(()=>ejhDeadLoss({cards:[props.card],cases:props.cases,operations:props.operations,trades:props.trades}))
const earnings=computed(()=>financeProfit({openingUSD:props.card.issuer_linked!==false&&props.card.remote?.orderNo?'0.50':'0.00',reversalUSD:fee.value.usd.amount??(!fee.value.count?'0.00':null),rechargeUSD:'0.00',clearingUSD:clearing.value.usd.amount===null?null:paymentMoney(Math.round(Number(clearing.value.usd.amount)*100)-paymentOnly.value.costExcluded-dead.value.alreadyCost),
 deadUSD:paymentMoney(dead.value.amount),refundUSD:refund.value.usd.amount??'0.00',merchantUSD:merchantFee.value,consumption:consumption.value.total,consumptionMissing:consumption.value.missing,costMissing:dead.value.missing+refund.value.missing+refund.value.pending+paymentOnly.value.missing+fee.value.missing+fee.value.pending+fee.value.usd.missing+clearing.value.missing+clearing.value.pending,estimated:!!fee.value.count||clearing.value.estimated||refund.value.estimated}))
</script>
<template>
 <div class="card-money">
  <div class="money-fee"><span>消费收益</span><strong>{{earnings.consumptionR??'—'}} R</strong></div>
  <div class="money-fee"><span>卡片成本</span><strong>{{earnings.costR??'—'}} R</strong></div>
  <small>开卡 {{earnings.openingR}} R + 撤销 {{earnings.reversalR??'—'}} R + 退款手续费 {{earnings.refundR??'—'}} R + 清算 {{earnings.clearingR??'—'}} R</small><small>{{earnings.costUSD??'—'}} USD</small>
  <div v-if="dead.rows.length" class="money-fee"><span>死号亏损</span><strong>{{earnings.deadR}} R<small>{{paymentMoney(dead.amount)}} USD</small></strong></div><div class="money-fee"><span>API 收费</span><strong>{{earnings.merchantR}} R<small>{{merchantFee}} USD</small></strong></div>
  <div class="money-fee card-profit"><span>卡片结余{{earnings.estimated?'（估算）':''}}</span><strong :class="{negative:earnings.profitR?.startsWith('-')}">{{earnings.profitR??'—'}} R</strong></div>
  <small>充值手续费在总账扣除</small><small v-if="card.payment_policy?.mode==='payment_only'">只付款本金 {{paymentOnly.missing?'待核实':paymentMoney(paymentOnly.amount)+' USD'}} · 单列支出</small>
  <small v-if="consumption.notPushed">{{consumption.notPushed}} 个账号未推送，无消费数据</small>
  <small v-if="earnings.incomplete" class="warning">{{consumption.missing?'消费未查询完整':'部分费用待核实'}}，暂不计算结余</small>
  <details class="money-details"><summary>付款、回款与费用明细</summary>
   <div class="money-head"><span>付款</span><span>回款</span><span>差额</span></div>
   <div v-for="r in amounts.rows" :key="r.currency" class="money-row"><strong>{{r.paid}}<small>{{r.currency}}{{r.authorization?' · 含授权':''}}</small></strong><strong>{{r.returned}}<small>{{r.currency}}</small></strong><strong>{{r.difference??'—'}}<small>{{r.currency}}</small></strong></div>
   <p v-if="!amounts.rows.length">暂无可识别交易</p>
   <div class="money-fee"><span>开卡费</span><strong>{{earnings.openingR}} R<small>{{earnings.openingR==='0.0000'?'0.00':'0.50'}} USDT · 按 1:1 USD</small></strong></div>
   <div class="money-fee"><span>尚待回款</span><strong>{{inFlight.payment.map(r=>r.amount+' '+r.currency).join(' / ')||(inFlight.missingPayment?'待核实':'0.00')}}<small>{{inFlight.paymentUSD.amount??(inFlight.paymentUSD.missing?'—':'0.00')}} USD · 已扣清算</small></strong></div>
   <EJHClearingCost :summary="clearing"/>
   <EJHReversalFee :records="reversalTrades" :fee-percent="feePercent" :cards="[card]" :operations="operations"/>
   <small v-if="amounts.invalid">部分金额或币种缺失，差额暂不可计算</small><small v-if="amounts.pending">{{amounts.pending}} 笔回款待入账，未计入</small><small>差额＝付款 − 回款；销卡退余额不计入。1 USD = 6.7 R。</small>
  </details>
 </div>
</template>
<style scoped>
.card-money{margin-top:10px;min-width:210px;font-variant-numeric:tabular-nums}.money-head,.money-row{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:8px}.money-head{font-size:11px;color:var(--muted);margin:10px 0 5px}.money-row{margin:5px 0}.money-row strong{font-size:12px;overflow-wrap:anywhere}.card-money small{display:block;font-size:10px;line-height:1.6;color:var(--muted);font-weight:400}.card-money p{font-size:11px;color:var(--muted)}.money-fee{display:flex;justify-content:space-between;gap:8px;font-size:11px;margin:8px 0 4px;padding-top:7px;border-top:1px solid var(--line)}.money-fee:first-child{border:0;padding-top:0}.money-fee strong{text-align:right;overflow-wrap:anywhere}.card-profit strong{color:var(--green-strong);font-size:14px}.card-money .negative,.card-money .warning{color:var(--red)}.money-details{margin-top:9px}.money-details summary{font-size:10px;color:var(--muted);cursor:pointer}
</style>
