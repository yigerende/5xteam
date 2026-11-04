import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import vm from 'node:vm'
import {ref,shallowRef,reactive,computed,watch} from 'vue'
import * as ejh from '../src/ejh.js'
import {applyPaymentPolicies} from '../src/payment-only.js'
import {applyRefundFeeOverrides} from '../src/ejh-refund-cost.js'
import {cardOrigin} from '../src/card-platform.js'
import {createLatestRequest} from '../src/latestRequest.js'
import {createEJHRefreshGate} from '../src/ejhRefresh.js'
import {refreshEJHSync} from '../src/ejhSync.js'
import {activationPlan} from '../src/proActivationPlan.js'
const paid = (id, plan, created_at, payment_status = 'success') => ({ id, plan, created_at, payment_status })
const account = { current_plan_type:'free', financial_cases:[paid('old','pro5','2026-10-01'),paid('paid','pro20','2026-10-02'),paid('retry','pro50','2026-10-03','failed'),paid('points','codex_point','2026-10-04')] }
assert.equal(activationPlan(account).label,'Pro 20x')
assert.equal(activationPlan({...account,plan_code:'pro50',current_plan_type:'plus'}).label,'Pro 20x')
assert.equal(activationPlan({current_plan_type:'pro',financial_cases:[]}).label,'未开通')
assert.equal(activationPlan({financial_cases:[paid('missing','','2026-10-01')]}).label,'套餐未记录')
assert.equal(activationPlan({financial_cases:[paid('cmsnav','pro5x','2026-10-01')]}).label,'Pro 5x')


