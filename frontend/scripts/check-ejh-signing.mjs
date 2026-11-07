import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { compileScript, parse } from '@vue/compiler-sfc'
import { transformWithEsbuild } from 'vite'

const source=readFileSync(new URL('../src/components/EJHSigningKey.vue',import.meta.url),'utf8')
const {descriptor}=parse(source)
const compiled=compileScript(descriptor,{id:'ejh-signing-test',inlineTemplate:true})
const {code}=await transformWithEsbuild(compiled.content,'ejh-signing-test.js',{format:'cjs',loader:'js'})
const module={exports:{}}
let requests=[],copied='',failure=false
const modules={vue:Vue,'../api':{api:async(path,options)=>{requests.push({path,options});if(failure)throw new Error('fixture network failure');return {signing_key_present:true,signing_public_key:'public-fixture'}}},'../clipboard':{copyText:async value=>{copied=value}}}
vm.runInNewContext(code,{window:{confirm:()=>true},module,exports:module.exports,require:id=>{assert.ok(modules[id]);return modules[id]}})
const component=module.exports.default
const render=settings=>renderToString(Vue.createSSRApp(component,{settings}))
const saved={app_key:'fixture',secret_present:true,signing_key_present:true,signing_public_key:'public-fixture'}
let html=await render(saved)
assert.ok(html.includes('复制公钥')&&html.includes('public-fixture')&&html.includes('私钥由服务器加密保管'))
assert.ok(!html.includes('复制私钥')&&!html.includes('导入 EJH 签名私钥'))
html=await render({})
assert.match(html,/<button[^>]*disabled/)
assert.ok(html.includes('请先保存 AppKey 和 AppSecret'))
assert.equal(requests.length,0)

// Exercise the actual component event handlers through Vue's renderer.
const renderer=Vue.createRenderer({
 createElement:type=>({type,props:{},children:[],addEventListener(){},removeEventListener(){}}),createText:text=>({text}),createComment:text=>({text}),
 setText:(n,text)=>{n.text=text},setElementText:(n,text)=>{n.text=text},
 patchProp:(n,key,old,value)=>{n.props[key]=value},insert:(n,p)=>{n.parent=p;p.children??=[];p.children.push(n)},
 insertStaticContent:(text,p)=>{const n={type:"static",text,parent:p};p.children.push(n);return [n,n]},
 remove:n=>{if(n.parent)n.parent.children=n.parent.children.filter(x=>x!==n)},parentNode:n=>n.parent,nextSibling:n=>{const a=n.parent?.children||[];return a[a.indexOf(n)+1]||null},
})
const root={children:[]};const settings=Vue.reactive({app_key:'fixture',secret_present:true})
const app=renderer.createApp(component,{settings,onUpdated:cfg=>Object.assign(settings,cfg)});app.config.errorHandler=e=>{console.error(e);throw e};app.mount(root)
const nodes=(n)=>[n,...(n.children||[]).flatMap(nodes)]
const button=text=>nodes(root).find(n=>n.type==='button'&&n.text===text)
await button('生成签名密钥').props.onClick();await Vue.nextTick()
assert.equal(requests.length,1);assert.equal(requests[0].path,'/api/ejh/signing-key');assert.equal(requests[0].options.method,'POST')
await button('复制公钥').props.onClick();assert.equal(copied,'public-fixture')
assert.equal(requests.length,1,'copy must not regenerate keys')
await button('重新生成密钥').props.onClick();await Vue.nextTick()
assert.equal(requests.length,2);assert.equal(requests[1].options.body.regenerate,true)
app.unmount()
const errorRoot={children:[]};failure=true
const errorApp=renderer.createApp(component,{settings:{app_key:'fixture',secret_present:true}});errorApp.mount(errorRoot)
await nodes(errorRoot).find(n=>n.type==='button'&&n.text==='生成签名密钥').props.onClick();await Vue.nextTick()
assert.ok(nodes(errorRoot).some(n=>n.text==='fixture network failure'))
assert.ok(!nodes(errorRoot).some(n=>n.text==='复制公钥'))
errorApp.unmount()
console.log('Passed: key generation, public-only display/copy, no generation on read, failure feedback')
