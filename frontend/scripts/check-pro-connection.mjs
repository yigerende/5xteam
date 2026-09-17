import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/components/ProManagementView.vue', import.meta.url), 'utf8')
const script = source.split('<script setup>')[1].split('</script>')[0]
const parsed = ts.createSourceFile('ProManagementView.js', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const names = new Set(['connectionPayload', 'loadGroups', 'testProvider', 'syncGroupNames', 'saveSettings'])
const functions = parsed.statements.filter(node => ts.isFunctionDeclaration(node) && names.has(node.name?.text)).map(node => node.getText(parsed)).join('\n')

function fixture(request) {
  const calls = []
  const state = {
    settings: {
      provider: 'sub2',
      sub2: { url: ' https://new-sub2.example/ ', email: ' admin@example.com ', group_ids: [7], group_names: ['Saved Sub2'] },
      cpa: { url: ' https://new-cpa.example/ ', group_ids: [9], group_names: ['Saved CPA'] },
      sub2_password: 'draft-password', cpa_key: 'draft-key',
    },
    sub2Groups: { value: [] }, cpaGroups: { value: [] }, busy: { value: '' },
    message: null, updateCountdown: () => {},
    api: async (path, options) => { calls.push({ path, options }); return request(path, options) },
    setMessage: (text, type) => { state.message = { text, type } },
  }
  vm.createContext(state)
  vm.runInContext(functions + '\nthis.actions = { loadGroups, testProvider, syncGroupNames, saveSettings }', state)
  return { state, calls, ...state.actions }
}
for (const provider of ['sub2', 'cpa']) {
  for (const action of ['groups', 'test']) {
    const groups = [{ id: provider === 'sub2' ? 7 : 9, name: 'Available' }]
    const view = fixture(async () => action === 'groups' ? groups : { connected: true, groups })
    const before = JSON.stringify(view.state.settings)
    await (action === 'groups' ? view.loadGroups(provider) : view.testProvider(provider))
    assert.equal(view.calls.length, 1, 'Do not save settings or contact the other provider')
    const [{ path, options }] = view.calls
    assert.equal(path, '/api/pro-settings/' + provider + '/' + action)
    assert.equal(options.method, 'POST')
    assert.equal(options.body[provider].url, 'https://new-' + provider + '.example/')
    assert.equal(options.body[provider === 'sub2' ? 'sub2_password' : 'cpa_key'], provider === 'sub2' ? 'draft-password' : 'draft-key')
    assert.equal(options.body[provider === 'sub2' ? 'cpa' : 'sub2'], undefined, 'Inactive credentials must not be submitted')
    if (provider === 'sub2') assert.equal(options.body.sub2.email, 'admin@example.com')
    if (action === 'groups' || provider === 'sub2') assert.equal(view.state[provider + 'Groups'].value.length, 1)
    assert.equal(JSON.stringify(view.state.settings), before, 'Testing must not clear secrets or activate a provider')
    assert.equal(view.state.busy.value, '')
    assert.equal(view.state.message.type, 'success')
  }
  const blank = fixture(async () => [])
  blank.state.settings.sub2_password = ''
  blank.state.settings.cpa_key = ''
  await blank.loadGroups(provider)
  assert.equal(blank.calls[0].options.body[provider === 'sub2' ? 'sub2_password' : 'cpa_key'], '', 'Blank reuses saved secret on the server')

  const failed = fixture(async () => { throw new Error('connection refused') })
  await failed.loadGroups(provider)
  assert.equal(failed.state.message.text, 'connection refused')
  assert.equal(failed.state.message.type, 'error')
  assert.equal(failed.state.busy.value, '')
  assert.equal(failed.state.settings.sub2_password, 'draft-password')

  let resolve
  const pending = fixture(() => new Promise(done => { resolve = done }))
  const running = pending.loadGroups(provider)
  await pending.loadGroups(provider)
  await pending.testProvider(provider)
  assert.equal(pending.calls.length, 1, 'Ignore double clicks while loading')
  resolve([])
  await running
}
{
  const view = fixture(async (_path, options) => JSON.parse(JSON.stringify(options.body)))
  view.syncGroupNames('sub2')
  view.syncGroupNames('cpa')
  assert.equal(view.state.settings.sub2.group_names[0], 'Saved Sub2')
  assert.equal(view.state.settings.cpa.group_names[0], 'Saved CPA')
  view.state.sub2Groups.value = [{ id: 7, name: 'New group' }]
  await view.saveSettings()
  assert.equal(view.calls[0].options.method, 'PUT')
  assert.equal(view.calls[0].options.body.sub2.group_names[0], 'New group')
  assert.equal(view.state.settings.sub2_password, '')
}
console.log('Pro draft connection frontend tests passed')
