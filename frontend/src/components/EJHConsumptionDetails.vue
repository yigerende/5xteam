<script setup>
import { computed, ref, watch } from 'vue'
import { api } from '../api'
import { consumptionExceptions } from '../ejh-consumption'
import { applyPaymentPolicies } from '../payment-only'
import { returnLabel } from '../ejh'
import { formatTime } from '../utils'

const props = defineProps({ data: { type:Object, required:true }, kind: {type:String,default:'missing'} })
const emit = defineEmits(['updated','close'])
const rows = computed(() => consumptionExceptions(applyPaymentPolicies(props.data).cases || [], props.data.consumption_history || [])[props.kind] || [])
const editing = ref(null), reason = ref(''), confirm = ref(false), saving = ref(false), error = ref('')
function edit(row, state) { editing.value = {row,state}; reason.value = ''; confirm.value = false; error.value = '' }
watch(() => props.kind, () => { editing.value = null; error.value = '' })
async function save() {
  if (saving.value || !editing.value || !confirm.value || reason.value.trim().length < 3) return
  const {row,state} = editing.value
  saving.value = true; error.value = ''
  try {
    await api('/api/ejh/cases/'+encodeURIComponent(row.id)+'/consumption-review', {method:'PUT',body:{state,reason:reason.value.trim(),confirm:true,expected_key:row.consumption_review_key,expected_revision:row.consumption_review?.revision||0}})
    editing.value = null; emit('updated')
  } catch (e) { error.value = e.message; emit('updated') }
  finally { saving.value = false }
}
</script>
<template>
 <section class="consumption-details" role="dialog" aria-modal="true" aria-label="消费查询账号明细">
  <header><h2>{{kind==='missing'?'消费未查询账号':'未推送账号'}}（{{rows.length}}）</h2><button type="button" class="btn ghost compact" :disabled="saving" @click="emit('close')">关闭</button></header>
  <p>这里是下游消费记录，不是卡片余额。核实原下游没有创建账号、没有消费后，才可确认未推送；付款、回款及推送历史保留。</p>
  <p v-if="!rows.length">当前没有此类账号。</p>
  <article v-for="item in rows" :key="item.email">
   <h3>{{item.email}}</h3>
   <div v-for="row in item.orders" :key="row.id" class="consumption-order">
    <p>{{row.card_name || '关联卡片'}} · 尾号 {{row.last4 || '—'}} · {{returnLabel(row)}}<span v-if="row.deleted"> · 账号已删除</span></p>
    <small>订单 {{row.id}}</small>
    <small>创建 {{formatTime(row.created_at)}}</small>
    <p v-if="row.consumption_state==='not_pushed'">{{row.consumption_review?.state==='not_pushed'?'已核实未推送，无消费数据':'未推送，无消费数据'}}</p>
    <p v-else-if="row.consumption_can_review">流程已结束，没有已保存的下游绑定或消费记录。请核实原下游后处理。</p>
    <p v-else>消费尚未取得。账号仍在处理、存在下游绑定或历史证据时，需从原下游补查。</p>
    <small v-if="row.consumption_review">核实记录：{{row.consumption_review.reason}} · {{formatTime(row.consumption_review.updated_at)}}</small>
    <button v-if="row.consumption_can_review&&row.consumption_state!=='not_pushed'" type="button" class="btn ghost compact" :disabled="saving" @click="edit(row,'not_pushed')">确认未推送</button>
    <button v-if="row.consumption_review?.state==='not_pushed'" type="button" class="btn ghost compact" :disabled="saving" @click="edit(row,'auto')">恢复自动判断</button>
   </div>
  </article>
  <form v-if="editing" @submit.prevent="save">
   <h3>{{editing.state==='auto'?'恢复自动判断':'确认未推送'}}：{{editing.row.email}}</h3>
   <label>核实依据<textarea v-model="reason" rows="3" minlength="3" maxlength="600" required :disabled="saving" placeholder="填写核实的原下游、账号及结果"/></label>
   <label class="consumption-confirm"><input v-model="confirm" type="checkbox" :disabled="saving"/>{{editing.state==='auto'?'确认恢复自动判断':'已核实原下游没有创建或使用该账号，不存在待确认推送'}}</label>
   <div><button type="submit" class="btn primary compact" :disabled="saving||!confirm||reason.trim().length<3">{{saving?'保存中…':'保存核实结果'}}</button><button type="button" class="btn ghost compact" :disabled="saving" @click="editing=null">取消</button></div>
   <p v-if="error" class="danger-text" role="alert">{{error}}</p>
  </form>
 </section>
</template>
<style scoped>
.consumption-details{width:min(720px,calc(100vw - 32px));max-height:85vh;overflow:auto;background:var(--panel,var(--bg));border:1px solid var(--line);border-radius:14px;padding:24px;color:var(--text);box-shadow:0 20px 80px #0003}.consumption-details header{display:flex;align-items:center;justify-content:space-between;gap:12px}.consumption-details h2{font-size:18px}.consumption-details h3{font-size:14px;overflow-wrap:anywhere}.consumption-details p,.consumption-details small{display:block;font-size:12px;line-height:1.7;overflow-wrap:anywhere;margin:6px 0}.consumption-details small{color:var(--muted)}.consumption-details article,.consumption-details form{border-top:1px solid var(--line);padding-top:14px;margin-top:14px}.consumption-order{margin-bottom:12px}.consumption-details textarea{display:block;width:100%;margin:8px 0}.consumption-details label{font-size:12px}.consumption-confirm{display:flex;gap:8px;align-items:flex-start;margin:10px 0}.consumption-confirm input{width:auto}.consumption-details button{margin:6px 8px 0 0}
</style>
