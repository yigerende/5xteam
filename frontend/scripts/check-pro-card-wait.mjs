import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import vm from 'node:vm'
import {ref,computed} from 'vue'

const code=readFileSync(new URL('../src/components/ProAutoDialog.vue',import.meta.url),'utf8').split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
const guard={flow_id:'',order_id:'',status:'',stop_version:0,stop_requested:false}
const busyError=()=>Object.assign(Error('EJH 正在同步'),{status:409,cardPreparationRetry:guard})
const settle=()=>new Promise(resolve=>setImmediate(resolve))
function fixture(replies,current={}){
 const calls=[],timers=new Map();let id=0,unmount
 const context=vm.createContext({ref,computed,defineProps:()=>({}),defineEmits:()=>()=>{},defineExpose(){},onBeforeUnmount:fn=>unmount=fn,window:{confirm:()=>true},clearTimeout:key=>timers.delete(key),setTimeout:(fn,ms)=>{timers.set(++id,{fn,ms});return id},async api(path,options){
  calls.push({path,options})
  if(options?.method!=='POST')return current
  const reply=replies.shift()
  if(reply instanceof Error)throw reply
  assert.ok(reply,'unexpected extra write')
  return reply
 }})
 vm.runInContext(code+';globalThis.f={start,retryStep,close,state,settings,email,cardID,opened,busy,error,cardWaitMessage};',context)
 const f=context.f;f.email.value='same@example.com';f.opened.value=true;f.settings.value={provider:'cmsnav',card_provider:'ejh',plan_code:'pro5',country:'PH'}
 return {...f,calls,timers,unmount:()=>unmount(),advance:async()=>{const entry=[...timers].find(([,v])=>v.ms===3000);assert.ok(entry,'no pending card wait');timers.delete(entry[0]);entry[1].fn();await settle()}}
}
const accepted={pro_auto:{id:'new',status:'running',stage:'login'}}
for(const retry of [false,true]){
 const f=fixture([busyError(),busyError(),accepted]);if(retry)f.state.value={id:'old',status:'failed'}
 const task=retry?f.retryStep():f.start();await settle()
 assert.ok(f.cardWaitMessage.value);assert.equal(f.busy.value,true)
 await f.start();assert.equal(f.calls.length,1,'double click submitted')
 await f.advance();await f.advance();await task
 assert.equal(f.calls.length,3);assert.equal(f.state.value.id,'new');assert.equal(f.error.value,'');assert.equal(f.cardWaitMessage.value,'')
 for(const {path,options} of f.calls){assert.equal(path,'/api/pro-accounts/same%40example.com/auto-pro');assert.equal(options.body.plan_code,'pro5');assert.equal(!!options.body.retry_current,retry)}
 assert.deepEqual(f.calls[1].options.body.card_preparation_retry,guard)
}
for(const cancel of ['close','unmount']){
 const f=fixture([busyError()]);const task=f.start();await settle();f[cancel]();await task
 assert.equal(f.calls.length,1);assert.equal(f.timers.size,0);assert.equal(f.busy.value,false)
}
for(const failure of [Error('付款结果未知'),Object.assign(Error('网络连接中断'),{network:true}),Object.assign(Error('待核实开卡'),{status:409})]){
 const f=fixture([failure]);await f.start();assert.equal(f.calls.filter(c=>c.options?.method==='POST').length,1);assert.equal(f.cardWaitMessage.value,'');assert.equal(f.error.value,failure.message)
}
const advanced=fixture([busyError(),Object.assign(Error('账号进度已变化'),{status:409})],accepted.pro_auto)
const advancing=advanced.start();await settle();await advanced.advance();await advancing
assert.equal(advanced.calls.filter(c=>c.options?.method==='POST').length,2);assert.equal(advanced.state.value.status,'running');assert.equal(advanced.error.value,'')
const stopped=fixture([busyError(),Object.assign(Error('停止状态已变化'),{status:409})],{status:'stopped',stop_requested:true})
const stopping=stopped.start();await settle();await stopped.advance();await stopping;assert.equal(stopped.state.value.status,'stopped');assert.match(stopped.error.value,/停止状态/)
const capped=fixture(Array.from({length:31},busyError));const task=capped.start();await settle();for(let i=0;i<30;i++)await capped.advance();await task
assert.equal(capped.calls.filter(c=>c.options?.method==='POST').length,31);assert.match(capped.error.value,/已停止自动等待/)
console.log('Passed: busy admission continues same account, bounded waits/cancel, no uncertain write replay, status/Stop guard and stale warning recovery')
