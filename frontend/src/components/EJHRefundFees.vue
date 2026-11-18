<script setup>
import { computed, ref } from 'vue'
import { X, Plus, Pencil } from 'lucide-vue-next'
import { api } from '../api'
import { usdToR } from '../ejh-profit'
import { formatTime } from '../utils'

const props=defineProps({records:{type:Array,default:()=>[]},trades:{type:Array,default:()=>[]},cards:{type:Array,default:()=>[]},disabled:Boolean})
const emit=defineEmits(['saved'])
const opened=ref(false),saving=ref(false),error=ref(''),editing=ref(false),draft=ref({})
const localTime=at=>new Date(Date.parse(at)+8*3600000).toISOString().slice(0,19)
const rows=computed(()=>[...props.records].sort((a,b)=>Date.parse(b.at)-Date.parse(a.at)||a.id.localeCompare(b.id)))
const refundTrades=computed(()=>props.trades.filter(r=>['5','16'].includes(r.trade?.tradeType)&&r.trade.tradeStatus==='2'&&r.trade.billCurrency==='USD'&&Number(r.trade.billAmount)>0))
const cardLabel=id=>props.cards.find(c=>c.id===id)?.local?.name||props.cards.find(c=>c.id===id)?.remote?.cardNo||id
function edit(row){
 error.value='';editing.value=true
 draft.value=row?{...row,trade_ids:[...(row.trade_ids||[])],time:localTime(row.at)}:{id:crypto.randomUUID(),revision:0,amount:'',time:localTime(new Date().toISOString()),source:'manual',note:'',voided:false,trade_ids:[]}
}
async function persist(row){
 if(saving.value)return
 saving.value=true;error.value=''
 try{const saved=await api('/api/ejh/refund-fees/'+encodeURIComponent(row.id),{method:'PUT',body:row});emit('saved',saved);editing.value=false}
 catch(e){error.value=e.message}
 finally{saving.value=false}
}
function save(){const {time,...row}=draft.value;return persist({...row,at:(time.length===16?time+':00':time)+'+08:00'})}
function toggle(row){
 if(!window.confirm(row.voided?'恢复这笔退款手续费记账？':'撤销这笔费用记账？原始流水和余额不会修改。'))return
 return persist({...row,voided:!row.voided})
}
function close(){if(!saving.value){opened.value=false;editing.value=false;error.value=''}}
</script>
<template>
 <button class="btn ghost" type="button" :disabled="disabled" @click="opened=true">退款费用</button>
 <Teleport to="body"><div v-if="opened" class="refund-fee-overlay" @click.self="close" @keydown.esc="close">
  <section class="refund-fee-dialog" role="dialog" aria-modal="true" aria-labelledby="refund-fee-title">
   <header><div><h2 id="refund-fee-title">清算退款手续费</h2><p>按发生时间单列展示，不计成本或净利润 · 1 USD = 6.7 R</p></div><button type="button" class="btn ghost" aria-label="关闭退款费用" :disabled="saving" @click="close"><X :size="18"/></button></header>
   <p class="refund-fee-note">已入账退款按配置费率自动计费。登记已核对费用时，请关联对应退款流水，登记总额将替代这些流水的估算费用，避免重复计费；多笔关联按退款金额分摊。未关联的登记按额外费用计入。</p>
   <p v-if="error" class="refund-fee-error" role="alert">{{error}}</p>
   <form v-if="editing" @submit.prevent="save">
    <div class="refund-fee-fields"><label>发生时间（北京时间）<input v-model="draft.time" type="datetime-local" step="1" required :disabled="saving"/></label><label>手续费 USD<input v-model="draft.amount" type="text" inputmode="decimal" pattern="[0-9]+([.][0-9]{1,2})?" maxlength="15" required :disabled="saving"/><small>{{usdToR(draft.amount)??'—'}} R</small></label><label>来源<select v-model="draft.source" :disabled="saving"><option value="manual">手动登记</option><option value="balance_reconciliation">余额核对</option></select></label></div>
    <label>说明<textarea v-model="draft.note" required maxlength="500" rows="2" :disabled="saving" placeholder="填写核对依据、卡尾号或账单说明"></textarea></label>
    <fieldset class="refund-links"><legend>关联退款流水（替代这些流水的自动手续费）</legend><label v-for="row in refundTrades" :key="row.id"><input type="checkbox" v-model="draft.trade_ids" :value="row.id" :disabled="saving"/><span>{{cardLabel(row.card_id)}} · {{row.trade.billAmount}} USD · {{row.trade.createdAt||row.trade.tradeTime}}<small>{{row.trade.tradeNo}}</small></span></label><small v-if="!refundTrades.length">暂无可关联的已入账退款。</small></fieldset>
    <div class="refund-fee-actions"><button class="btn primary" :disabled="saving">{{saving?'保存中…':'保存费用'}}</button><button class="btn ghost" type="button" :disabled="saving" @click="editing=false">取消</button></div>
   </form>
   <div v-else class="refund-fee-actions"><button class="btn primary" type="button" :disabled="saving" @click="edit()"><Plus :size="14"/>登记费用</button></div>
   <div class="refund-fee-list"><article v-for="row in rows" :key="row.id" :class="{voided:row.voided}"><div><strong>{{row.amount}} USD <small>· {{usdToR(row.amount)}} R</small></strong><span>{{formatTime(row.at)}} · {{row.source==='balance_reconciliation'?'余额核对登记':'手动登记'}}{{row.voided?' · 已撤销':''}}</span><p>{{row.note}}</p></div><div class="refund-fee-actions"><button v-if="!row.voided" class="btn ghost" type="button" :disabled="saving" @click="edit(row)"><Pencil :size="12"/>修改</button><button class="btn ghost" type="button" :disabled="saving" @click="toggle(row)">{{row.voided?'恢复记账':'撤销记账'}}</button></div></article><p v-if="!rows.length" class="refund-fee-note">暂无登记记录</p></div>
  </section>
 </div></Teleport>
