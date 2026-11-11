<script setup>
import {ref,reactive,computed,watch,onMounted,onUnmounted} from 'vue'
import {api} from '../api'
import {createLatestRequest} from '../latestRequest'

const props=defineProps({settings:Boolean,compact:Boolean})
const detailDialog=ref(null),fetchError=ref(''),state=ref(null),form=reactive({rules:[]}),password=ref(''),groups=ref([]),monitorGroups=ref([])
const groupsLoading=ref(false),monitorLoading=ref(false),groupsLoaded=ref(false),monitorLoaded=ref(false),groupsError=ref(''),monitorError=ref('')
const groupRequest=createLatestRequest(v=>groupsLoading.value=v),monitorRequest=createLatestRequest(v=>monitorLoading.value=v)
const busy=ref(false),error=ref(''),notice=ref(''),dirty=ref(false)
const request=createLatestRequest(),clock=ref(Date.now()),clockOffset=ref(0)
let timer,disposed=false,revision=0,ruleKey=0,groupRevision=0
const accountRequests=new Map()
const selectedGroups=c=>[...(c.group_ids??(c.group_id?[c.group_id]:[]))]
const draftRule=r=>({...r,enabled:r.enabled!==false,group_ids:selectedGroups(r),account_ids:[...(r.account_ids||[])],_key:++ruleKey,_accounts:[],_names:{},_loaded:false,_complete:false,_loading:false,_error:'',_warnings:[],_search:''})
function setDraft(c,monitor){for(const r of form.rules)cancelAccounts(r);Object.assign(form,c);form.account_names={...(c.account_names||{})};form.rules=(c.rules??[{enabled:true,monitor_group_id:monitor?.group_id||0,group_ids:selectedGroups(c),account_ids:c.account_ids||[]}]).map(draftRule)}
const allIDs=computed(()=>[...new Set(form.rules.filter(r=>r.enabled).flatMap(r=>r.account_ids))])
const groupOptions=computed(()=>[...groups.value,...[...new Set(form.rules.flatMap(r=>r.group_ids))].filter(id=>!groups.value.some(g=>g.id===id)).map(id=>({id,name:groupsLoaded.value?'已失效／已删除的兜底分组':'已保存兜底分组'}))])
const monitorOptions=computed(()=>[...monitorGroups.value,...[...new Set(form.rules.map(r=>r.monitor_group_id).filter(Boolean))].filter(id=>!monitorGroups.value.some(g=>g.id===id)).map(id=>({id,name:state.value?.rules?.find(r=>r.rule.monitor_group_id===id)?.monitor.group_name||'已保存监控分组'}))])
const phases={active:'目标：开启兜底',off:'目标：关闭兜底',mixed:'各规则独立调度',disabled:'兜底未启用',unknown:'保持当前调度'}
const fmt=t=>t&&!String(t).startsWith('0001')?new Date(t).toLocaleString('zh-CN',{timeZone:'Asia/Shanghai',hour12:false}):'尚未核验'
const stale=computed(()=>!state.value?.checked_at||String(state.value.checked_at).startsWith('0001')||clock.value+clockOffset.value-Date.parse(state.value.checked_at)>1000*(Number(state.value?.config?.check_seconds||10)+50))
const unresolved=computed(()=>state.value?.accounts?.filter(a=>a.error||a.observed==null||a.observed!==a.desired)||[])
const pending=computed(()=>state.value?.config?.enabled?(state.value.config.account_ids||[]).filter(id=>!state.value.accounts.some(a=>a.id===id&&!a.old_connection)).length:0)
const cardStatus=computed(()=>{
 if(fetchError.value)return '读取异常'
 if(!state.value)return '读取中…'
 if(state.value.error||state.value.plan.hold)return '部分规则待核验'
 if(stale.value&&(state.value.config.enabled||state.value.accounts.length))return '待核验'
 if(pending.value||unresolved.value.length||state.value.accounts.some(a=>!a.hold&&a.planned!=null&&a.desired!==a.planned))return '切换待确认'
 return {active:'兜底已开启',off:'兜底已关闭',mixed:'按规则兜底中',disabled:'未启用',unknown:'保持当前调度'}[state.value.plan.phase]||'待核验'
})
async function refresh(initial=false,background=false){
 if(disposed||(background&&busy.value))return
 const version=revision
 try{await request.run(signal=>api('/api/pro-restock/fallback',{signal,timeoutMs:15000}),data=>{if(disposed||version!==revision)return;state.value=data;fetchError.value='';clock.value=Date.now();const at=Date.parse(data.server_time);if(Number.isFinite(at))clockOffset.value=at-clock.value;if(initial){setDraft(data.config,data.monitor);dirty.value=false;password.value=''}},{background})}catch(e){if(!disposed&&version===revision)fetchError.value=e.message}
}
async function action(fn){if(busy.value||disposed)return;busy.value=true;revision++;error.value='';notice.value='';try{await fn()}catch(e){if(!disposed)error.value=e.message}finally{busy.value=false}}
function input(preview=false,row=null){return {version:form.version,enabled:preview?false:form.enabled,url:form.url,email:form.email,check_seconds:form.check_seconds,password:password.value,group_id:row?.group_ids[0]||0,group_ids:row?[...row.group_ids]:[],account_ids:row?[...row.account_ids]:[],rules:preview?null:form.rules.map(r=>({enabled:r.enabled,monitor_group_id:Number(r.monitor_group_id)||0,group_ids:[...r.group_ids],account_ids:[...r.account_ids]}))}}
function resetGroup(row){cancelAccounts(row);row._accounts=[];row._loaded=false;row._complete=false;row._error='';row._warnings=[];dirty.value=true}
function resetConnection(){groupRevision++;groups.value=[];groupsLoaded.value=false;groupsError.value='';form.account_names={};for(const r of form.rules){resetGroup(r);r.group_ids=[];r.account_ids=[];r._names={}}password.value='';dirty.value=true}
function addRule(){form.rules.push(draftRule({enabled:true,monitor_group_id:0,group_ids:[],account_ids:[]}));dirty.value=true}
function removeRule(row){cancelAccounts(row);form.rules=form.rules.filter(r=>r!==row);dirty.value=true}
async function loadGroups(force=true){
 if(disposed||(!force&&(groupsLoaded.value||groupsLoading.value||!form.url||!form.email||(!password.value&&!form.password_present))))return
 const version=groupRevision,body=input(true);groupsError.value=''
 try{await groupRequest.run(signal=>api('/api/pro-restock/fallback/groups',{method:'POST',body,signal}),data=>{if(!disposed&&version===groupRevision){groups.value=data;groupsLoaded.value=true}})}catch(e){if(!disposed&&version===groupRevision)groupsError.value=e.message}
}
async function loadMonitorGroups(force=true){
 if(disposed||(!force&&(monitorLoaded.value||monitorLoading.value)))return
 monitorError.value=''
 try{await monitorRequest.run(signal=>api('/api/pro-restock/fallback/monitor-groups',{signal}),data=>{if(!disposed){monitorGroups.value=data;monitorLoaded.value=true}})}catch(e){if(!disposed)monitorError.value=e.message}
}
function loadOptions(force=false){return Promise.all([loadMonitorGroups(force),loadGroups(force)])}
function cancelAccounts(row){const request=accountRequests.get(row._key);if(request){request.controller.abort();clearTimeout(request.timeout);accountRequests.delete(row._key)}row._loading=false}
async function loadAccounts(row){
 if(disposed||busy.value)return
 cancelAccounts(row)
 const request={controller:new AbortController(),timeout:null};accountRequests.set(row._key,request)
 row._loading=true;row._complete=false;row._error='';row._warnings=[]
 request.timeout=setTimeout(()=>{if(accountRequests.get(row._key)!==request)return;cancelAccounts(row);row._error='读取超时，请重试；已选账号仍可取消选择。'},25000)
 try{
  const data=await api('/api/pro-restock/fallback/accounts',{method:'POST',body:input(true,row),signal:request.controller.signal})
  if(disposed||accountRequests.get(row._key)!==request)return
  row._accounts=Array.isArray(data)?data:data.accounts;row._loaded=true;row._complete=Array.isArray(data)||data.complete===true;row._warnings=data.warnings||[]
  for(const a of row._accounts)if(a.name)row._names[a.id]=a.name
 }catch(e){if(!disposed&&accountRequests.get(row._key)===request)row._error=e.message}
 finally{if(accountRequests.get(row._key)===request)cancelAccounts(row)}
}
function accountName(row,id){return row._names[id]||form.account_names?.[id]||(form.url===state.value?.config?.url&&form.email===state.value?.config?.email?state.value?.accounts?.find(a=>Number(a.id)===Number(id)&&!a.old_connection)?.name:'')||'未保存账号名称'}
const unavailable=row=>row.account_ids.filter(id=>!row._accounts.some(a=>Number(a.id)===Number(id)))
function removeUnavailable(row){if(!row._complete)return;const ids=new Set(unavailable(row));row.account_ids=row.account_ids.filter(id=>!ids.has(id));dirty.value=true}
const filtered=row=>row._accounts.filter(a=>(a.name+' '+a.id+' '+a.type).toLowerCase().includes(row._search.toLowerCase()))
function selectVisible(row){row.account_ids=[...new Set([...row.account_ids,...filtered(row).map(a=>a.id)])];dirty.value=true}
function remove(row,id){row.account_ids=row.account_ids.filter(v=>Number(v)!==Number(id));dirty.value=true}
async function save(){await action(async()=>{
 const c=await api('/api/pro-restock/fallback',{method:'PUT',body:input()});if(disposed)return
 setDraft(c,state.value?.monitor);password.value='';dirty.value=false;await refresh();notice.value='兜底规则已保存：每条按自己的监控组没有 OAuth 账号时开启、有 OAuth 账号时关闭；共享账号只要一条规则需要就保持开启。'
})}
async function retry(){await action(async()=>{try{const data=await api('/api/pro-restock/fallback/retry',{method:'POST'});if(!disposed){state.value=data;notice.value='核验完成。'}}finally{await refresh()}})}
async function reload(){await action(async()=>{await refresh(true)});if(!disposed&&props.settings)await loadOptions(true)}
watch(()=>props.settings,visible=>{if(visible&&state.value)loadOptions(true)})
onMounted(async()=>{await refresh(true);if(disposed)return;if(props.settings)loadOptions();timer=setInterval(()=>{clock.value=Date.now();if(!document.hidden)void refresh(false,true)},5000)})
onUnmounted(()=>{disposed=true;revision++;for(const r of form.rules)cancelAccounts(r);clearInterval(timer);request.dispose();groupRequest.dispose();monitorRequest.dispose()})
</script>
<template>
<article v-if="compact" class="fallback-card" aria-label="兜底调度"><span>兜底调度</span><strong>{{cardStatus}}</strong><p>{{state?.config?.rules?.length||0}} 条规则 · 已选 {{state?.config?.account_ids?.length||0}} 个账号（去重）</p><p>{{fetchError||state?.error||state?.plan?.reason||'正在读取状态'}}</p><button type="button" aria-haspopup="dialog" @click="detailDialog?.showModal()">查看兜底详情</button></article>
<component :is="compact?'dialog':'div'" ref="detailDialog" :class="{'fallback-dialog':compact}" :aria-label="compact?'兜底调度详情':undefined" @click="compact&&$event.target===detailDialog&&detailDialog.close()">
<article class="fallback-panel" aria-label="兜底调度">
 <button v-if="compact" type="button" class="close-detail" aria-label="关闭兜底详情" @click="detailDialog.close()">✕</button>
 <header><div><h2>兜底调度</h2><p>每条规则独立监控：组内没有 OAuth 账号时开启兜底，有 OAuth 账号时关闭；API Key 不计入，查询失败保持原状态</p></div><span class="badge">{{state?phases[state.plan.phase]:'读取状态中…'}}</span></header>
 <p v-if="error||fetchError" class="alert error" role="alert">{{error||fetchError}}</p><p v-if="notice" class="alert" role="status">{{notice}}</p>
 <template v-if="state">
  <p>{{state.plan.reason}}</p>
  <div class="rule-statuses"><div v-for="(row,index) in state.rules" :key="index" class="rule-status" :class="{warn:row.plan.hold}"><b>规则 {{index+1}} · {{row.monitor.group_name||'监控分组'}} #{{row.rule.monitor_group_id||'未选择'}}</b><span>{{phases[row.plan.phase]}}</span><p>监控组 OAuth 账号：{{row.monitor.count??'未知'}} · 兜底分组 {{row.rule.group_ids.length}} 个 · 所选账号 {{row.rule.account_ids.length}} 个</p><small>{{row.monitor.error||row.plan.reason}} · {{fmt(row.monitor.at)}}</small></div></div>
  <p v-if="state.error" class="alert error" role="alert">{{state.error}}</p>
  <p v-if="stale&&state.config.enabled" class="alert warn">尚未完成近期核验，上方计划不能视为账号已完成切换。</p>
  <p v-if="pending" class="alert warn">{{pending}} 个所选账号等待首次核验。</p>
  <p v-if="unresolved.length" class="alert warn">{{unresolved.length}} 个账号尚未确认目标状态或存在异常，后台继续核验。</p>
  <details v-if="state.accounts.length"><summary>逐账号调度状态（{{state.accounts.length}}）</summary><div class="table-wrap"><table><thead><tr><th>账号</th><th>目标</th><th>最近实际状态</th><th>核验时间</th><th>说明</th></tr></thead><tbody><tr v-for="(a,index) in state.accounts" :key="index"><td>{{a.name||'账号'}} #{{a.id}}<small v-if="a.old_connection">旧连接收尾</small></td><td>{{a.hold?'保持':a.desired?'开启':'关闭'}}</td><td>{{a.observed==null?'未知':a.observed?'开启':'关闭'}}</td><td>{{fmt(a.checked_at)}}</td><td>{{a.error||'已确认'}}</td></tr></tbody></table></div></details>
  <div class="toolbar"><button type="button" :disabled="busy" @click="retry">立即核验／重试</button><span>以已保存的规则为准</span></div>
  <form v-if="settings" @submit.prevent="save" @input="dirty=true">
   <fieldset :disabled="busy">
   <label class="check"><input v-model="form.enabled" type="checkbox">启用兜底调度</label>
   <p>监控分组使用 Pro 推送的主 Sub2 连接，可独立于自动补货的监控组选择。所有规则共用下面这一套兜底连接。与补货总开关、观察模式、禁补时段独立。</p>
   <div class="fields">
    <label>兜底 Sub2 地址<input v-model.trim="form.url" type="url" placeholder="https://sub2.example.com" :required="form.enabled" @change="resetConnection"></label>
    <label>管理员邮箱<input v-model.trim="form.email" type="email" autocomplete="off" :required="form.enabled" @change="resetConnection"></label>
    <label>管理员密码<input v-model="password" type="password" autocomplete="new-password" :placeholder="form.password_present?'已加密保存，留空保留':'请输入管理员密码'"></label>
    <label>兜底检查／失败重试周期（秒）<input v-model.number="form.check_seconds" type="number" min="2" max="300" step="1" required></label>
   </div>
   <div class="toolbar"><button type="button" :disabled="monitorLoading" @click="loadMonitorGroups()">{{monitorLoading?'正在读取监控分组…':'刷新监控分组'}}</button><button type="button" :disabled="groupsLoading" @click="loadGroups()">{{groupsLoading?'正在读取兜底分组…':'连接检查／刷新兜底分组'}}</button><span v-if="monitorLoaded">监控分组 {{monitorGroups.length}} 个</span></div>
   <p v-if="monitorError" class="alert error" role="alert">监控分组读取失败：{{monitorError}}。当前保留已选分组，请点击“刷新监控分组”重试。</p>
   <p v-if="groupsError" class="alert error" role="alert">兜底分组读取失败：{{groupsError}}。请检查连接后重试。</p>
   <section v-for="(row,index) in form.rules" :key="row._key" class="rule-editor" :aria-label="'兜底规则 '+(index+1)">
    <header><b>规则 {{index+1}}</b><div class="toolbar"><label class="check"><input v-model="row.enabled" type="checkbox">启用本规则</label><button type="button" @click="removeRule(row)">删除规则</button></div></header>
    <label>监控分组<select v-model.number="row.monitor_group_id" :required="form.enabled&&row.enabled" @focus="loadMonitorGroups(false)"><option :value="0" disabled>{{monitorLoading?'正在读取监控分组…':'请选择一个监控分组'}}</option><option v-for="g in monitorOptions" :key="g.id" :value="g.id">{{g.name}} · #{{g.id}}</option></select></label>
    <div class="group-selection"><span>兜底分组 · 已选 {{row.group_ids.length}} 个</span><div class="group-list"><label v-for="g in groupOptions" :key="g.id" class="check"><input v-model="row.group_ids" type="checkbox" :value="g.id" @change="resetGroup(row)">{{g.name}} · #{{g.id}}</label><p v-if="!groupOptions.length">{{groupsLoading?'正在读取兜底分组…':'请连接读取兜底分组，再勾选一个或多个。'}}</p></div></div>
    <div class="toolbar"><button type="button" :disabled="!row.group_ids.length||row._loading" @click="loadAccounts(row)">{{row._loading?'正在读取账号…':'读取／刷新本规则账号'}}</button><button v-if="row._loading" type="button" @click="cancelAccounts(row)">停止读取</button><b>已选 {{row.account_ids.length}} 个账号</b><button type="button" :disabled="!row._loaded" @click="selectVisible(row)">选中搜索结果</button><button type="button" @click="row.account_ids=[];dirty=true">清空账号</button></div>
    <p v-if="row._error" class="alert error" role="alert">{{row._error}} 已保留选择，可继续取消账号或重试。</p>
    <p v-for="warning in row._warnings" :key="warning" class="alert warn">{{warning}}</p>
    <p v-if="row._loaded">已读取 {{row._accounts.length}} 个账号{{row._complete?'':'（部分分组读取失败，未出现的账号暂不能判定失效）'}}，读取不会切换调度。</p>
    <label>搜索兜底账号<input v-model="row._search" type="search" placeholder="账号名称、ID 或类型"></label>
    <div class="account-list"><label v-for="a in filtered(row)" :key="a.id" class="account check"><input v-model="row.account_ids" type="checkbox" :value="a.id"><span><b>{{a.name||'未命名'}} #{{a.id}}</b><small>{{a.type==='apikey'?'API Key':'OAuth'}} · {{a.status}} · 调度{{a.schedulable?'开启':'关闭'}}</small></span></label></div>
    <p v-if="!filtered(row).length">{{row._loaded?'没有匹配账号':'读取账号列表后勾选；已保存的选择仍保留。'}}</p>
    <p v-for="id in unavailable(row)" :key="id"><b>{{accountName(row,id)}}</b> · #{{id}} · {{row._complete?'已删除或已移出所选分组':row._loaded?'本次未读取到，保留原选择':'已保存的选择'}} <button type="button" @click="remove(row,id)">取消选择</button></p>
    <button v-if="row._complete&&unavailable(row).length" type="button" @click="removeUnavailable(row)">取消选择全部失效账号</button>
   </section>
   <div class="toolbar"><button type="button" :disabled="form.rules.length>=50" @click="addRule">＋ 添加兜底规则</button><span>{{form.rules.length}} 条规则 · 启用规则共选 {{allIDs.length}} 个账号（去重）</span></div>
   <p>共享账号只要有一条规则需要就保持开启；某条监控读取失败时保留相关账号的调度，其他规则继续运行。禁补高并发统计会合并各启用规则所选的兜底账号并去重。</p>
   <p>默认每 3 秒核验，与补货并发检查周期分开。组内存在禁用或无额度账号仍算有账号。停用、删除规则或取消账号后，不再被其他启用规则需要的账号会关闭调度。</p>
   <p v-if="error" class="alert error" role="alert">{{error}}</p>
   <footer><span>{{dirty?'有未保存的兜底配置，请保存后生效':'兜底配置单独保存'}} · v{{form.version}}</span><div class="toolbar"><button type="button" @click="reload">重新载入</button><button class="primary" type="submit">{{busy?'处理中…':'保存兜底配置'}}</button></div></footer>
   </fieldset>
  </form>
 </template>
