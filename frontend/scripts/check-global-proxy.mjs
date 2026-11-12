import assert from 'node:assert/strict'
import {readFileSync} from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import {renderToString} from '@vue/server-renderer'
import {parse,compileScript} from '@vue/compiler-sfc'
import {transformWithEsbuild} from 'vite'
import {globalProxyHealthText} from '../src/globalProxyHealth.js'
const now=Date.now()
assert.equal(globalProxyHealthText({state:'cooling',cooldown_until:now+65000},now),'冷却剩余 1 分 5 秒')
assert.equal(globalProxyHealthText({state:'testing'},now),'恢复测试中')
assert.equal(globalProxyHealthText({state:'recovering',test_attempts:2},now),'等待下次测试 · 已测试 2 次')
const source=readFileSync(new URL('../src/components/GlobalProxyFailover.vue',import.meta.url),'utf8')
const {descriptor}=parse(source),compiled=compileScript(descriptor,{id:'policy',inlineTemplate:true})
const {code}=await transformWithEsbuild(compiled.content,'policy.js',{format:'cjs',loader:'js'})
const module={exports:{}};vm.runInNewContext(code,{module,exports:module.exports,require:()=>Vue,console})
const policy={backup_ids:['backup'],cooldown_seconds:300,recovery_tests:3,test_interval_seconds:5}
const props={modelValue:policy,primaryURL:'http://one.example',proxies:[{id:'primary',name:'主线',url:'http://one.example'},{id:'backup',name:'备用线路',url:'http://two.example'}]}
const render=p=>renderToString(Vue.createSSRApp(module.exports.default,p))
const html=await render(props)
assert.match(html,/备用线路/);assert.doesNotMatch(html,/<option[^>]*value="primary"/)
assert.match(html,/value="300"/);assert.match(html,/value="3"/);assert.match(html,/value="5"/)
assert.match(html,/从头登录/);assert.match(html,/测试次数留空/)
assert.match(await render({...props,modelValue:{...policy,recovery_tests:null}}),/placeholder="留空：不测试"/)
console.log('Passed: real main/backup configuration render, defaults, blank test mode, and cooldown countdown')
