<script setup>
import { usdToR } from '../ejh-profit'
defineProps({summary:{type:Object,required:true}})
</script>
<template>
 <div class="usage-cost"><span>实际使用消耗</span><div>
  <strong v-for="row in summary.rows" :key="row.currency">{{row.amount}} {{row.currency}}</strong>
  <strong v-if="summary.count||!summary.waiting&&!summary.pending">{{summary.usd.amount===null?'待核实':'≈ '+summary.usd.amount+' USD'}}</strong>
  <strong v-else>回款后计算</strong>
  <small v-if="summary.usd.amount!==null&&(summary.count||!summary.waiting&&!summary.pending)">折合 ≈ {{usdToR(summary.usd.amount)}} R</small>
  <small>确认回款后计算 · USD 按开卡比例估算</small>
  <small v-if="summary.coveredCount">已计清算的部分不重复扣除</small>
  <small v-if="summary.pending">{{summary.pending}} 笔回款金额待确认，尚未计入</small>
  <small v-if="summary.missing">{{summary.missing}} 笔金额或折算依据待核实</small>
 </div></div>
</template>
<style scoped>
.usage-cost{display:flex;justify-content:space-between;align-items:baseline;gap:12px;margin:9px 0 4px;padding-top:8px;border-top:1px solid var(--line);font-size:11px}.usage-cost>span{color:var(--muted);flex-shrink:0}.usage-cost>div{text-align:right;min-width:0;font-variant-numeric:tabular-nums}.usage-cost strong{display:block;font-size:12px;overflow-wrap:anywhere}.usage-cost small{display:block;font-size:10px;color:var(--muted);line-height:1.6}
</style>