</article>
</component>
</template>
<style scoped>
.rule-editor{border:1px solid var(--border,#dce7e9);border-radius:9px;padding:16px;margin:18px 0}.rule-editor header{border-bottom:1px solid var(--border,#dce7e9)}.rule-editor header .toolbar{margin:0}.rule-statuses{display:grid;grid-template-columns:repeat(auto-fit,minmax(250px,1fr));gap:10px}.rule-status{border:1px solid var(--border,#dce7e9);border-radius:7px;padding:12px}.rule-status>span{display:block;margin-top:6px;color:var(--teal,#008786)}

.fallback-card{height:100%;box-sizing:border-box;background:#fff;border:1px solid var(--border,#dce7e9);border-radius:11px;padding:21px;min-width:0;color:var(--ink,#234752)}.fallback-card>span{font-size:12px;color:var(--muted,#708a94)}.fallback-card>strong{display:block;font-size:22px;font-weight:550;margin:21px 0 5px;overflow-wrap:anywhere}.fallback-card p{font-size:11px;display:-webkit-box;-webkit-line-clamp:2;-webkit-box-orient:vertical;overflow:hidden}.fallback-dialog{border:0;border-radius:13px;padding:0;width:min(850px,calc(100vw - 32px));max-height:85vh;color:var(--ink,#234752);background:#fff;box-shadow:0 24px 100px #12383d33}.fallback-dialog::backdrop{background:#102e3c77;backdrop-filter:blur(3px)}.fallback-dialog .fallback-panel{margin:0;position:relative;padding-top:50px}.close-detail{position:absolute;right:15px;top:12px}:global([data-theme=dark]) .fallback-card,:global([data-theme=dark]) .fallback-dialog{background:#192e38}@media(max-width:600px){.fallback-card{padding:15px}.fallback-card>strong{font-size:18px}}
.group-selection{margin:12px 0;font-size:12px}.group-list{max-height:180px;overflow:auto;border:1px solid var(--border,#dce7e9);border-radius:6px;padding:0 12px;margin-top:8px}
fieldset{border:0;padding:0;margin:0;min-width:0}
.fallback-panel{background:#fff;border:1px solid var(--border,#dce7e9);border-radius:11px;padding:21px;margin-bottom:18px;color:var(--ink,#234752);font-size:13px}header,footer,.toolbar{display:flex;align-items:center;justify-content:space-between;gap:12px;flex-wrap:wrap}h2{font-size:15px;margin:0 0 8px}p{color:var(--muted,#708a94);line-height:1.8;margin:8px 0}button,input,select{font:inherit;border:1px solid var(--border,#dce7e9);border-radius:6px;background:white;color:inherit;padding:9px 12px}button{cursor:pointer}button:disabled{opacity:.5;cursor:default}.primary{background:#008786;color:white;border-color:#008786}.badge,.alert{background:#edf7f5;color:#31736e;border-radius:6px;padding:9px 12px}.warn{background:#fff8ec;color:#976f31}.error{background:#fff1f0;color:#a45148}.fields{display:grid;grid-template-columns:1fr 1fr;gap:16px 20px}label{display:block;margin:12px 0;font-size:12px}input:not([type=checkbox]),select{display:block;width:100%;box-sizing:border-box;min-width:0;margin-top:8px}.check{display:flex;gap:10px;align-items:center}.check input{accent-color:#008786;width:16px;height:16px;flex-shrink:0}.toolbar{justify-content:flex-start;margin:12px 0}.toolbar span,footer>span{color:var(--muted,#708a94);font-size:12px}.selection{border:1px solid var(--border,#dce7e9);padding:14px;border-radius:8px;margin:18px 0}.account-list{max-height:320px;overflow:auto;display:grid;grid-template-columns:1fr 1fr;gap:8px}.account{padding:10px;border:1px solid var(--border,#dce7e9);border-radius:6px;margin:0;overflow-wrap:anywhere;min-width:0}.account span{min-width:0}small{display:block;color:var(--muted,#708a94);margin-top:6px}footer{border-top:1px solid var(--border,#dce7e9);padding-top:12px}summary{cursor:pointer;padding:8px 0}.table-wrap{overflow:auto}table{width:100%;border-collapse:collapse;min-width:620px}th,td{padding:10px;border-bottom:1px solid var(--border,#dce7e9);text-align:left;max-width:300px;overflow-wrap:anywhere}th{font-size:12px}:focus-visible{outline:2px solid #008786;outline-offset:2px}:global([data-theme=dark]) .fallback-panel,:global([data-theme=dark]) .fallback-panel :is(button,input,select){background:#192e38}:global([data-theme=dark]) .fallback-panel .primary{background:#007b78}:global([data-theme=dark]) .fallback-panel :is(.badge,.alert){background:#193b38;color:#b4e4d9}:global([data-theme=dark]) .fallback-panel .warn{background:#3c3222;color:#efd3a1}:global([data-theme=dark]) .fallback-panel .error{background:#412a2b;color:#f3b4af}@media(max-width:650px){.fields,.account-list{grid-template-columns:1fr}.fallback-panel{padding:15px}}
</style>
