<script setup>
import { computed, onMounted, reactive, ref } from 'vue'
import { api } from '../api'
import MessageBar from './MessageBar.vue'
import Sub2ProxyPicker from './Sub2ProxyPicker.vue'
const props=defineProps({ transfer: {type:Boolean,default:true} })
const form=ref(null), groups=ref([]), busy=ref(false)
const message=reactive({text:'',type:''})
const models=computed({get:()=>form.value?.sub2.models?.join('\n')||'',set:v=>{form.value.sub2.models=v.split(/[\n,]/).map(s=>s.trim()).filter(Boolean)}})
function normalize(data){return {...data,monitor_group_ids:data.monitor_group_ids||[],sub2:{...data.sub2,proxy_release_minutes:data.sub2?.proxy_release_minutes||0,group_ids:data.sub2?.group_ids||[],group_names:data.sub2?.group_names||[],proxy_ids:data.sub2?.proxy_ids||[],models:data.sub2?.models||[]}}}
async function save(){busy.value=true;try{if(form.value.sub2.proxy_release_minutes===null)throw new Error('请填写代理绑定分钟数');form.value=normalize(await api('/api/pro-siwc/settings',{method:'PUT',body:form.value}));message.text='SIWC 设置已保存';message.type='success'}catch(e){message.text=e.message;message.type='error'}finally{busy.value=false}}
async function loadGroups(){busy.value=true;try{const r=await api('/api/pro-siwc/groups',{method:'POST',body:form.value});groups.value=r.groups;message.text=''}catch(e){message.text=e.message;message.type='error'}finally{busy.value=false}}
onMounted(async()=>{try{form.value=normalize(await api('/api/pro-siwc/settings'))}catch(e){message.text=e.message;message.type='error'}})
</script>
<template>
 <section class="siwc-settings" aria-label="SIWC 转入和额度设置">
  <h3>{{transfer?'降智转入 SIWC':'SIWC 额度退出规则'}}</h3>
  <MessageBar :message="message" />
  <template v-if="form">
   <template v-if="transfer">
    <p class="table-note">移除原服务分组后授权并推送。期间暂停自动补货；推送成功等待 10 秒并核验监控，再合并两边并发和容量。原推送超时继续计时。</p>
    <div class="siwc-grid">
     <label class="field wide"><span>Sub2SIWC 地址</span><input v-model="form.sub2.url" placeholder="https://siwc.example.com" /></label>
     <label class="field"><span>管理员邮箱</span><input v-model="form.sub2.email" /></label>
     <label class="field"><span>管理员密码</span><input v-model="form.password" type="password" :placeholder="form.sub2.password_present?'已保存，留空不修改':'请输入密码'" /></label>
     <label class="field"><span>账号并发</span><input v-model.number="form.sub2.account_concurrency" type="number" min="1" max="100" /></label>
     <label class="field"><span>优先级</span><input v-model.number="form.sub2.priority" type="number" min="1" max="100" /></label>
    </div>
    <button class="btn ghost compact" type="button" :disabled="busy" @click="loadGroups">{{busy?'读取中…':'读取 SIWC 分组'}}</button>
    <div class="siwc-grid"><div v-for="[key,label] in [['group_ids','推送分组'],['monitor_group_ids','补货监控分组']]" :key="key" class="field"><span>{{label}}</span><div class="group-options"><label v-for="g in groups" :key="g.id"><input v-if="key==='group_ids'" v-model="form.sub2.group_ids" type="checkbox" :value="g.id"/><input v-else v-model="form.monitor_group_ids" type="checkbox" :value="g.id"/>{{g.name}} <small>#{{g.id}}</small></label><small v-if="!groups.length">已选：{{(key==='group_ids'?form.sub2.group_ids:form.monitor_group_ids)?.join(', ')||'无'}}；读取分组后选择</small></div></div></div>
    <Sub2ProxyPicker v-model:release-minutes="form.sub2.proxy_release_minutes" auth-mode="siwc" v-model:enabled="form.sub2.bind_proxy" v-model:ids="form.sub2.proxy_ids" :url="form.sub2.url" :email="form.sub2.email" :password="form.password||''" active :disabled="busy" endpoint="/api/pro-siwc/proxies"/>
    <label class="field"><span>可用模型（每行一个，留空不限制）</span><textarea v-model="models" rows="3" /></label>
   </template>
   <p class="table-note">直接推送和降智转入 SIWC 均使用以下额度规则；任一已启用窗口达到阈值后退出并按原配置合并/删除。5h、7d 百分比单独显示，消费金额继续累计。</p>
   <div class="siwc-grid">
    <label v-for="[enabled,value,label] in [['five_hour_enabled','five_hour_remaining','5h'],['seven_day_enabled','seven_day_remaining','7d']]" :key="enabled" class="field"><span><input v-model="form[enabled]" type="checkbox" />{{label}} 剩余 ≤（%）时退出</span><input v-model.number="form[value]" type="number" min="0" max="100" step="0.1" :disabled="!form[enabled]" /></label>
    <label class="field"><span>SIWC 额度检查周期（秒）</span><input v-model.number="form.poll_seconds" type="number" min="10" max="86400" /></label>
   </div>
   <button type="button" class="btn primary" :disabled="busy" @click="save">{{busy?'处理中…':'保存 SIWC 设置'}}</button>
  </template>
 </section>
</template>
<style scoped>
.siwc-settings{border-top:1px solid var(--line);padding-top:20px;margin-top:20px}.siwc-settings{margin-bottom:20px}.siwc-settings h3{font-size:15px;margin-bottom:12px}.siwc-grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:16px;margin:16px 0}.wide{grid-column:1/-1}.group-options{border:1px solid var(--line);border-radius:6px;padding:10px;max-height:180px;overflow:auto}.group-options label{display:flex;align-items:center;gap:8px;margin:6px 0}input[type=checkbox]{width:15px;height:15px;min-height:15px;margin-right:6px}.table-note{white-space:normal;line-height:1.8}@media(max-width:700px){.siwc-grid{grid-template-columns:1fr}}
</style>
