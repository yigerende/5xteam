<script setup>
import { computed, ref } from 'vue'
import { api } from '../api'
import { paymentStatusLabel } from '../card-status'
import { payProviderName, proPlanName } from '../gptpay'
import { formatTime } from '../utils'
const props=defineProps({provider:{type:String,default:'ejh'}})
const base=computed(()=>props.provider==='nexovcc'?'/api/nexovcc/occupancy/':'/api/ejh/occupancy/')
const issuer=computed(()=>props.provider==='nexovcc'?'环球':'EJH')
const emit=defineEmits(['updated'])
const opened=ref(false),busy=ref(false),error=ref(''),order=ref(null),card=ref(null),outcome=ref(''),evidence=ref(''),confirmed=ref(false)
const allowed=computed(()=>order.value&&['success','failed','manual_review','submission_unknown'].includes(order.value.status))
const canSave=computed(()=>allowed.value&&!!outcome.value&&confirmed.value&&evidence.value.trim().length>=5&&!busy.value)
async function open(row,local){
 if(busy.value)return
 opened.value=true;busy.value=true;error.value='';order.value=null;card.value=local;outcome.value='';evidence.value='';confirmed.value=false
 try{order.value=await api(base.value+encodeURIComponent(row.order_id))}catch(e){error.value=e.message}finally{busy.value=false}
}
async function query(){
 if(busy.value||!order.value?.remote?.id)return
 busy.value=true;error.value='';confirmed.value=false;outcome.value=''
 try{order.value=await api('/api/gptpay/orders/'+encodeURIComponent(order.value.id)+'/refresh',{method:'POST',body:{}})}catch(e){error.value=e.message}finally{busy.value=false}
}
async function save(){
 if(!canSave.value)return
 busy.value=true;error.value=''
 try{
  await api(base.value+encodeURIComponent(order.value.id)+'/resolve',{method:'POST',body:{card_id:card.value.id,email:order.value.email,expected_status:order.value.status,outcome:outcome.value,evidence:evidence.value.trim(),confirm:confirmed.value}})
  opened.value=false;emit('updated')
 }catch(e){error.value=e.message}finally{busy.value=false}
}
defineExpose({open})
</script>
<template><Teleport to="body"><div v-if="opened" class="modal-backdrop occupancy-overlay" @click.self="!busy&&(opened=false)"><section class="modal occupancy-dialog" role="dialog" aria-modal="true" aria-label="核实开通结果并处理占用">
<h2>核实开通结果并处理占用</h2>
<p v-if="busy" role="status">正在读取或保存，请稍候…</p><p v-if="error" role="alert" class="danger-text">{{error}}</p>
<template v-if="order"><p><strong>{{order.email}}</strong><br>卡尾号 {{card.last4}} · {{payProviderName(order.provider)}} · {{proPlanName(order.plan_code)}}<br>本地订单 {{order.id}}<br>供应商订单 {{order.remote?.id||'未取得供应商订单编号'}}<br>开通状态：{{paymentStatusLabel(order.status)}} · 更新 {{formatTime(order.updated_at)}}</p>
<p v-if="order.error||order.status_check_error" class="danger-text">{{order.error}}<br>{{order.status_check_error}}</p>
<button type="button" class="btn ghost" :disabled="busy||!order.remote?.id" @click="query">查询原订单状态</button>
<p v-if="!order.remote?.id">未取得供应商订单编号，请凭本地订单或请求编号 {{order.request_id||'未记录'}} 向供应商核实是否执行、是否扣款。查不到订单不等于开通失败。</p>
<p v-if="!allowed" class="danger-text">原订单仍在处理中，请先查询结果；暂不能保存核实结果。</p>
<form @submit.prevent="save"><label class="field"><span>核实结果</span><select v-model="outcome" :disabled="busy||!allowed" @change="confirmed=false"><option value="">请选择实际核实结果</option><option v-if="order.status!=='failed'" value="success_removed">已开通成功，且已完成合并、移出空间</option><option v-if="order.status!=='success'" value="not_activated">已人工核实未开通，解除本单占用</option></select></label>
<p v-if="outcome==='success_removed'">保留累计开通次数，记录为已申请待回款；继续占用此卡，自动识别回款后才可复用。</p>
<p v-if="outcome==='not_activated'">记录本次开通失败并释放在途名额，保留原请求和供应商返回记录。</p>
<label class="field"><span>核实依据（供应商回复、原订单结果或合并移出记录）</span><textarea v-model="evidence" :disabled="busy" minlength="5" maxlength="600" required placeholder="填写原订单核实结果；也可在账号回款的未开通列表删除该记录并解除占用"/></label>
<label class="occupancy-confirm"><input v-model="confirmed" type="checkbox" :disabled="busy"/>我已核实此账号和原订单，确认上述结果属实</label>
<p class="muted">仅核实这笔旧订单的本地状态，不发送开通、退款或销卡请求；核实记录保存在 {{issuer}} 核对审计中。</p>
<div class="panel-actions"><button type="submit" class="btn primary" :disabled="!canSave">{{busy?'处理中…':'确认核实结果'}}</button><button type="button" class="btn ghost" :disabled="busy" @click="opened=false">关闭</button></div></form></template>
<button v-else type="button" class="btn ghost" :disabled="busy" @click="opened=false">关闭</button>
</section></div></Teleport></template>
<style scoped>
.occupancy-overlay{z-index:13000}.occupancy-dialog{width:min(700px,94vw);max-height:88vh;overflow:auto}.occupancy-dialog p{margin:14px 0;overflow-wrap:anywhere;line-height:1.7}.occupancy-dialog .field{margin:14px 0}.occupancy-confirm{display:flex;gap:8px;align-items:flex-start}.occupancy-confirm input{width:auto;margin-top:4px}
</style>
