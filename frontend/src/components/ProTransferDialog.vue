<script setup>
import { computed, ref } from 'vue'
import { Download, Upload, LoaderCircle, X } from 'lucide-vue-next'
import { api, downloadFile } from '../api'
const props=defineProps({ selected:{type:Array,default:()=>[]},query:{type:String,default:''},mergeState:{type:String,default:''} })
const emit=defineEmits(['updated','busy'])
const opened=ref(false),mode=ref('export'),busy=ref(false),error=ref(''),notice=ref(''),scope=ref('selected'),overwrite=ref(false),file=ref(null),filename=ref(''),done=ref(0),results=ref([])
const counts=computed(()=>results.value.reduce((acc,row)=>{acc[row.status]=(acc[row.status]||0)+1;return acc},{imported:0,updated:0,skipped:0,failed:0}))
const resultLabels={imported:'已导入',updated:'已更新',skipped:'已跳过',failed:'失败'}
function open(){if(busy.value)return;opened.value=true;error.value='';notice.value='';scope.value=props.selected.length?'selected':'all';file.value=null;filename.value='';results.value=[];done.value=0;overwrite.value=false}
function close(){if(!busy.value){opened.value=false;file.value=null}}
async function chooseFile(event){
  file.value=null;results.value=[];done.value=0;error.value='';notice.value=''
  const selected=event.target.files?.[0];filename.value=selected?.name||''
  if(!selected)return
  if(selected.size>64*1024*1024){error.value='文件超过 64MB，请分批导出';return}
  busy.value=true;emit('busy',true)
  try{
    const data=JSON.parse(await selected.text())
    if(data.format!=='space-pro-accounts'||data.version!==1||!data.exported_at||!Array.isArray(data.accounts)||!data.accounts.length||data.accounts.length>10000)throw new Error('请选择本项目导出的 Pro 账号迁移 JSON')
    const emails=new Set()
    for(const item of data.accounts){
      const email=item.profile?.email?.trim().toLowerCase()
      if(!email||emails.has(email)||item.profile?.management_scope!=='pro'||item.credentials?.email?.trim().toLowerCase()!==email)throw new Error('文件包含无效或重复的 Pro 账号')
      emails.add(email)
    }
    file.value=data
  }catch(e){error.value=e instanceof SyntaxError?'JSON 格式错误':e.message}
  finally{busy.value=false;emit('busy',false)}
}
async function exportJSON(){
  if(busy.value)return
  busy.value=true;emit('busy',true);error.value='';notice.value='正在生成并下载迁移文件…'
  try{
    await downloadFile('/api/pro-accounts/export','pro-accounts.json',{method:'POST',body:scope.value==='selected'?{emails:props.selected.map(a=>a.email)}:{all:true,query:props.query,merge_state:props.mergeState}})
    notice.value='JSON 已导出，可在目标环境的 Pro 管理中导入'
  }catch(e){error.value=e.message;notice.value=''}
  finally{busy.value=false;emit('busy',false)}
}
async function importJSON(){
  if(busy.value||!file.value)return
  busy.value=true;emit('busy',true);error.value='';notice.value='';results.value=[];done.value=0
  const data=file.value
  try{
    for(let offset=0;offset<data.accounts.length;offset+=20){
      const batch=data.accounts.slice(offset,offset+20)
      const response=await api('/api/pro-accounts/import',{method:'POST',body:{file:{...data,accounts:batch},overwrite:overwrite.value}})
      results.value.push(...response.results);done.value+=batch.length
    }
    notice.value='导入完成。已暂停自动调度，可从列表中的“继续迁移流程”或单项按钮继续'
  }catch(e){error.value=e.message+'；已成功导入的账号会保留，可重试同一文件'}
  finally{busy.value=false;emit('busy',false);emit('updated')}
}
defineExpose({open})
</script>
<template>
<Teleport to="body"><div v-if="opened" class="modal-backdrop" @click.self="close"><section class="modal pro-transfer-dialog" role="dialog" aria-modal="true" aria-label="Pro 账号导入导出">
  <div class="modal-heading"><h2>导入 / 导出 JSON</h2><button class="icon-button" title="关闭导入导出" :disabled="busy" @click="close"><X :size="17"/></button></div>
  <nav class="transfer-tabs"><button class="btn" :class="{primary:mode==='export'}" :disabled="busy" @click="mode='export';error='';notice=''"><Download :size="15"/>导出 JSON</button><button class="btn" :class="{primary:mode==='import'}" :disabled="busy" @click="mode='import';error='';notice=''"><Upload :size="15"/>导入 JSON</button></nav>
  <p>包含账号密码、2FA、AT/RT、Session、六阶段、四步状态和原订单凭据。文件含敏感凭证，请妥善保管。</p>
  <p class="muted">迁移前请停止来源环境的任务。目标环境使用自己的全局代理、母号专属代理和推送配置；正在登录的内存会话需要重新建立。</p>
  <template v-if="mode==='export'">
    <label class="choice"><input v-model="scope" value="selected" type="radio" :disabled="busy||!selected.length"/>已选账号（{{ selected.length }}）</label>
    <label class="choice"><input v-model="scope" value="all" type="radio" :disabled="busy"/>当前筛选的全部账号（不限本页）</label>
    <button class="btn primary" :disabled="busy||(scope==='selected'&&!selected.length)" @click="exportJSON"><LoaderCircle v-if="busy" class="spin" :size="15"/><Download v-else :size="15"/>{{busy?'正在导出…':'下载 JSON'}}</button>
  </template>
  <template v-else>
    <label class="field"><span>迁移 JSON 文件</span><input type="file" accept=".json,application/json" :disabled="busy" @change="chooseFile"/></label>
    <p v-if="file">{{filename}} · {{file.accounts.length}} 个账号</p>
    <label class="choice"><input v-model="overwrite" type="checkbox" :disabled="busy"/>覆盖同邮箱 Pro 账号（以文件进度为准，默认跳过已存在账号）</label>
    <p>导入后保留进度并暂停自动调度，不会重复付款。原订单可继续查询；合并结果待确认时先核实再续跑。</p>
    <button class="btn primary" :disabled="busy||!file" @click="importJSON"><LoaderCircle v-if="busy" class="spin" :size="15"/><Upload v-else :size="15"/>{{busy?'正在导入…':'开始导入'}}</button>
    <section v-if="done||results.length" class="transfer-progress" aria-live="polite">
      <progress :value="done" :max="file?.accounts.length||1" aria-label="Pro 账号导入进度"/>
      <p>{{done}} / {{file?.accounts.length}} · 新增 {{counts.imported}} · 更新 {{counts.updated}} · 跳过 {{counts.skipped}} · 失败 {{counts.failed}}</p>
      <ul><li v-for="row in results" :key="row.email" :class="{'danger-text':row.status==='failed'}"><strong>{{row.email}}</strong>：{{resultLabels[row.status]}}<small v-if="row.message">{{row.message}}</small></li></ul>
    </section>
  </template>
  <p v-if="error" class="danger-text" role="alert">{{error}}</p><p v-else-if="notice" role="status">{{notice}}</p>
  <div class="panel-actions"><button class="btn ghost" :disabled="busy" @click="close">关闭</button></div>
</section></div></Teleport>
</template>
<style scoped>
.pro-transfer-dialog{width:min(760px,calc(100vw - 32px));max-height:90vh;overflow:auto}
.transfer-tabs{display:flex;gap:8px;margin:16px 0}.choice{display:flex;align-items:center;gap:8px;margin:16px 0}
p{font-size:12px;line-height:1.7;overflow-wrap:anywhere}.transfer-progress progress{width:100%;margin-top:16px}
.transfer-progress ul{max-height:240px;overflow:auto;padding-left:20px;font-size:12px}.transfer-progress li{margin:10px 0;overflow-wrap:anywhere}.transfer-progress small{display:block;color:var(--muted);margin-top:4px}
.spin{animation:pro-transfer-spin 1s linear infinite}@keyframes pro-transfer-spin{to{transform:rotate(360deg)}}
</style>