const code=readFileSync(new URL('../src/components/EJHManagementView.vue',import.meta.url),'utf8').split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
const tick=()=>new Promise(resolve=>setImmediate(resolve))
const storage={getItem:()=>null,setItem(){}}
const gate=createEJHRefreshGate({storage:()=>storage})
const plain=v=>JSON.parse(JSON.stringify(v))
const card=id=>({id,issuer_linked:true,remote:{orderNo:id,status:'1'},local:{}})
function fixture({enabled=true,defaultPageSize=10,cards=[],cases=[],failSync=false,holdSync=false,holdSettings=false,holdView=false,failView=false,failURLs=new Set(),confirmDelete=true}={}){
 const calls=[],unmounts=[],confirmations=[],timers=[],workers=[]
 let release,percent=6,failSave=false,revision='1'
 const blocked=new Promise(resolve=>{release=resolve})
 const source=()=>({cards,cases,trades:[],operations:[],audit:[],sync:{},reversal_fee_percent:percent,revision})
 const document={hidden:false,addEventListener(){},removeEventListener(){}}
 const context=vm.createContext({ref,shallowRef,reactive,computed,watch,URLSearchParams,AbortController,...ejh,applyPaymentPolicies,applyRefundFeeOverrides,cardOrigin,createLatestRequest,refreshEJHSync,ejhRefreshGate:gate,
  createEJHOverviewWorker:()=>({calculate:async s=>{workers.push(s);return {fullHistoryCount:s.cases.length}},dispose(){}}),
  nextTick:tick,onMounted(){},onBeforeUnmount:fn=>unmounts.push(fn),defineProps:()=>({defaultPageSize}),defineEmits:()=>()=>{},document,
  window:{localStorage:storage,addEventListener(){},removeEventListener(){},confirm:t=>{confirmations.push(t);return confirmDelete}},
  setInterval:()=>0,clearInterval(){},setTimeout:(fn,ms)=>{timers.push({fn,ms});return timers.length},clearTimeout(){},CreditCard:{},CircleDashed:{},Clock3:{},CheckCircle2:{},isClosedCard:c=>c.remote?.status==='2',
  async api(url,options={}){
   calls.push({url,options});if(failURLs.has(url))throw Error('query failed')
   if(url==='/api/ejh/settings'){
    if(holdSettings)await blocked
    if(options.method==='PUT'){if(failSave)throw Error('save failed');percent=options.body.reversal_fee_percent}
    return {enabled,reversal_fee_percent:percent,page_refresh_seconds:300,templates:[]}
   }
   if(url==='/api/ejh/revision')return {revision}
   if(url==='/api/ejh/overview')return source()
   if(url.startsWith('/api/ejh/view?')){
    const p=new URLSearchParams(url.split('?')[1]),section=p.get('section')
    if(holdView)await blocked
    if(failView)throw Error('read timed out')
    if(section==='balance_targets')return {cards:cards.filter(c=>!c.normal_pending&&!['2','8'].includes(c.remote.status))}
    if(section==='pending_targets')return {cards:cards.filter(c=>c.normal_pending)}
    let rows=section==='cases'?cases.filter(c=>p.get('category')==='unopened'?c.state==='unopened':c.state!=='unopened'):section==='cards'?cards.filter(c=>p.get('card_id')?c.id===p.get('card_id'):ejh.cardLifecycle(c)===(p.get('lifecycle')||'normal')):section==='card'?cards.filter(c=>c.id===p.get('card_id')):[]
    if(section==='cards')rows=rows.filter(c=>ejh.cardMatchesFilters(c,cases,p.get('card_state'),p.get('return_state'))&&ejh.cardMatchesSearch(c,cases,p.get('q'),p.get('card_id')))
    else if(p.get('q'))rows=rows.filter(c=>JSON.stringify(c).includes(p.get('q')))
    if(section==='cases'&&p.get('status'))rows=rows.filter(c=>c.state===p.get('status'))
    const size=Number(p.get('page_size'))||10,page=Math.min(Number(p.get('page'))||1,Math.max(1,Math.ceil(rows.length/size)))
    return {...source(),items:rows.slice((page-1)*size,page*size),page,total:rows.length,page_size:size,counts:Object.fromEntries(Object.keys(ejh.returnStates).map(k=>[k,cases.filter(c=>c.state===k).length])),card_total:cards.length,lifecycle_counts:{normal:cards.filter(c=>ejh.cardLifecycle(c)==='normal').length,closed:cards.filter(c=>ejh.cardLifecycle(c)==='closed').length},case_counts:{normal:cases.filter(c=>c.state!=='unopened').length,unopened:cases.filter(c=>c.state==='unopened').length}}
   }
   if(options.method==='DELETE'){
    const removed=cases.filter(c=>c.state==='unopened'&&(url.endsWith('/unopened')||url.endsWith('/'+c.id)));cases=cases.filter(c=>!removed.includes(c));revision+='d'
    return {deleted:removed.length,deleted_ids:removed.map(c=>c.id)}
   }
   if(url==='/api/ejh/sync'){if(holdSync)await blocked;if(failSync)throw Error('sync failed');return {synced:true}}
   if(url.endsWith('/action'))return {status:'completed'}
   if(url.startsWith('/api/ejh/operations'))return {items:[]}
   return {}
  }
 })
 vm.runInContext(code+';globalThis.exposed={retryFailedRefresh,progress,refreshProgress,refreshing,initialize,refreshPage,refreshAllBalances,busy,message,balanceFailures,editLimit,saveLimit,data,limitID,limitValue,lifecycleFilter,filtered,setLifecycle,clearFilters,cardStatusFilter,targetCardID,changeTab,showCaseState,tab,status,query,accountReturnStates,caseCategory,caseCounts,setCaseCategory,deleteUnopenedCase,page,pageSize,visible,settings,save,load,ready,loading,initializing,initialError,pollSavedData,overview,loadOverview,readError,refreshSaved,cardAction,cardReading,detail};',context)
 return {...context.exposed,calls,workers,timers,document,confirmations,release,setHoldView:v=>holdView=v,setFailView:v=>failView=v,setRevision:v=>revision=v,setFailSync:v=>failSync=v,setFailSave:v=>failSave=v,unmount:()=>unmounts.forEach(fn=>fn())}
}
let f=fixture();await f.initialize();await tick()
assert.equal(f.calls.filter(c=>c.options.method).length,0,'entry must not invoke upstream')
assert.equal(f.calls.filter(c=>c.url.includes('/dashboard')).length,0,'legacy full-list poll remains')
assert.ok(f.timers.some(t=>t.ms===300000),'configured display interval ignored')
assert.equal(f.busy.value,false)
let before=f.calls.length;await f.pollSavedData()
assert.deepEqual(f.calls.slice(before).map(c=>c.url),['/api/ejh/revision'],'unchanged data reloaded')
f.document.hidden=true;before=f.calls.length;await f.pollSavedData();assert.equal(f.calls.length,before)
f.document.hidden=false;f.setRevision('2');await f.pollSavedData();await tick()
assert.ok(f.calls.slice(before).some(c=>c.url.startsWith('/api/ejh/view?')))
f.unmount()
assert.equal(fixture({defaultPageSize:50}).pageSize.value,50)
console.log('Passed: saved-data entry, configured revision polling, no 5-second full reload, hidden-page pause')

