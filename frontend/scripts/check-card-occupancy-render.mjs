import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { compileScript, parse } from '@vue/compiler-sfc'
import { transformWithEsbuild } from 'vite'
import * as status from '../src/card-status.js'

const source = readFileSync(new URL('../src/components/CardOccupancy.vue', import.meta.url), 'utf8')
const { descriptor } = parse(source)
const compiled = compileScript(descriptor, { id: 'occupancy-render', inlineTemplate: true })
const { code } = await transformWithEsbuild(compiled.content, 'occupancy-render.js', { format: 'cjs' })
const module = { exports: {} }
const modules = { vue: Vue, '../card-status': status, './EJHOccupancyDialog.vue': { default: { render: () => null }, __esModule: true } }
vm.runInNewContext(code, { module, exports: module.exports, require: id => modules[id], console })
for (const issuer of ['ejh', 'nexovcc']) {
  for (const [state, deleted, action] of [['manual_review', false, true], ['submission_unknown', false, true], ['failed', false, true], ['success', true, true], ['success', false, false], ['processing', false, false]]) {
    const local = { card_type: issuer, active_accounts: 1, occupants: [{ email: 'test@example.com', order_id: 'original', status: state, deleted }] }
    const app = Vue.createSSRApp(module.exports.default, { card: issuer === 'ejh' ? local : { local }, compact: true })
    const html = await renderToString(app)
    assert.equal(html.includes('处理占用'), action, issuer + '/' + state + '/' + deleted)
  }
}
console.log('Passed: stopped orders expose occupancy handling for existing accounts on both issuers; active and successful accounts stay protected')
