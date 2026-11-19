<script setup>
import { computed } from 'vue'
import { accountProfit } from '../ejh-account-profit'
import { summarizeReversalFees } from '../ejh-reversal-fees'
import { summarizeRefundFees } from '../ejh-refund-cost'
import { usdToR } from '../ejh-profit'
const props = defineProps({ deadFeeUSD:{type:String,default:'0.00'}, merchantFeeUSD:{type:String,default:'0.00'}, clearing: {type:Object,default:null}, records: { type: Array, default: () => [] }, feePercent: { type: Number, default: 6 }, cards: { type: Array, default: () => [] }, operations: { type: Array, default: () => [] }, confirmedOnly: Boolean, hideLabel: Boolean, showProfit: Boolean, consumption: {type:Number,default:null}, consumptionMissing: {type:Number,default:0} })
const fee = computed(() => summarizeReversalFees(props.records, props.feePercent, props.confirmedOnly, props.cards, props.operations))
const refund = computed(() => summarizeRefundFees(props.records, props.feePercent, props.confirmedOnly, props.cards, props.operations))
const result = computed(() => accountProfit({...props,fee:fee.value,refund:refund.value}))
</script>

<template>
 <div v-if="fee.count||fee.pending||fee.missing||!(refund.count||refund.pending||refund.missing)" class="reversal-fee" :class="{'reversal-fee-cell':hideLabel}">
  <span v-if="!hideLabel">授权撤销费</span>
  <div class="reversal-fee-values">
   <strong v-for="row in fee.rows" :key="row.currency">{{row.amount}} {{row.currency}}</strong>
   <strong v-if="!fee.rows.length">{{fee.missing||fee.pending?'待核实':'—'}}</strong>
   <strong v-if="fee.usd.amount!==null && (fee.usd.estimated || fee.rows.some(row=>row.currency!=='USD'))" class="reversal-fee-usd" title="按各卡初始开卡的 USD 结算比例估算">{{fee.usd.estimated?'≈ ':''}}{{fee.usd.amount}} USD{{fee.usd.missing?'（部分）':''}}</strong>
   <strong v-if="showProfit || fee.count || fee.missing || fee.pending" class="reversal-fee-r" title="授权撤销费 USD × 6.7">折合 {{result.estimated?'≈ ':''}}{{result.amount??'—'}} R{{result.incomplete&&result.amount!==null?'（部分）':''}}</strong>
   <small v-if="fee.usd.missing">{{fee.usd.missing}} 笔缺少 USD 折算依据，未计入</small>
   <small v-if="fee.count">{{fee.percent===null?'按各笔费率分别计算':'按 '+fee.percent+'% 计算'}}{{records.some(r=>r.reversal_fee)?' · 已回款费率已锁定':''}}</small>
   <small v-if="fee.missing">{{fee.missing}} 笔金额或比例待核实</small>
   <small v-if="fee.pending">{{fee.pending}} 笔待确认，未计入</small>
  </div>
 </div>
 <div v-if="refund.count||refund.pending||refund.missing" class="reversal-fee" :class="{'reversal-fee-cell':hideLabel}"><span v-if="!hideLabel">退款手续费</span><div class="reversal-fee-values"><strong>{{refund.usd.amount??'待核实'}}{{refund.usd.amount!==null?' USD':''}}</strong><small v-if="refund.usd.amount!==null">折合 {{usdToR(refund.usd.amount)}} R</small><small v-if="refund.entries.some(e=>e.manual)">按已关联登记费用{{refund.entries.some(e=>e.allocated)?'分摊':''}}</small><small v-if="refund.entries.some(e=>!e.manual)">{{refund.percent===null?'按各笔费率计算':'按 '+refund.percent+'% 计算'}}{{records.some(r=>r.reversal_fee)?' · 已回款费率已锁定':''}}</small><small v-if="refund.missing||refund.pending">{{refund.missing+refund.pending}} 笔待核实或待入账，未计入</small></div></div>
 <div v-if="deadFeeUSD===null||Number(deadFeeUSD)>0" class="reversal-fee"><span>死号亏损</span><strong>{{deadFeeUSD??'待核实'}} USD</strong></div><div v-if="showProfit" class="reversal-fee"><span>API 收费</span><strong>{{merchantFeeUSD}} USD</strong></div>
 <div v-if="showProfit" class="reversal-profit" title="账号结余＝消费收益 R − 撤销费 R − 清算成本 R − API 收费 R − 死号亏损 R；未分摊开卡费，充值与退款手续费仅展示">
  <span>账号结余</span><div><strong :class="{'negative':result.profit?.startsWith('-')}">{{result.profit!==null&&result.estimated?'≈ ':''}}{{result.profit??'—'}} R</strong><small>未分摊开卡费、充值手续费</small><small v-if="result.profit===null">{{consumptionMissing||consumption===null?'消费未查询完整':'费用待核实'}}</small></div>
 </div>
</template>

<style scoped>
.reversal-fee{display:flex;justify-content:space-between;align-items:baseline;gap:12px;margin:9px 0 4px;padding-top:8px;border-top:1px solid var(--line);font-size:11px}.reversal-fee>span{color:var(--muted);flex-shrink:0}.reversal-fee-values{text-align:right;min-width:0;font-variant-numeric:tabular-nums}.reversal-fee strong{display:block;font-size:12px;overflow-wrap:anywhere}.reversal-fee small{display:block;font-size:10px;color:var(--muted);line-height:1.6}.reversal-fee-cell{display:block;border:0;margin:0;padding:0}.reversal-fee-cell .reversal-fee-values{text-align:left}
.reversal-profit{display:flex;align-items:baseline;justify-content:space-between;gap:12px;margin-top:8px;padding-top:8px;border-top:1px solid var(--line);font-size:11px}.reversal-profit>span{color:var(--muted)}.reversal-profit>div{text-align:right;min-width:0}.reversal-profit strong{font-size:12px;color:var(--green-strong);font-variant-numeric:tabular-nums;overflow-wrap:anywhere}.reversal-profit .negative{color:var(--red)}.reversal-profit small{display:block;font-size:10px;color:var(--muted)}.reversal-fee-r{margin-top:3px}
</style>