f=fixture({holdView:true,cards:[card('old')]});let pending=f.initialize();await tick()
const reads=Array.from({length:20},()=>f.load({background:true}));await tick()
assert.equal(f.calls.filter(c=>c.url.startsWith('/api/ejh/view?')).length,1,'read storm')
let signal=f.calls.find(c=>c.url.startsWith('/api/ejh/view?')).options.signal
f.unmount();assert.equal(signal.aborted,true);f.release();await pending;await Promise.all(reads)
assert.equal(f.data.value.cards.length,0,'unmounted response applied')
f=fixture({holdView:true,cards:[card('old')]});pending=f.initialize();await tick()
f.changeTab('cases');const newer=f.load();await tick()
assert.equal(f.visible.value.length,0,'old section rendered as new rows')
f.release();await pending;await newer;assert.equal(f.visible.value.length,0);f.unmount()
f=fixture({failView:true});await f.initialize();assert.match(f.initialError.value,/timed out/);assert.equal(f.ready.value,false);assert.equal(f.busy.value,false);f.unmount()
f=fixture({holdSettings:true,cards:[card('first')]});pending=f.initialize();await tick();assert.equal(f.visible.value.length,1,'settings hid current rows');f.release();await pending;f.unmount()
console.log('Passed: repeated-read coalescing, navigation/unmount cancellation, stale response guard, independent settings')

f=fixture({cards:[card('normal'),{...card('closed'),remote:{status:'2'}},{...card('pending'),normal_pending:true}]});await f.initialize()
assert.deepEqual(Array.from(f.visible.value,c=>c.id),['normal']);f.setLifecycle('closed');await f.load();assert.deepEqual(Array.from(f.visible.value,c=>c.id),['closed'])
f.cardStatusFilter.value='normal_pending';assert.equal(f.lifecycleFilter.value,'normal_pending');await f.load();assert.equal(f.visible.value[0].id,'pending')
f.clearFilters();f.targetCardID.value='closed';await f.load();assert.equal(f.visible.value[0].id,'closed');f.setLifecycle('normal');assert.equal(f.targetCardID.value,'');f.unmount()
const unopened=Array.from({length:12},(_,i)=>({id:'u'+i,email:'test'+i+'@example.com',state:'unopened'})),normal={id:'normal',email:'normal@example.com',state:'opened'}
f=fixture({cases:[...unopened,normal]});await f.initialize();await tick();assert.equal(f.overview.value._overview.fullHistoryCount,13,'overview paginated')
f.showCaseState('unopened');f.query.value='test11@';await f.load();assert.equal(f.visible.value.length,1);assert.equal(f.caseCounts.value.unopened,12)
await f.deleteUnopenedCase();await tick();assert.match(f.confirmations[0],/全部 12 条.*其他分页和搜索范围外/);assert.match(f.message.text,/已删除 12 条/);assert.equal(f.caseCounts.value.unopened,0);assert.equal(f.caseCounts.value.normal,1);f.unmount()
f=fixture({cases:unopened.slice(0,11)});await f.initialize();f.showCaseState('unopened');f.page.value=2;await f.load();await f.deleteUnopenedCase(f.visible.value[0]);await tick();assert.equal(f.page.value,1);assert.equal(f.caseCounts.value.unopened,10);f.unmount()
f=fixture({cases:unopened,confirmDelete:false});await f.initialize();await f.deleteUnopenedCase();assert.equal(f.calls.filter(c=>c.options.method==='DELETE').length,0);f.unmount()
console.log('Passed: section/filter navigation, full-scope overview, delete across pages, clamp and confirmation')