</template>
<style scoped>
.refund-fee-overlay{position:fixed;inset:0;z-index:1200;background:#0007;display:flex;align-items:center;justify-content:center;padding:20px}.refund-fee-dialog{width:760px;max-width:100%;max-height:90vh;overflow:auto;background:var(--surface);color:var(--text);border:1px solid var(--line);border-radius:14px;padding:22px}.refund-fee-dialog header{display:flex;justify-content:space-between;gap:16px;align-items:flex-start}.refund-fee-dialog h2{margin:0;font-size:18px}.refund-fee-dialog header p,.refund-fee-note{font-size:12px;color:var(--muted);line-height:1.7}.refund-fee-dialog form{padding:16px;border:1px solid var(--line);border-radius:10px;background:var(--bg)}.refund-fee-fields{display:grid;grid-template-columns:1.4fr 1fr 1fr;gap:12px}.refund-fee-dialog label{display:flex;flex-direction:column;gap:7px;font-size:12px;margin-bottom:12px;min-width:0}.refund-fee-dialog input,.refund-fee-dialog select,.refund-fee-dialog textarea{width:100%;box-sizing:border-box}.refund-fee-dialog small{font-size:11px;color:var(--muted);font-weight:400}.refund-fee-actions{display:flex;align-items:center;gap:8px;flex-wrap:wrap;margin:10px 0}.refund-fee-actions .btn{display:flex;align-items:center;gap:5px;font-size:12px}.refund-fee-list article{display:flex;justify-content:space-between;gap:16px;padding:16px 0;border-bottom:1px solid var(--line)}.refund-fee-list article>div:first-child{min-width:0}.refund-fee-list strong{display:block;font-size:16px}.refund-fee-list span{display:block;color:var(--muted);font-size:11px;margin-top:7px}.refund-fee-list p{font-size:12px;line-height:1.7;overflow-wrap:anywhere;margin-bottom:0}.refund-fee-list .voided{opacity:.6}.refund-fee-error{color:var(--red);font-size:12px}@media(max-width:640px){.refund-fee-fields{grid-template-columns:1fr}.refund-fee-list article{display:block}.refund-fee-dialog{padding:16px}.refund-fee-overlay{padding:12px}}
</style>

<style scoped>
.refund-links{border:1px solid var(--line);padding:12px;border-radius:8px;max-height:220px;overflow:auto;margin-bottom:12px}.refund-links legend{font-size:12px;color:var(--muted)}.refund-links label{display:flex;flex-direction:row;align-items:flex-start;gap:8px}.refund-links input{width:auto;flex:none;margin-top:3px}.refund-links span{overflow-wrap:anywhere}.refund-links small{display:block}
</style>
