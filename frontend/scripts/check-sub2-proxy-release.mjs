import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import {renderToString} from '@vue/server-renderer'
import {parse,compileScript} from '@vue/compiler-sfc'
import {transformWithEsbuild} from 'vite'
import {proxyReleaseText,proxyReleasePending,proxyBindingID} from '../src/sub2ProxyRelease.js'
const now=Date.parse('2026-11-12T10:00:00Z')
const b={id:'one',state:'bound',proxy_id:7,proxy_name:'测试代理',bound_at:'2026-11-12T09:53:00Z',due_at:'2026-11-12T10:23:00Z',minutes:30}
assert.equal(proxyReleaseText(b,now),'代理解绑剩余：23 分钟')
assert.equal(proxyReleaseText(b,now+22*60000+59999),'代理解绑剩余：1 分钟')
assert.equal(proxyReleaseText(b,now+23*60000),'代理解绑中')
assert.equal(proxyReleaseText({...b,state:'retry'},now+24*60000),'代理解绑失败，自动重试中')
assert.equal(proxyReleaseText({...b,state:'unbound'},now),'代理已解绑')
assert.equal(proxyReleasePending({...b,state:'unbound'}),false)
assert.equal(proxyReleaseText({...b,state:'permanent',due_at:null},now),'代理：一直绑定')
async function renderComponent(name,props){
 const file=new URL(`../src/components/${name}.vue`,import.meta.url),source=readFileSync(file,'utf8')
 const modules={vue:Vue}
 for(const [,id] of source.matchAll(/^import .* from ['"]([^'"]+)['"]/gm))if(id!=='vue')modules[id]=await import(new URL(id.endsWith('.js')?id:id+'.js',file))
 const {descriptor}=parse(source),compiled=compileScript(descriptor,{id:name,inlineTemplate:true})
 const {code}=await transformWithEsbuild(compiled.content,name+'.js',{format:'cjs',loader:'js'})
 const module={exports:{}};vm.runInNewContext(code,{module,exports:module.exports,require:id=>modules[id],console})
 return renderToString(Vue.createSSRApp(module.exports.default,props))
}
const html=await renderComponent('Sub2ProxyStatus',{id:'one',binding:b,now})
assert.match(html,/代理解绑剩余：23 分钟/);assert.match(html,/测试代理/);assert.match(html,/预计解绑/)
assert.doesNotMatch(await renderComponent('Sub2ProxyStatus',{binding:b,now}),/代理解绑剩余/)
assert.match(await renderComponent('Sub2ProxyStatus',{id:'one',binding:{...b,state:'retry'},now}),/重试解绑/)
assert.doesNotMatch(await renderComponent('Sub2ProxyStatus',{id:'one',binding:{...b,state:'bound'},now:now+24*60000}),/代理已解绑/)
console.log('Passed: countdown rounding, overdue awaiting confirmation, permanent/retry/completed states, and actual account-label component')

assert.equal(proxyBindingID({push_provider:'cpa',sub2_proxy_binding_id:'old'}),'')
assert.equal(proxyBindingID({push_provider:'sub2',sub2_proxy_binding_id:'new'}),'new')
