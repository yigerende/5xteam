<script setup>
import { ref, onMounted, watch, onBeforeUnmount } from 'vue'
import { api } from '../api'
import { formatTime } from '../utils'
import Pagination from './Pagination.vue'
const props=defineProps({revision:{type:Number,default:0}})
const rows=ref([]),total=ref(0),summary=ref('0.00'),page=ref(1),size=ref(10),loading=ref(false),error=ref('')
let generation=0
async function load(){const current=++generation;loading.value=true;error.value='';try{const result=await api(`/api/ejh/adjustments?page=${page.value}&page_size=${size.value}`);if(current!==generation)return;rows.value=result.items;total.value=result.total;summary.value=result.summary.amount}catch(e){if(current===generation)error.value=e.message}finally{if(current===generation)loading.value=false}}
const signed=v=>Number(v)>0?'+'+v:v
const fields=[['net','实际到账'],['assets','余额及在途'],['cost','总成本'],['refund_fees','清算退款手续费'],['merchant_fee','外部 API 收费'],['external_transfer','平台外转出'],['payment_only','只付款支出'],['account','EJH 账户余额'],['card_balance','卡余额折合'],['in_flight','付款在途'],['refund_settlement','退款待销卡'],['usage','其中使用消耗（已含在成本）']]
watch([page,size,()=>props.revision],load);onMounted(load);onBeforeUnmount(()=>generation++)
</script>
<template>
 <section class="panel adjustment-list" aria-label="手动平账列表">
  <header><div><h2>手动平账列表</h2><p>累计平账 <strong>{{signed(summary)}} USD</strong> · {{total}} 条。仅调整核对差额，原始资金及经营数据保留。</p></div><button class="btn ghost" :disabled="loading" @click="load">{{loading?'读取中…':'刷新列表'}}</button></header>
  <p v-if="error" class="danger-text" role="alert">{{error}}</p>
  <p v-if="!rows.length&&!loading&&!error" class="empty">暂无手动平账记录。可在余额总览点击“一键平账”。</p>
  <div v-if="rows.length" class="table-shell"><table><thead><tr><th>操作时间 / 人员</th><th>平账前差额 USD</th><th>本次平账 USD</th><th>平账后差额 USD</th><th>原因与依据</th></tr></thead><tbody><tr v-for="row in rows" :key="row.id"><td>{{formatTime(row.at)}}<small>{{row.actor}} · #{{Number(row.id)}}</small></td><td>{{row.gap_before}}</td><td class="amount">{{signed(row.amount)}}</td><td>{{row.gap_after}}</td><td><p class="note">{{row.note}}</p><details><summary>查看完整记录</summary><dl><div><dt>记录编号</dt><dd>{{row.id}}</dd></div><div><dt>请求编号</dt><dd>{{row.request_id}}</dd></div><div><dt>原始差额</dt><dd>{{row.raw_gap}}</dd></div><div><dt>累计平账（前 → 后）</dt><dd>{{row.total_before}} → {{row.total_after}} USD</dd></div><div><dt>核对计算时间</dt><dd>{{formatTime(row.calculated_at)}}</dd></div><div><dt>主余额查询 / 交易同步</dt><dd>{{formatTime(row.snapshot.balance_at)}} / {{formatTime(row.snapshot.sync_at)}}</dd></div><div v-for="[key,label] in fields" :key="key"><dt>{{label}}</dt><dd>{{row.snapshot[key]??'待核实'}} USD</dd></div><div><dt>数据版本 / 公式</dt><dd>{{row.version}} / {{row.snapshot.formula}}</dd></div></dl><small>原始差额 + 此前累计平账 + 本次平账 = 平账后差额。金额包含历史汇率估算，平账不证明卡商实际扣费。</small><details><summary>卡片余额查询时间及原始依据</summary><pre>{{JSON.stringify(row.snapshot,null,2)}}</pre></details></details></td></tr></tbody></table></div>
  <Pagination :disabled="loading" :page="page" :page-size="size" :total="total" @update:page="page=$event" @update:page-size="size=Math.min($event,100);page=1"/>
 </section>
</template>
<style scoped>
.adjustment-list header{display:flex;justify-content:space-between;gap:16px;align-items:flex-start}.adjustment-list h2{font-size:17px;margin:0 0 8px}.adjustment-list header p,.adjustment-list small{color:var(--muted);font-size:12px;line-height:1.7}.adjustment-list td{vertical-align:top;font-variant-numeric:tabular-nums}.adjustment-list td small{display:block}.adjustment-list th{white-space:nowrap}.adjustment-list summary{cursor:pointer;color:var(--green-strong);font-size:12px}.adjustment-list .amount{font-weight:600;color:var(--green-strong)}.adjustment-list .note{white-space:pre-wrap;overflow-wrap:anywhere;max-width:520px;margin:0 0 8px}.adjustment-list dl{min-width:260px;font-size:12px}.adjustment-list dl>div{display:flex;gap:12px;justify-content:space-between;padding:5px 0;border-bottom:1px solid var(--line)}.adjustment-list dd{margin:0;overflow-wrap:anywhere;text-align:right;max-width:340px}.adjustment-list pre{max-width:650px;max-height:300px;overflow:auto;font-size:11px}.empty{padding:28px 0;color:var(--muted);text-align:center}@media(max-width:640px){.adjustment-list header{flex-wrap:wrap}.adjustment-list pre{max-width:280px}}
</style>
