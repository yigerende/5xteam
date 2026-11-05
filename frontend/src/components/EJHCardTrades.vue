<script setup>
import { onBeforeUnmount, reactive, ref } from 'vue'
import { api } from '../api'
import { tradeKinds } from '../ejh'
import { formatTime } from '../utils'
import EJHReversalFee from './EJHReversalFee.vue'
const props=defineProps({savedTrades:{type:Array,default:()=>[]},defaultPageSize:{type:Number,default:10},operations:{type:Array,default:()=>[]},feePercent:{type:Number,default:6}})
const opened=ref(false),busy=ref(false),error=ref(''),card=ref(null),rows=ref([]),page=ref(1),hasMore=ref(false),queriedAt=ref(''),applied=ref(null),cached=ref(false)
const pageSize=ref(props.defaultPageSize || 10)
const dates=reactive({start:'',end:''})
const requestedPage=ref(1)
const date=value=>value.toLocaleDateString('sv-SE',{timeZone:'Asia/Shanghai'})
const amount=(value,currency)=>value!==''&&value!=null&&currency?value+' '+currency:'未提供'
const feeRecord=t=>({trade:t,card_id:card.value?.id,refund_fee_override:props.savedTrades.find(r=>r.card_id===card.value?.id&&r.trade?.tradeNo===t.tradeNo)?.refund_fee_override,reversal_fee:props.savedTrades.find(r=>r.card_id===card.value?.id&&r.trade?.orderNo===t.orderNo&&r.trade?.tradeNo===t.tradeNo&&r.trade?.tradeType===t.tradeType)?.reversal_fee})
let controller,disposed=false
async function open(value){
 if(busy.value)return
 card.value=value;opened.value=true;rows.value=[];applied.value=null;error.value='';queriedAt.value='';page.value=1;pageSize.value=props.defaultPageSize || 10;hasMore.value=false
 const now=new Date();dates.end=date(now);dates.start=date(new Date(now.getTime()-29*86400000))
 await query(1,true)
}
async function changeSize(event){const size=Number(event.target.value);await query(1,false,size)}
async function query(nextPage=1,newSearch=false,nextSize=pageSize.value){
 if(busy.value||!card.value)return
 const range=newSearch?{...dates}:applied.value||{...dates}
 if(!range?.start||!range?.end){error.value='请选择开始和结束日期';return}
 busy.value=true;requestedPage.value=nextPage;error.value='';controller=new AbortController()
 try{
  const result=await api('/api/ejh/cards/'+encodeURIComponent(card.value.id)+'/trades',{method:'POST',body:{start:range.start,end:range.end,page:nextPage,page_size:nextSize},signal:controller.signal})
  if(disposed)return
  cached.value=!!result.cached;rows.value=result.items||[];page.value=result.page;pageSize.value=nextSize;hasMore.value=result.has_more;queriedAt.value=result.queried_at;applied.value={start:range.start,end:range.end}
 }catch(e){if(!disposed)error.value=e.message}finally{busy.value=false}
}
onBeforeUnmount(()=>{disposed=true;controller?.abort()})
defineExpose({open})
</script>
<template><Teleport to="body"><div v-if="opened" class="modal-backdrop card-trades-overlay" @click.self="!busy&&(opened=false)"><section class="modal card-trades-dialog" role="dialog" aria-modal="true" aria-label="银行卡交易明细">
<div class="modal-heading"><h2>银行卡交易明细</h2><button type="button" class="btn ghost" :disabled="busy" @click="opened=false">关闭</button></div>
<p>{{card.local?.name||'EJH 卡片'}} · {{card.remote?.cardNo}} · {{card.remote?.orderNo}}</p>
<form class="trade-dates" @submit.prevent="query(1,true)"><label class="field"><span>开始日期</span><input v-model="dates.start" type="date" required :disabled="busy"/></label><label class="field"><span>结束日期</span><input v-model="dates.end" type="date" required :disabled="busy"/></label><button class="btn primary" :disabled="busy">{{busy?'正在查询…':'查询交易明细'}}</button></form>
<p v-if="card.remote?.status==='2'" class="muted">此卡已销卡，显示已保存的交易；销卡退回未确认时由后台继续补查，确认后停止查询。</p><p class="muted">默认查询最近 30 天，每次最多 90 天、每页 {{pageSize}} 条。仅查询当前银行卡；授权、清算、撤销、退款和销卡流水分别展示。</p>
<p v-if="busy" role="status">正在读取第 {{requestedPage}} 页交易明细，请稍候…</p><p v-if="error" role="alert" class="danger-text">{{error}}{{applied?'；下方保留上次成功查询的结果。':''}}</p>
<p v-if="applied">显示 {{applied.start}} 至 {{applied.end}} · 第 {{page}} 页 · 本页 {{rows.length}} 条 · {{cached?'历史保存于':'查询于'}} {{formatTime(queriedAt)}}</p>
<div class="table-shell"><table><thead><tr><th>流水 / 类型</th><th>入账状态</th><th>刷卡金额</th><th>交易 / 结算金额</th><th>撤销 / 退款手续费</th><th>商户 / 时间</th><th>其他明细</th></tr></thead><tbody><tr v-for="t in rows" :key="t.tradeNo+':'+t.tradeType"><td>{{t.tradeNo}}<small>{{tradeKinds[t.tradeType]||t.tradeType}}</small></td><td>{{t.tradeStatus==='2'?'已入账':t.tradeStatus==='1'?'待入账':'状态未知'}}<small v-if="t.tradeType==='2'&&['1','2'].includes(t.tradeStatus)">授权已撤销，按回款识别；入账状态保留卡台原值</small></td><td>{{amount(t.cardAmount,t.cardCurrency)}}</td><td>{{amount(t.tradeAmount,t.tradeCurrency)}}<small>结算 {{amount(t.billAmount,t.billCurrency)}}</small></td><td><EJHReversalFee :records="[feeRecord(t)]" :cards="[card]" :operations="operations" :fee-percent="feePercent" hide-label/></td><td>{{t.collectionName||'商户未提供'}}<small>交易 {{t.tradeTime||'—'}}</small><small>记账 {{t.createdAt||'—'}}</small></td><td><details><summary>展开</summary><p>授权码 {{t.authCode||'—'}}<br>汇率 {{t.buyTradeBankRate||'—'}}<br>退款金额字段 {{t.refundBuyPrice||'未提供'}}<br>退款去向 {{t.refundTag==='1'?'账户':t.refundTag?'未知':'卡片'}}<br>备注 {{t.extInfoyx||'—'}}<br>商户地址 {{t.collectionAddress||'—'}}</p></details></td></tr></tbody></table></div>
<p v-if="applied&&!rows.length&&!busy">本页没有交易记录{{page>1?'，可返回上一页':'，可调整日期后查询'}}。</p>
<div class="panel-actions"><label>每页 <select :value="pageSize" :disabled="busy" @change="changeSize"><option v-for="size in [10,50,100,500]" :key="size" :value="size">{{size}}</option></select> 条</label><button class="btn ghost" :disabled="busy||page<=1" @click="query(page-1)">上一页</button><span>第 {{page}} 页</span><button class="btn ghost" :disabled="busy||!hasMore" @click="query(page+1)">下一页</button></div>
</section></div></Teleport></template>
<style scoped>.card-trades-overlay{z-index:12500}.card-trades-dialog{width:min(1200px,96vw);max-height:90vh;overflow:auto}.trade-dates{display:flex;align-items:flex-end;gap:12px;flex-wrap:wrap;margin:18px 0}.trade-dates .field{width:190px}.card-trades-dialog p{margin:12px 0;line-height:1.7;overflow-wrap:anywhere}.card-trades-dialog table{width:100%;min-width:850px}.card-trades-dialog td{vertical-align:top;max-width:220px;overflow-wrap:anywhere}.card-trades-dialog small{display:block;color:var(--muted);margin-top:5px}.card-trades-dialog summary{cursor:pointer}.card-trades-dialog .panel-actions{margin-top:18px;align-items:center}</style>
