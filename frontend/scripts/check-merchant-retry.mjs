import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { compileScript, parse } from '@vue/compiler-sfc'
import { transformWithEsbuild } from 'vite'

const source=readFileSync(new URL('../src/components/ProMerchantStages.vue',import.meta.url),'utf8')
const {descriptor}=parse(source)
const compiled=compileScript(descriptor,{id:'merchant-retry-check',inlineTemplate:true})
const {code}=await transformWithEsbuild(compiled.content,'merchant-retry-check.js',{format:'cjs',loader:'js'})
const module={exports:{}}
vm.runInNewContext(code,{module,exports:module.exports,require:id=>id==='vue'?Vue:{default:{render:()=>null},__esModule:true}})
const component=module.exports.default
const base={request_id:'old-request',job_id:'old-job',status:'failed',remote_status:'failed'}
async function render(merchant,disabled=false){return renderToString(Vue.createSSRApp(component,{account:{pro_merchant:merchant},disabled}))}
for(const status of ['failed','cancelled'])assert.match(await render({...base,remote_status:status}),/重试（重新发单）/)
for(const status of ['queued','processing','verifying','attention','succeeded','banned','unknown',''])assert.doesNotMatch(await render({...base,remote_status:status}),/重试（重新发单）/)
for(const m of [{...base,job_id:''},{...base,request_id:''},{...base,completed_at:'2026-11-11T03:00:00Z'},{...base,status:'waiting'}])assert.doesNotMatch(await render(m),/重试（重新发单）/)
assert.match(await render(base,true),/disabled[^>]*>重试（重新发单）/)
assert.match(await render(base),/查询 \/ 续跑进度/)
// Real handlers send the displayed attempt ID, preventing a stale tab from
// reissuing a later failed order. A click also acquires the page's busy guard.
for(const name of ['ProManagementView','ProAutoDialog']){
 const src=readFileSync(new URL(`../src/components/${name}.vue`,import.meta.url),'utf8')
 assert.match(src,/@reissue=/)
 assert.match(src,/merchant-retry/)
 assert.match(src,/request_id:.*pro_merchant\?\.request_id/)
}
console.log('Merchant retry: failure-only button, disabled state, original-query action and stale-request identity passed')
