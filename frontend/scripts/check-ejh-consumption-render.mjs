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
assert.doesNotMatch(await totals({ data:{ cases:[row] }, masked:true }), /未推送，无消费数据/)
for (const name of ['EJHCaseAmounts','EJHAccountReturnSummary']) {
  const render = await component(name)
  assert.match(await render({row}), /未推送，无消费数据/)
  assert.doesNotMatch(await render({row:{...row,consumption_total:0}}), /未推送，无消费数据/)
}
const card = await component('EJHCardAmounts')
assert.match(await card({card:{id:'card',issuer_linked:false},cases:[row]}),/1 个账号未推送，无消费数据/)
console.log('Passed: real overview, card and account templates distinguish no-push, unknown and known zero; masked mode hides count')
