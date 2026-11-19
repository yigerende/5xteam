import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import * as Vue from 'vue'
import { renderToString } from '@vue/server-renderer'
import { compileScript, parse } from '@vue/compiler-sfc'
import { transformWithEsbuild } from 'vite'

// Execute the real template before the first async response. Script-only request
// tests cannot detect a template dereferencing state while it is still null.
const file = new URL('../src/components/ProRestockView.vue', import.meta.url)
const source = readFileSync(file, 'utf8')
const modules = { vue: Vue }
for (const [, id] of source.matchAll(/^import .* from ['"]([^'"]+)['"]/gm)) {
  if (id === 'vue') continue
  modules[id] = id.endsWith('.vue')
    ? { default: { render: () => null }, __esModule: true }
    : await import(new URL(id + '.js', file))
}
const { descriptor } = parse(source)
const compiled = compileScript(descriptor, { id: 'restock-render-check', inlineTemplate: true })
const { code } = await transformWithEsbuild(compiled.content, 'restock-render-check.js', { format: 'cjs', loader: 'js' })
const module = { exports: {} }
vm.runInNewContext(code, {
  module, exports: module.exports, require: id => {
    assert.ok(modules[id], 'Unexpected template dependency: ' + id)
    return modules[id]
  },
  console,
})
const app = Vue.createSSRApp(module.exports.default)
app.config.errorHandler = error => { throw error }
const html = await renderToString(app)
assert.match(html, /正在读取补货状态/)
for (const title of ['监控大屏', '补货配置', '兜底配置', '决策与记录']) assert.ok(html.includes(title))
assert.ok(!html.includes('pro-fallback-card-slot'), 'Data-dependent content rendered before state arrived')
console.log('Passed: restock initial template renders without data and keeps all tab navigation available')

// Render the saved settings tab to verify the browser's number constraints,
// including the original screenshot's rejected 10-second quota interval.
const configuredSource=source.replace("const state=ref(null),tab=ref('dashboard')", "const state=ref({config:{enabled:false,version:1,check_seconds:1,quota_seconds:10,quota_concurrency:8,cooldown_seconds:0,emergency_cooldown_seconds:120},tasks:[],limits:{}}),tab=ref('settings')")
 .replace('const sections=[', 'Object.assign(form,state.value.config)\nconst sections=[')
const configuredScript=compileScript(parse(configuredSource).descriptor,{id:'restock-settings-check',inlineTemplate:true})
const configuredCode=await transformWithEsbuild(configuredScript.content,'restock-settings-check.js',{format:'cjs',loader:'js'})
const configuredModule={exports:{}}
vm.runInNewContext(configuredCode.code,{module:configuredModule,exports:configuredModule.exports,require:id=>modules[id],console})
const settingsHTML=await renderToString(Vue.createSSRApp(configuredModule.exports.default))
const cadence=settingsHTML.match(/<article[^>]*><h2>运行节奏<\/h2>[\s\S]*?<\/article>/)?.[0]
assert.ok(cadence,'cadence section missing')
const inputs=[...cadence.matchAll(/<input\b[^>]*>/g)].map(m=>m[0])
assert.equal(inputs.length,5)
for(const input of inputs)assert.ok(!/\bmax=/.test(input),'former timing upper bound retained')
assert.match(inputs[1],/value="10"/);assert.match(inputs[1],/min="1"/)
assert.match(inputs[2],/value="8"/);assert.match(inputs[3],/min="0"/)
assert.match(inputs[4],/value="120"/)
console.log('Passed: rendered cadence controls allow 10 seconds, configurable concurrency and independent zero cooldown')
