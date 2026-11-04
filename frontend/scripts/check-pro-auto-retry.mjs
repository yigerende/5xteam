import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { computed, ref } from 'vue'
const source=readFileSync(new URL('../src/components/ProAutoDialog.vue',import.meta.url),'utf8')
const code=source.split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
function fixture(){
 const calls=[], timers=new Map(), replies=new Map()
 let fail=false,resolve,hold=false
 const ctx=vm.createContext({computed,ref,defineProps:()=>({defaultPageSize:10}),defineEmits:()=>()=>{},defineExpose(){},onBeforeUnmount(){},clearTimeout:id=>timers.delete(id),setTimeout:fn=>{timers.set(1,fn);return 1},window:{confirm:()=>true},async api(path,options){
  calls.push({path,options})
  if(options?.method==='POST'){
   if(hold)await new Promise(r=>resolve=r)
   if(fail)throw Error('订单待确认')
   return {pro_auto:{id:'new-flow',status:'running',stage:'login',steps:{login:'pending'}}}
  }
  if(replies.has(path))return replies.get(path)
  return {id:'old-flow',order_id:'original',payment_status:'failed',status:'failed'}
 }})
 vm.runInContext(code+';globalThis.f={refresh,active,retryStep,start,canStart,retryCurrent,restartable,resumable,state,settings,cardID,email,busy,error};',ctx)
 return {...ctx.f,calls,replies,fail:()=>{fail=true},hold:()=>{hold=true},release:()=>resolve()}
}
for(const state of [{id:'old',status:'failed',steps:{login:'failed'}},{id:'old',status:'failed',order_id:'order',payment_status:'failed'},{id:'old',status:'stopped',order_id:'order',payment_status:'failed'}]){
 const f=fixture();f.state.value=state
 assert.equal(f.retryCurrent.value,true);assert.equal(f.restartable.value,true);assert.equal(f.canStart.value,false)
 f.settings.value={provider:'cmsnav',plan_code:'pro50',country:'PH',credit_quantity:0};f.cardID.value='selected-card';f.email.value='retry@example.com'
 f.hold();const first=f.retryStep();assert.equal(f.busy.value,true);await f.retryStep();assert.equal(f.calls.length,1)
 f.release();await first
 assert.deepEqual(JSON.parse(JSON.stringify(f.calls[0].options.body)),{retry_current:true,provider:'cmsnav',card_id:'selected-card',plan_code:'pro50',country:'PH',credit_quantity:0})
 assert.equal(f.state.value.stage,'login');assert.equal(f.state.value.status,'running');assert.equal(f.retryCurrent.value,false)
}
for(const payment_status of ['success','manual_review','processing','submission_unknown']){
 const f=fixture();f.state.value={id:'old',status:'failed',order_id:'original',payment_status}
 assert.equal(f.retryCurrent.value,true);assert.equal(f.restartable.value,false)
}
for(const status of ['running','waiting_quota','awaiting_merchant','completed']){
 const f=fixture();f.state.value={id:'old',status,order_id:'original'};assert.equal(f.retryCurrent.value,false)
}
for(const status of ['failed','stopped','awaiting_merchant','completed']){
 const f=fixture();f.state.value={id:'merchant-flow',status,pro_merchant:{request_id:'existing'},steps:{oauth:'completed'}}
 assert.equal(f.canStart.value,false);assert.equal(f.retryCurrent.value,false);assert.equal(f.resumable.value,false)
}
const resumed=fixture();resumed.state.value={id:'old',status:'failed',steps:{oauth:'completed'}}
assert.equal(resumed.resumable.value,true);assert.equal(resumed.retryCurrent.value,false);assert.equal(resumed.canStart.value,false)
const fresh=fixture();assert.equal(fresh.canStart.value,true);assert.equal(fresh.retryCurrent.value,false)
const failed=fixture();failed.fail();await failed.retryStep();assert.match(failed.error.value,/待确认/);assert.equal(failed.state.value.payment_status,'failed');assert.equal(failed.busy.value,false)
console.log('Passed: failed/pre-order retries from login, current settings/card payload, success recovery, ambiguous status, duplicate clicks, resume-only and failure refresh')

const replaced=fixture();replaced.email.value='old@example.com';replaced.state.value={id:'old',stage:'recharge',status:'failed',replacement:{pending:true,limit:2,attempts:0}}
assert.equal(replaced.active.value,true);assert.equal(replaced.retryCurrent.value,false)
replaced.replies.set('/api/pro-accounts/old%40example.com/auto-pro',{status:'failed',replacement:{next_email:'new@example.com'}})
replaced.replies.set('/api/pro-accounts/new%40example.com/auto-pro',{id:'new',stage:'login',status:'running',replacement:{attempts:1,limit:2}})
await replaced.refresh();assert.equal(replaced.email.value,'new@example.com');assert.equal(replaced.state.value.id,'new')
const exhausted=fixture();exhausted.state.value={id:'old',stage:'recharge',status:'failed',replacement:{limit:0,attempts:0}}
assert.equal(exhausted.retryCurrent.value,false);assert.equal(exhausted.canStart.value,false)
console.log('Passed: replacement progress follows successor and exhausted step two cannot restart the original account')

for(const original of [
 {status:'stopped',stop_requested:true},
 {status:'failed',payment_status:'success',quota_resume_available:true},
 {status:'waiting_quota',stage:'quota'},
]){
 const f=fixture();f.email.value='paid@example.com'
 const state={id:'paid-flow',...original,replacement:{next_email:'removed@example.com'}}
 f.state.value=state
 f.replies.set('/api/pro-accounts/paid%40example.com/auto-pro',state)
 await f.refresh()
 assert.equal(f.calls.length,1);assert.equal(f.email.value,'paid@example.com');assert.equal(f.state.value.id,'paid-flow')
 assert.equal(f.active.value,original.status==='waiting_quota')
}
console.log('Passed: stopped and paid quota recovery stays on the original account instead of a historical successor')
