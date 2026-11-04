<script setup>
import { computed } from 'vue'
import { paymentOnlySummary, paymentMoney } from '../payment-only'
import { formatTime } from '../utils'
const props=defineProps({data:Object,issuer:String,masked:Boolean,compact:Boolean})
const summary=computed(()=>(props.issuer==='ejh'&&props.data._overview?.paymentOnly)||paymentOnlySummary(props.data,props.issuer))
const value=n=>props.masked?'---':paymentMoney(n)
</script>
<template><section class="payment-only-summary" :class="{compact}"><details><summary><span>只付款支出 · 累计</span><strong>{{masked?'---':summary.missing?'待核实':value(summary.amount)}} <small>USD</small></strong><small>{{summary.rows.length}} 张卡 · {{summary.estimated?'含历史比例折算 · ':''}}点击查看明细</small></summary><p>单列付款本金，不计退款业务清算成本；实际退款会冲减支出，开卡费等费用仍保留。</p><div v-if="!masked" class="payment-only-rows"><article v-for="r in summary.rows" :key="r.card_id"><b>卡片 {{r.last4||r.card_id}}</b><span>{{r.accounts.join('、')||'无关联账号'}}</span><span>付款 {{value(r.paid)}} − 回款 {{value(r.returned)}} = {{r.missing?'待核实':value(r.amount)}} USD</span><small>{{r.manual?'手动核实本金':'按已有付款记录'}} · {{formatTime(r.at)}} · {{r.note}}</small></article><p v-if="!summary.rows.length">尚未设置只付款卡片。</p></div></details></section></template>
<style scoped>
.payment-only-summary{border-top:1px solid var(--line,#ddd);margin-top:14px;padding:14px 0}.payment-only-summary summary{cursor:pointer;display:grid;gap:8px}.payment-only-summary summary>span{font-size:12px}.payment-only-summary strong{font-size:23px;color:var(--text)}.payment-only-summary small,.payment-only-summary p{font-size:11px;color:var(--muted);line-height:1.7}.payment-only-rows article{display:grid;gap:6px;padding:12px 0;border-top:1px solid var(--line);font-size:12px;overflow-wrap:anywhere}
</style>

<style scoped>
.payment-only-summary.compact{margin:0;padding:18px 22px;border:0;min-width:0;overflow-wrap:anywhere}.compact summary{gap:9px}.compact summary>span{color:var(--muted)}.compact strong{font-size:24px;font-variant-numeric:tabular-nums;line-height:1.25}.compact strong small{font-weight:400}.compact details>p{margin:12px 0}.compact .payment-only-rows{max-height:280px;overflow:auto}.compact summary::-webkit-details-marker{display:none}.compact summary:focus-visible{outline:2px solid var(--green-strong);outline-offset:4px}@media(max-width:700px){.payment-only-summary.compact{padding:16px 12px}.compact strong{font-size:22px}}
</style>
