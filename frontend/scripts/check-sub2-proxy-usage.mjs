import assert from 'node:assert/strict'
import vm from 'node:vm'
import {readFileSync} from 'node:fs'
import {ref,computed} from 'vue'
const code=readFileSync(new URL('../src/components/Sub2ProxyPicker.vue',import.meta.url),'utf8').split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm,'')
const props={url:'https://sub.example',email:'admin@example.test',password:'',endpoint:'/api/pro-settings/sub2/proxies',enabled:true,active:true,disabled:false,ids:[1],authMode:''}
const rows=[{id:1,name:'proxy',status:'active',account_count:0,binding_key:'stable-key',cumulative_bindings:5,pending_bindings:0,binding_revision:1}]
const calls=[],emits=[];let cap=5,failEdit=false
const ctx=vm.createContext({ref,computed,watch(){},onBeforeUnmount(){},defineProps:()=>props,defineEmits:()=>((...args)=>emits.push(args)),AbortController,URL,Date,async api(path,options={}){
 calls.push({path,options})
 if(path.includes('binding-policy')){if(options.method==='PUT')cap=options.body.max_bindings;return {max_bindings:cap}}
 if(path.includes('binding-usage')){if(failEdit)throw Error('累计次数已变化，请刷新后再修改');rows[0].cumulative_bindings=options.body.count;rows[0].binding_revision++;return {saved:true}}
 return rows.map(p=>({...p}))
}})
vm.runInContext(code+';globalThis.f={refresh,available,state,rows,limit,savedLimit,saveLimit,beginEdit,editCount,saveCount,editing,error,message,selectVisible};',ctx)
const f=ctx.f;await f.refresh()
assert.equal(f.rows.value[0].cumulative_bindings,5);assert.equal(f.savedLimit.value,5);assert.equal(f.available(f.rows.value[0]),false);assert.match(f.state(f.rows.value[0]),/累计额度/)
f.limit.value=0;await f.saveLimit();assert.equal(cap,0);assert.equal(f.available(f.rows.value[0]),true)
f.limit.value=-1;const before=calls.length;await f.saveLimit();assert.equal(calls.length,before);assert.match(f.error.value,/非负整数/)
f.beginEdit(f.rows.value[0]);f.editCount.value=2;await f.saveCount(f.rows.value[0]);assert.equal(f.rows.value[0].cumulative_bindings,2);assert.equal(f.editing.value,'')
const request=calls.find(c=>c.path.includes('binding-usage')).options.body
assert.deepEqual(JSON.parse(JSON.stringify(request)),{url:props.url,binding_key:'stable-key',count:2,expected_revision:1})
failEdit=true;f.beginEdit(f.rows.value[0]);f.editCount.value=0
const originalRevision=f.rows.value[0].binding_revision
rows[0].binding_revision++;rows[0].cumulative_bindings++;await f.refresh()
await f.saveCount(f.rows.value[0]);assert.match(f.error.value,/已变化/);assert.equal(f.rows.value[0].cumulative_bindings,3)
assert.equal(calls.filter(c=>c.path.includes('binding-usage')).at(-1).options.body.expected_revision,originalRevision,'refresh during editing must not adopt a newer revision')
f.savedLimit.value=4;f.rows.value[0].pending_bindings=1;assert.equal(f.available(f.rows.value[0]),false)
f.rows.value.push({id:2,status:'active',account_count:0,binding_key:'other',cumulative_bindings:0,pending_bindings:0});f.selectVisible();assert.deepEqual([...emits.at(-1)[1]],[1,2])
console.log('Passed: cumulative display, limit save/validation, versioned manual edits, pending-cap exclusion and retained selected IDs')
