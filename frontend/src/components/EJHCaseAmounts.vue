<script setup>
import { ejhDeadLoss } from '../dead-account-loss'
import { paymentOnlySummary, paymentMoney } from '../payment-only'
import { computed } from 'vue'
import { accountAmounts } from '../ejh-account-amounts'
import { formatTime } from '../utils'
import { accountConsumptionText, accountConsumptionForProfit } from '../ejh-consumption'
import { tradeKinds } from '../ejh'
import ActivationCost from './ActivationCost.vue'
import ReturnStatusBadge from './ReturnStatusBadge.vue'
import { casePaymentTrades, summarizeClearingCosts } from '../ejh-clearing'
import EJHClearingCost from './EJHClearingCost.vue'
import EJHReversalFee from './EJHReversalFee.vue'
const props=defineProps({row:{type:Object,required:true},cards:{type:Array,default:()=>[]},operations:{type:Array,default:()=>[]},trades:{type:Array,default:()=>[]},feePercent:{type:Number,default:6}})
const dead=computed(()=>ejhDeadLoss({cases:[props.row],cards:props.cards,operations:props.operations,trades:props.trades}))
const amounts=computed(()=>accountAmounts(props.row,props.trades))
const clearing = computed(() => {
 const raw=summarizeClearingCosts(casePaymentTrades(props.row,props.trades),props.cards,props.operations,[props.row])
 if(props.row.pro_dead)return {...raw,usd:{...raw.usd,amount:raw.usd.amount==null?null:paymentMoney(Math.round(Number(raw.usd.amount)*100)-dead.value.alreadyCost)}}
 if(!props.row.payment_only)return raw
 const id=props.row.ejh_card_id||props.row.card_id
 const summary=paymentOnlySummary({payment_policies:[{card_id:id,mode:'payment_only'}],cards:props.cards,cases:[props.row],operations:props.operations,trades:casePaymentTrades(props.row,props.trades)},'ejh')
 return {...raw,usd:{...raw.usd,amount:raw.usd.amount===null?null:paymentMoney(Math.round(Number(raw.usd.amount)*100)-summary.costExcluded)},missing:raw.missing+summary.missing}
})
</script>
<template><div class="account-money"><div class="account-money-grid">
<div><small>付款{{amounts.authorization?'（授权占用）':''}}</small><strong>{{amounts.paid===null?'待核实':amounts.paid+' '+amounts.currency}}</strong><small v-if="amounts.paid!==null&&!amounts.posted">待入账</small></div>
<div><small>已确认回款</small><strong v-for="r in amounts.returned" :key="r.currency">{{r.amount}} {{r.currency}}</strong><strong v-if="!amounts.returned.length">{{amounts.invalid?'待核实':'0.00'+(amounts.currency?' '+amounts.currency:'')}}</strong><small v-if="amounts.returnedAt" title="优先取卡台记账时间；无有效记账时间时取交易时间">回款时间 {{formatTime(amounts.returnedAt)}}</small><small v-if="amounts.pending">{{amounts.pending}} 笔待核实，未计入</small><small v-else-if="!amounts.returned.length&&!amounts.invalid">{{row.state_override?.state==='returned'?'人工标记已回款；尚无已确认金额流水':'暂无已确认回款'}}</small></div>
<div><small>差额（付款 − 回款）</small><strong>{{amounts.difference===null?'—':amounts.difference+' '+amounts.currency}}</strong><small>{{amounts.reason||(amounts.authorization?'按授权占用计算':'按刷卡金额计算')}}</small></div>
<div class="consumption-total" title="Pro 管理中的用户消耗统计，按账号显示"><small>消费收益</small><strong>{{accountConsumptionText(row)}}</strong></div>
</div><details class="fee-details"><summary>费用与回款明细</summary><p v-if="!row.pro_dead&&['opened','waiting'].includes(row.state)" class="return-note">尚待回款：{{amounts.remaining??'待核实'}} {{amounts.currency}}（已扣清算）</p><EJHClearingCost :summary="clearing"/><EJHReversalFee :dead-fee-u-s-d="dead.missing?null:paymentMoney(dead.amount)" :merchant-fee-u-s-d="row.merchant_fee_usd||'0.00'" :clearing="clearing" show-profit :consumption="accountConsumptionForProfit(row)" :records="row.returns||[]" :fee-percent="feePercent" :cards="cards" :operations="operations" confirmed-only/><details><summary>查看流水与匹配依据</summary><ActivationCost :row="row" details/><div v-for="t in row.returns||[]" :key="t.id"><span>{{tradeKinds[t.trade.tradeType]}}</span> · <ReturnStatusBadge :state="t.confirmed?'returned':'unverified'"/><p>{{t.confirmed_amount||'金额待核实'}} {{t.confirmed_currency}} · {{t.evidence}}</p></div></details></details></div></template>
<style scoped>.account-money{min-width:440px;max-width:760px}.account-money-grid{display:grid;grid-template-columns:repeat(4,minmax(0,1fr));gap:16px}.account-money-grid small,.account-money-grid strong{display:block;line-height:1.65}.account-money-grid small{font-size:11px;color:var(--muted)}.account-money-grid strong{font-size:14px;overflow-wrap:anywhere}.account-money details{margin-top:12px}.account-money summary{cursor:pointer;color:var(--green-strong);font-size:12px}.account-money p{font-size:12px}</style>
