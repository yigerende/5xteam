import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import vm from 'node:vm'
import {ref, computed, watch} from 'vue'

const source=readFileSync(new URL('../src/components/Sub2ApiView.vue',import.meta.url),'utf8')
const code=source.split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
const requests=[],timers=new Map()
let timerID=0,unmount
const context=vm.createContext({
 ref,computed,watch,URLSearchParams,AbortController,Intl,Date,
 onMounted(){},onBeforeUnmount(fn){unmount=fn},
 setTimeout(fn,delay){const id=++timerID;timers.set(id,{fn,delay});return id},
 clearTimeout(id){timers.delete(id)},
 api(url,options){let resolve,reject;const promise=new Promise((ok,fail)=>{resolve=ok;reject=fail});requests.push({url,options,resolve,reject});return promise},
})
vm.runInContext(code+'\nglobalThis.testView={load,report,status,progress,error};',context)
const v=context.testView
v.progress.value={old:true}
const first=v.load()
requests[0].resolve({rows:['fresh report']})
requests[1].resolve({running:true})
requests[2].reject(new Error('progress timeout'))
await first
assert.equal(v.report.value.rows[0],'fresh report')
assert.equal(v.status.value.running,true)
assert.equal(v.progress.value.old,true)
assert.match(v.error.value,/同步进度.*自动重试/)
assert.equal(timers.size,1)

const retry=v.load()
requests[3].resolve({rows:['retried']})
requests[4].resolve({running:false})
requests[5].resolve({cursors:{usage:{id:2}}})
await retry
assert.equal(v.error.value,'')
assert.equal(v.progress.value.cursors.usage.id,2)
assert.equal(timers.size,1)

const stale=v.load(),fresh=v.load()
assert.equal(requests[6].options.signal.aborted,true)
for(const r of requests.slice(9,12))r.resolve({revision:2})
await fresh
for(const r of requests.slice(6,9))r.resolve({revision:1})
await stale
assert.equal(v.report.value.revision,2)
assert.equal(v.status.value.revision,2)

const leaving=v.load()
unmount()
for(const r of requests.slice(12,15))r.resolve({revision:3})
await leaving
assert.equal(v.report.value.revision,2)
assert.equal(timers.size,0)
console.log('Passed: partial refresh, retry recovery, stale response cancellation, and unmount cleanup')
