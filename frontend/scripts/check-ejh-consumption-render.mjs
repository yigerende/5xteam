import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { compileScript, parse } from '@vue/compiler-sfc'
import { transformWithEsbuild } from 'vite'

async function component(name) {
  const file = new URL(`../src/components/${name}.vue`, import.meta.url)
  const source = readFileSync(file, 'utf8'), modules = { vue: Vue }
  for (const [, id] of source.matchAll(/^import .* from ['"]([^'"]+)['"]/gm)) {
    if (id === 'vue') continue
    modules[id] = id.endsWith('.vue') ? { default: { render: () => null }, __esModule: true } : await import(new URL(id + '.js', file))
  }
  const { descriptor } = parse(source)
  const compiled = compileScript(descriptor, { id: name, inlineTemplate: true })
  const { code } = await transformWithEsbuild(compiled.content, `${name}.js`, { format: 'cjs', loader: 'js' })
  const module = { exports: {} }
  vm.runInNewContext(code, { module, exports: module.exports, require: id => { assert.ok(modules[id], id); return modules[id] }, console })
  return props => renderToString(Vue.createSSRApp(module.exports.default, props))
}
const row = { id:'order', email:'example@example.com', state:'returned', consumption_state:'not_pushed', returns:[] }
const totals = await component('EJHFinanceTotals')
let html = await totals({ data:{ cases:[row] } })
assert.match(html, /1 个账号未推送，无消费数据/)
assert.doesNotMatch(html, /个账号未查询/)
html = await totals({ data:{ cases:[row,{email:'unknown@example.com'}] } })
assert.match(html, /1 个账号未查询/)
assert.match(html, /查看处理/)
assert.doesNotMatch(await totals({ data:{ cases:[row] }, masked:true }), /未推送，无消费数据/)
for (const name of ['EJHCaseAmounts','EJHAccountReturnSummary']) {
  const render = await component(name)
  assert.match(await render({row}), /未推送，无消费数据/)
  assert.doesNotMatch(await render({row:{...row,consumption_total:0}}), /未推送，无消费数据/)
}
const card = await component('EJHCardAmounts')
assert.match(await card({card:{id:'card',issuer_linked:false},cases:[row]}),/1 个账号未推送，无消费数据/)
console.log('Passed: real overview, card and account templates distinguish no-push, unknown and known zero; masked mode hides count')
const detail=await component('EJHConsumptionDetails')
html=await detail({data:{cases:[{...row,consumption_state:'',last4:'6870',consumption_can_review:true}]},kind:'missing'})
assert.match(html,/example@example.com/)
assert.match(html,/6870/)
assert.match(html,/已回款/)
assert.match(html,/确认未推送/)
html=await detail({data:{cases:[{...row,consumption_review:{state:'not_pushed',reason:'核实原下游未创建',revision:1}}]},kind:'notPushed'})
assert.match(html,/恢复自动判断/)
assert.match(html,/核实原下游未创建/)
console.log('Passed: actual review dialog shows account, card, state, evidence and reversal action')
