import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/components/FreePipelineView.vue', import.meta.url), 'utf8')
const script = source.slice(source.indexOf('<script setup>') + '<script setup>'.length, source.indexOf('</script>'))
const parsed = ts.createSourceFile('FreePipelineView.js', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const functionNames = new Set(['loadPushSettings', 'savePushSettings', 'loadActivePushGroups', 'loadCPAGroups',
  'selectedGroupNames', 'selectedCPAGroupNames', 'selectedModels', 'setMessage'])
const variableNames = new Set(['sub2Form', 'cpaForm', 'cpaGroups', 'pushProvider', 'groups', 'message', 'busy', 'pushGroupsController'])
const declarations = parsed.statements.filter(statement =>
  ts.isFunctionDeclaration(statement) && functionNames.has(statement.name?.text) ||
  ts.isVariableStatement(statement) && statement.declarationList.declarations.some(item => variableNames.has(item.name.getText(parsed))),
).map(statement => statement.getText(parsed)).join('\n')
const tick = () => new Promise(resolve => setImmediate(resolve))
function deferred() {
  let resolve, reject
  const promise = new Promise((ok, fail) => { resolve = ok; reject = fail })
  return { promise, resolve, reject }
}
function savedSettings(provider = 'sub2') {
  return {
    provider,
    sub2: { url: 'https://sub2.example', email: 'admin@example.com', password_present: true, group_ids: [7], group_names: ['Saved Sub2'] },
    cpa: { url: 'https://cpa.example', key_present: true, group_ids: [9], group_names: ['Saved CPA'], enable_401_check: false, quota_enabled: false },
  }
}
function fixture(settings, request = async path => {
  if (path === '/api/sub2-settings/test') return { groups: [{ id: 8, name: 'Available Sub2' }] }
  if (path === '/api/push-settings/cpa/groups') return [{ id: 10, name: 'Available CPA' }]
  throw new Error('Unexpected request: ' + path)
}) {
  const calls = []
  const context = vm.createContext({
    AbortController, ref: value => ({ value }), reactive: value => value,
    api: async (path, options = {}) => {
      calls.push({ path, options })
      if (path === '/api/push-settings' && options.method !== 'PUT') return settings
      if (path === '/api/push-settings' && options.method === 'PUT') return { ...settings, provider: options.body.provider }
      return request(path, options)
    },
  })
  vm.runInContext(declarations + '\nthis.view = { loadPushSettings, savePushSettings, sub2Form, cpaForm, cpaGroups, pushProvider, groups, message, busy }', context)
  return { ...context.view, calls }
}
const groupCalls = view => view.calls.filter(call => call.path !== '/api/push-settings')

for (const provider of ['sub2', 'cpa']) {
  const expectedPath = provider === 'cpa' ? '/api/push-settings/cpa/groups' : '/api/sub2-settings/test'
  const view = fixture(savedSettings(provider), async path => {
    assert.equal(path, expectedPath, 'Disabled provider must never be contacted, even when its saved connection is complete')
    return provider === 'cpa' ? [{ id: 10 }] : { groups: [{ id: 8 }] }
  })
  await view.loadPushSettings()
  await tick()
  assert.deepEqual(groupCalls(view).map(call => call.path), [expectedPath])
  assert.equal(view.message.text, '')
  assert.equal(view.pushProvider.value, provider)
  assert.equal((provider === 'cpa' ? view.cpaGroups : view.groups).value.length, 1)
  assert.equal((provider === 'cpa' ? view.groups : view.cpaGroups).value.length, 0)
  assert.equal(view.sub2Form.groupIDs.join(','), '7', 'Auto loading must preserve saved group selections')
  assert.equal(view.cpaForm.groupIDs.join(','), '9')
}

for (const [provider, field] of [['sub2', 'url'], ['sub2', 'email'], ['sub2', 'password_present'], ['cpa', 'url'], ['cpa', 'key_present']]) {
  const settings = savedSettings(provider)
  settings[provider][field] = ''
  const view = fixture(settings)
  await view.loadPushSettings()
  await tick()
  assert.equal(groupCalls(view).length, 0, 'Incomplete active settings must not fall back to the inactive provider')
  assert.equal(view.message.text, '')
}

for (const provider of ['sub2', 'cpa']) {
  const view = fixture(savedSettings(provider), async () => { throw new Error(provider + ' unavailable') })
  await view.loadPushSettings()
  await tick()
  assert.equal(groupCalls(view).length, 1)
  assert.equal(view.message.text, provider + ' unavailable', 'Active provider errors must remain visible')
  assert.equal(view.message.type, 'error')
}

{
  const pending = deferred()
  const view = fixture(savedSettings(), () => pending.promise)
  let initialized = false
  const loading = view.loadPushSettings().then(() => { initialized = true })
  await tick()
  assert.equal(initialized, true, 'Slow group loading must not hold page initialization')
  pending.resolve({ groups: [] })
  await loading
  await tick()
}

for (const initial of ['sub2', 'cpa']) {
  for (const oldOutcome of ['success', 'failure']) {
    const target = initial === 'sub2' ? 'cpa' : 'sub2'
    const oldRequest = deferred()
    let firstSignal
    const oldPath = initial === 'cpa' ? '/api/push-settings/cpa/groups' : '/api/sub2-settings/test'
    const newPath = target === 'cpa' ? '/api/push-settings/cpa/groups' : '/api/sub2-settings/test'
    const view = fixture(savedSettings(initial), (path, options) => {
      if (path === oldPath) { firstSignal = options.signal; return oldRequest.promise }
      assert.equal(path, newPath)
      return Promise.resolve(target === 'cpa' ? [{ id: 20 }] : { groups: [{ id: 20 }] })
    })
    const loading = view.loadPushSettings()
    await tick()
    view.pushProvider.value = target
    await view.savePushSettings()
    await tick()
    assert.equal(firstSignal?.aborted, true, 'Saving another provider must cancel the previous group request')
    assert.deepEqual(groupCalls(view).map(call => call.path), [oldPath, newPath])
    if (oldOutcome === 'failure') oldRequest.reject(new Error('Inactive provider DNS failed'))
    else oldRequest.resolve(initial === 'cpa' ? [{ id: 99 }] : { groups: [{ id: 99 }] })
    await loading
    await tick()
    assert.equal(view.message.type, 'success', 'Late inactive response must not overwrite save success')
    assert.equal((initial === 'cpa' ? view.cpaGroups : view.groups).value.length, 0)
    assert.equal((target === 'cpa' ? view.cpaGroups : view.groups).value[0].id, 20)
    assert.equal(view.busy.value, '')
  }
}

console.log('Passed: active provider only, inactive service unavailable, missing settings, saved selections, active errors, nonblocking initialization, both switch directions and stale responses')