f=fixture({cards:[card('a')]});await f.initialize();f.settings.reversal_fee_percent=8;await f.load();assert.equal(f.settings.reversal_fee_percent,8);assert.equal(f.data.value.reversal_fee_percent,6)
f.setFailSave(true);await f.save();assert.equal(f.data.value.reversal_fee_percent,6)
f.setFailSave(false);await f.save();await tick();assert.equal(f.data.value.reversal_fee_percent,8)
f.settings.reversal_fee_percent=0;await f.save();await tick();assert.equal(f.data.value.reversal_fee_percent,0)
f.editLimit(f.data.value.cards[0]);f.limitValue.value='';before=f.calls.length;await f.saveLimit();assert.equal(f.calls.length,before)
f.limitValue.value=4;f.setHoldView(true);await f.saveLimit();assert.equal(f.busy.value,false,'successful action waits for follow-up read');assert.equal(f.limitID.value,'');assert.match(f.message.text,/已保存/)
f.release();await tick();f.unmount()
f=fixture({cards:[card('a')]});await f.initialize();f.setFailView(true);f.editLimit(f.data.value.cards[0]);f.limitValue.value=4;await f.saveLimit();await tick();assert.match(f.message.text,/已保存/);assert.match(f.readError.value,/页面更新失败/);f.unmount()
console.log('Passed: settings drafts/zero fee, immediate success, separate follow-up failure')

f=fixture({holdSync:true,cards:[card('a')]});await f.initialize();pending=f.refreshPage();await tick();assert.equal(f.refreshing.value,true);assert.equal(f.busy.value,false,'refresh disabled all writes/views')
await f.refreshPage();assert.equal(f.calls.filter(c=>c.url==='/api/ejh/sync').length,1,'double click duplicated sync')
f.unmount();f.release();await pending;assert.equal(f.calls.filter(c=>c.url==='/api/ejh/balance').length,0,'unmount started further requests')
f=fixture({enabled:false});await f.initialize();await f.refreshPage(true);assert.equal(f.calls.filter(c=>c.options.method).length,0);await f.refreshPage();assert.equal(f.calls.filter(c=>c.url==='/api/ejh/sync').length,1);f.unmount()
f=fixture({failSync:true});await f.initialize();await f.refreshPage();assert.equal(f.balanceFailures.value.length,1);before=f.calls.length;await f.retryFailedRefresh();assert.equal(f.balanceFailures.value.length,1);assert.deepEqual(f.calls.slice(before).filter(c=>c.options.method).map(c=>c.url),['/api/ejh/sync']);f.setFailSync(false);await f.retryFailedRefresh();assert.equal(f.balanceFailures.value.length,0);f.unmount()
const failURLs=new Set(['/api/ejh/cards/b/action'])
f=fixture({cards:['a','b','c'].map(card),failURLs});await f.initialize();await f.refreshPage();assert.equal(f.balanceFailures.value.length,1);failURLs.clear();before=f.calls.length;await f.retryFailedRefresh();assert.deepEqual(f.calls.slice(before).filter(c=>c.options.method).map(c=>c.url),['/api/ejh/cards/b/action']);f.unmount()
f=fixture({failSync:true,failURLs:new Set(['/api/ejh/balance'])});await f.initialize();await f.refreshPage();f.setFailSync(false);f.calls.length=0;pending=f.retryFailedRefresh();f.refreshProgress.value.stopRequested=true;await f.retryFailedRefresh();await pending;assert.equal(f.calls.filter(c=>c.options.method).length,1);assert.equal(f.balanceFailures.value.length,1);f.unmount()
f=fixture({cards:[card('a')]});await f.initialize();f.busy.value=true;await f.cardAction(card('a'),'details');assert.ok(f.detail.value,'write busy blocked pure viewing');f.unmount()
console.log('Passed: local refresh scope, no duplicate sync, stop/unmount guards, manual disabled-issuer refresh and failed-only retry')
