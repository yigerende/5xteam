<script setup>
import {computed,watch} from 'vue'
const props=defineProps({modelValue:{type:Object,required:true},proxies:{type:Array,default:()=>[]},primaryURL:String,disabled:Boolean})
const emit=defineEmits(['update:modelValue'])
const ids=computed(()=>props.modelValue.backup_ids||[])
const options=computed(()=>props.proxies.filter(p=>p.url!==props.primaryURL))
function update(key,value){emit('update:modelValue',{...props.modelValue,[key]:value})}
function setBackup(index,value){const next=[...ids.value];next[index]=value;update('backup_ids',next.filter(Boolean))}
function move(index,delta){const next=[...ids.value];[next[index],next[index+delta]]=[next[index+delta],next[index]];update('backup_ids',next)}
function add(){const p=options.value.find(p=>!ids.value.includes(p.id));if(p)update('backup_ids',[...ids.value,p.id])}
watch(()=>props.primaryURL,()=>{const next=ids.value.filter(id=>props.proxies.some(p=>p.id===id&&p.url!==props.primaryURL));if(next.length!==ids.value.length)update('backup_ids',next)})
</script>
<template>
 <div class="global-proxy-policy field wide">
  <strong>全局代理主备与故障冷却</strong>
  <p>上方全局代理为主用。登录发生网络故障后立即冷却当前线路，选择可用备用从头登录；一个登录会话内保持原代理。全部不可用时自动等待，不改为直连。</p>
  <div v-for="(id,index) in ids" :key="index" class="backup-line">
   <span>备用 {{index+1}}</span><select :value="id" :disabled="disabled" @change="setBackup(index,$event.target.value)"><option v-for="p in options" :key="p.id" :value="p.id" :disabled="p.id!==id&&ids.includes(p.id)">{{p.name}}</option></select>
   <button type="button" :disabled="disabled||index===0" @click="move(index,-1)">上移</button><button type="button" :disabled="disabled||index===ids.length-1" @click="move(index,1)">下移</button><button type="button" :disabled="disabled" @click="update('backup_ids',ids.filter((_,i)=>i!==index))">移除</button>
  </div>
  <button class="btn ghost compact" type="button" :disabled="disabled||!options.some(p=>!ids.includes(p.id))" @click="add">添加备用代理</button>
  <div class="policy-fields">
   <label>故障冷却（秒）<input :value="modelValue.cooldown_seconds" type="number" min="10" max="3600" required :disabled="disabled" @input="update('cooldown_seconds',Number($event.target.value))" /></label>
   <label>恢复测试次数<input :value="modelValue.recovery_tests??''" type="number" min="1" max="10" placeholder="留空：不测试" :disabled="disabled" @input="update('recovery_tests',$event.target.value===''?null:Number($event.target.value))" /></label>
   <label>测试间隔（秒）<input :value="modelValue.test_interval_seconds" type="number" min="1" max="300" required :disabled="disabled||modelValue.recovery_tests===null" @input="update('test_interval_seconds',Number($event.target.value))" /></label>
  </div>
  <small>默认冷却 300 秒，最多测试 3 次、间隔 5 秒；通过即恢复，次数用尽后再次冷却。测试次数留空时到期直接恢复。主用恢复后优先用于新登录，已有会话不中途切换。“任务最少”模式仍按原代理池选择，但跳过冷却和测试中的线路。</small>
 </div>
</template>
<style scoped>
.global-proxy-policy{display:block;padding:16px;border:1px solid var(--line);border-radius:10px}.global-proxy-policy p,.global-proxy-policy small{display:block;color:var(--muted);line-height:1.7;margin:8px 0}.backup-line{display:flex;gap:8px;align-items:center;margin:8px 0;flex-wrap:wrap}.backup-line select{flex:1;min-width:150px}.backup-line button{border:1px solid var(--line);background:transparent;padding:5px 9px;border-radius:6px;color:inherit;cursor:pointer}.backup-line button:disabled{opacity:.4}.policy-fields{display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:14px;margin-top:14px}.policy-fields label{display:grid;gap:7px;font-size:12px}@media(max-width:700px){.policy-fields{grid-template-columns:1fr}}
</style>
