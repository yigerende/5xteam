import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/components/QualitySettingsView.vue', import.meta.url), 'utf8')
const script = source.slice(source.indexOf('<script setup>') + '<script setup>'.length, source.indexOf('</script>'))
const parsed = ts.createSourceFile('QualitySettingsView.js', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const names = new Set(['setForm', 'loadGroups', 'save'])
const declarations = parsed.statements.filter(statement =>
  ts.isFunctionDeclaration(statement) && names.has(statement.name?.text),
).map(statement => statement.getText(parsed)).join('\n')
let saved
let failGroups = false
const context = vm.createContext({
  form: {}, busy: { value: false }, groups: { value: [] }, initial: { value: null }, message: {},
  emit: () => {},
  api: async (path, options) => {
    if (path === '/api/sub2-settings/test') {
      if (failGroups) throw new Error('Groups unavailable')
      return { groups: [1, 2, 3, 4].map(id => ({ id, name: 'Group ' + id })) }
    }
    assert.equal(path, '/api/quality/settings')
    assert.equal(options.method, 'PUT')
    saved = JSON.parse(JSON.stringify(options.body))
    return { settings: saved }
  },
})
vm.runInContext(declarations, context)
context.setForm({ normal_group_ids: null })
assert.equal(Array.isArray(context.form.normal_group_ids), true)
assert.equal(Array.isArray(context.form.degraded_group_ids), true)
context.form.normal_group_ids.push(1, 2)
context.form.degraded_group_ids.push(3, 4)
await context.loadGroups()
assert.equal(context.form.normal_group_ids.join(','), '1,2')
assert.equal(context.form.degraded_group_ids.join(','), '3,4')
await context.save()
assert.deepEqual(saved.normal_group_ids, [1, 2])
assert.deepEqual(saved.degraded_group_ids, [3, 4])
assert.equal(context.busy.value, false)
context.form.normal_group_ids.splice(0, 1)
await context.save()
assert.deepEqual(saved.normal_group_ids, [2])
assert.deepEqual(saved.degraded_group_ids, [3, 4])
context.setForm(saved)
assert.equal(context.form.normal_group_ids.join(','), '2')
assert.equal(context.form.degraded_group_ids.join(','), '3,4')
failGroups = true
await context.loadGroups()
assert.equal(context.form.normal_group_ids.join(','), '2')
assert.equal(context.form.degraded_group_ids.join(','), '3,4')
assert.equal(context.message.type, 'error')
assert.equal(context.busy.value, false)
console.log('Passed: null defaults, multiple numeric IDs, refresh preservation, independent uncheck, save round trip and failed group lookup')

