<script setup>
import { ref } from 'vue'
import { api } from '../api'
const props=defineProps({disabled:Boolean})
const emit=defineEmits(['saved'])
const modal=ref(null),preview=ref(null),note=ref(''),busy=ref(false),error=ref(''),pending=ref(null)
const storageKey='ejh-adjustment-pending'
function clearPending(){pending.value=null;sessionStorage.removeItem(storageKey)}
async function open(){
 if(props.disabled||busy.value)return
 error.value='';preview.value=null;note.value='';if(!modal.value.open)modal.value.showModal();busy.value=true
 try{
  const saved=sessionStorage.getItem(storageKey)
  pending.value=saved?JSON.parse(saved):null
  if(pending.value){
   try{const row=await api('/api/ejh/adjustments?request_id='+encodeURIComponent(pending.value.request_id));clearPending();emit('saved',row);modal.value.close();return}catch(e){if(e.status!==404)throw e}
   preview.value={gap_before:pending.value.expected_gap,total_before:pending.value.expected_total,suggested_amount:pending.value.amount};note.value=pending.value.note
   error.value='上次提交结果待确认。继续核实将沿用原请求编号，不会重复记账。'
  }else preview.value=await api('/api/ejh/adjustments/preview')
 }catch(e){error.value=e.message}finally{busy.value=false}
}
async function submit(){
 if(busy.value||!preview.value||!note.value.trim())return
 busy.value=true;error.value=''
 try{
  if(!pending.value){pending.value={request_id:crypto.randomUUID(),expected_gap:preview.value.gap_before,expected_total:preview.value.total_before,amount:preview.value.suggested_amount,note:note.value.trim()};sessionStorage.setItem(storageKey,JSON.stringify(pending.value))}
  const row=await api('/api/ejh/adjustments',{method:'POST',body:pending.value,timeoutMs:45000})
  clearPending();emit('saved',row);modal.value.close()
 }catch(e){
  error.value=e.message
  if([400,409,422].includes(e.status)){clearPending();preview.value=null}
 }finally{busy.value=false}
}
</script>
<template>
 <button type="button" class="btn ghost" :disabled="disabled||busy" @click="open">一键平账</button>
 <Teleport to="body"><dialog ref="modal" class="adjustment-dialog" aria-labelledby="adjustment-title" @cancel="busy&&$event.preventDefault()">
  <h2 id="adjustment-title">确认手动平账</h2><p>只调整核对差额，不改变真实余额、成本或净利润。操作会永久留档。</p>
  <p v-if="busy&&!preview" role="status">正在计算当前核对金额…</p>
  <template v-if="preview">
   <dl><div v-if="preview.raw_gap!=null"><dt>原始核对差额</dt><dd>{{preview.raw_gap}} USD</dd></div><div><dt>此前累计平账</dt><dd>{{preview.total_before}} USD</dd></div><div><dt>当前剩余差额</dt><dd>{{preview.gap_before??'待核实'}} USD</dd></div><div class="adjustment-emphasis"><dt>本次平账金额</dt><dd>{{Number(preview.suggested_amount)>0?'+':''}}{{preview.suggested_amount??'—'}} USD</dd></div></dl>
   <small>依据最近保存的余额及流水；余额时点可在核对明细中查看。</small>
   <label>平账原因<textarea v-model="note" maxlength="1000" rows="3" placeholder="记录本次平账原因和核对依据" :disabled="busy||!!pending"/></label>
   <p v-if="preview.suggested_amount==='0.00'">当前差额为零，无需平账。</p>
  </template>
  <p v-if="error" class="danger-text" role="alert">{{error}}</p>
  <footer><button class="btn ghost" :disabled="busy" @click="modal.close()">关闭</button><button v-if="!preview&&!busy" class="btn ghost" @click="open">重新读取</button><button class="btn primary" :disabled="busy||!preview?.suggested_amount||preview.suggested_amount==='0.00'||!note.trim()" @click="submit">{{busy?'处理中…':pending?'继续核实原请求':'确认平账并记录'}}</button></footer>
 </dialog></Teleport>
</template>
<style scoped>
.adjustment-dialog{width:min(510px,calc(100vw - 32px));max-height:85vh;overflow:auto;box-sizing:border-box;border:1px solid var(--line);border-radius:12px;background:var(--surface);color:var(--text);padding:24px}.adjustment-dialog::backdrop{background:#0006}.adjustment-dialog h2{font-size:18px;margin:0 0 12px}.adjustment-dialog p,.adjustment-dialog small{font-size:12px;line-height:1.7;color:var(--muted)}.adjustment-dialog dl{margin:18px 0}.adjustment-dialog dl>div{display:flex;justify-content:space-between;gap:12px;padding:10px 0;border-bottom:1px solid var(--line);font-size:13px}.adjustment-dialog dd{margin:0;font-variant-numeric:tabular-nums}.adjustment-emphasis{color:var(--green-strong);font-weight:600}.adjustment-dialog label{display:block;font-size:12px;margin-top:16px}.adjustment-dialog textarea{display:block;width:100%;box-sizing:border-box;margin-top:8px;resize:vertical}.adjustment-dialog footer{display:flex;justify-content:flex-end;gap:8px;flex-wrap:wrap;margin-top:20px}.adjustment-dialog .danger-text{color:var(--red)}
</style>
