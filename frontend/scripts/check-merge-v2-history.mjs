import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { compileScript, parse } from '@vue/compiler-sfc'
import { transformWithEsbuild } from 'vite'
import * as utils from '../src/utils.js'

const source = readFileSync(new URL('../src/components/ProMergeV2Stages.vue', import.meta.url), 'utf8')
const { descriptor } = parse(source)
const compiled = compileScript(descriptor, { id: 'merge-history-test', inlineTemplate: true })
const { code } = await transformWithEsbuild(compiled.content, 'merge-history-test.js', { format: 'cjs', loader: 'js' })
const module = { exports: {} }
const modules = { vue: Vue, '../utils': utils, './CompactErrorDetails.vue': { default: { render: () => null }, __esModule: true } }
vm.runInNewContext(code, { module, exports: module.exports, require: id => { assert.ok(modules[id]); return modules[id] } })
const render = account => renderToString(Vue.createSSRApp(module.exports.default, { account, readonly: true }))
for (const status of ['running', 'completed', 'failed', 'interrupted', 'unknown']) {
  const html = await render({ pro_auto: { steps: { merge_v2: status } } })
  assert.ok(html.includes('步骤明细缺失'))
  assert.ok(!html.includes('未开始'))
}
for (const account of [{}, { pro_auto: { steps: { merge_v2: 'pending' } } }, { pro_auto: { steps: { merge_v2: 'not_started' } } }]) {
  const html = await render(account)
  assert.ok(!html.includes('步骤明细缺失'))
  assert.equal((html.match(/未开始/g) || []).length, 5)
}
const html = await render({ pro_auto: { steps: { merge_v2: 'completed' } }, pro_merge_v2: { status: 'completed', steps: { invite: 'completed', login: 'completed', submit: 'completed', remove: 'completed', groups: 'completed' } } })
assert.ok(!html.includes('步骤明细缺失'))
assert.equal((html.match(/<small>完成<\/small>/g) || []).length, 5)
console.log('Passed: missing workflow history is distinguished from not started; actual steps remain authoritative')
