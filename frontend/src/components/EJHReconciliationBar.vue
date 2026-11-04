<script setup>
import { computed } from 'vue'
import { reconciliationStatus } from '../ejh-reconciliation-view'
import { usdToR } from '../ejh-profit'
const props=defineProps({data:{type:Object,required:true},masked:Boolean})
const display=value=>props.masked?'---':value
const report=computed(()=>props.data._overview?.reconciliation || reconciliationStatus(props.data))
const value=computed(()=>report.value.value)
const state=computed(()=>props.masked?'unknown':report.value.state)
</script>
<template>
 <section class="reconcile-bar" :class="state" aria-label="资金核对">
  <div class="reconcile-status"><span class="status-dot"></span><div><strong>资金核对 · {{masked?'已关闭':report.label}}</strong><small>按最近查询余额 · 含估算 · ≤ 1 USD 为小额差异</small></div></div>
  <div class="reconcile-values"><div><span>账面差额</span><b>{{display(value.gap??'—')}} <em>USD</em></b><small>{{display(usdToR(value.gap)??'—')}} R</small></div><div><span>其中未销卡待补回</span><b>{{display(value.closurePending??'—')}} <em>USD</em></b><small>不计成本</small></div><div><span>其余待核对</span><b>{{display(value.unexplained??'—')}} <em>USD</em></b><small>未认定为损失</small></div></div>
  <details v-if="!masked&&value.gap!==null"><summary>核对明细与计算方式</summary><p>充值 <b>{{display(value.gross)}}</b> − 余额及在途 <b>{{display(value.assets)}}</b> − 总成本 <b>{{display(value.earnings.costUSD)}}</b><template v-if="value.externalTransfer!=='0.00'"> − 平台外转出 <b>{{display(value.externalTransfer??'—')}}</b></template><template v-if="value.paymentOnly.amount"> − 只付款支出 <b>{{(value.paymentOnly.amount/100).toFixed(2)}}</b></template> ＝ 差额 <b>{{display(value.gap)}}</b> USD</p></details>
  <p v-else-if="!masked">{{display(value.lossReasons.join('；')||'部分费用待核实，暂不能核对总账。')}}</p>
 </section>
</template>
<style scoped>
.reconcile-bar{margin:16px 20px 0;padding:16px 18px;border:1px solid #e8c589;border-radius:9px;background:#fff9eb;color:#795416;display:flex;align-items:center;justify-content:space-between;gap:14px 28px;flex-wrap:wrap;font-variant-numeric:tabular-nums}.reconcile-status{display:flex;gap:10px;align-items:center}.reconcile-status strong{font-size:14px}.status-dot{width:8px;height:8px;border-radius:50%;background:#c28a27;flex:none}.reconcile-bar small{display:block;font-size:10px;line-height:1.7;opacity:.85}.reconcile-values{display:flex;gap:34px}.reconcile-values span{font-size:10px;display:block}.reconcile-values b{font-size:22px;display:block;margin:4px 0}.reconcile-values em{font-size:10px;font-style:normal;font-weight:400}.reconcile-bar p{flex-basis:100%;margin:0;border-top:1px solid #e8c58988;padding-top:10px;font-size:11px;line-height:1.7}.reconcile-bar.balanced{color:var(--green-strong);background:var(--green-bg);border-color:var(--line)}.balanced .status-dot{background:var(--green-strong)}.unknown{color:var(--muted);background:var(--bg);border-color:var(--line)}@media(max-width:640px){.reconcile-bar{margin:12px 12px 0;padding:14px}.reconcile-values{width:100%;gap:12px;justify-content:space-between}.reconcile-values b{font-size:18px}}
</style>
<style scoped>
.reconcile-bar.small{color:var(--green-strong);background:var(--green-bg);border-color:var(--line)}.small .status-dot{background:var(--green-strong)}.reconcile-bar.stale{color:var(--muted);background:var(--bg);border-color:var(--line)}
</style>

<style scoped>
.reconcile-bar{padding:12px 16px;gap:10px 24px}.reconcile-values b{font-size:20px;margin:3px 0}.reconcile-bar details{flex-basis:100%;font-size:11px}.reconcile-bar summary{cursor:pointer;width:fit-content}.reconcile-bar details p{margin-top:10px}
</style>
