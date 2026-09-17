import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, computed, watch, nextTick } from 'vue'

const source = readFileSync(new URL('../src/components/FreePipelineView.vue', import.meta.url), 'utf8')
const parsed = ts.createSourceFile('Pipeline.js', source.split('<script setup>')[1].split('</script>')[0], ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const names = new Set(['clearPipelineSelection', 'syncPipelineSelection', 'selectMotherChildren', 'togglePipelineSelected', 'toggleAllDisplayed', 'setAccountPage', 'setAccountPageSize', 'setTeamSpaceFilter', 'runSelectedPipelineTask', 'removeSelectedRecords', 'hasDownstream'])
const vars = new Set(['selectedAccountIDs', 'selectedAccountSnapshots', 'selectedMotherID', 'motherSelectionController', 'selectedPipelineAccounts', 'allDisplayedSelected', 'displayedAccounts', 'hasDownstream'])
const code = parsed.statements.filter(s => ts.isFunctionDeclaration(s) ? names.has(s.name?.text) : ts.isVariableStatement(s) && s.declarationList.declarations.some(d => vars.has(d.name.getText(parsed)))).map(s => s.getText(parsed)).join('\n')
const children = Array.from({ length: 23 }, (_, i) => ({ id: 'a' + i, email: 'child' + i + '@example.com', admin_account_id: '8', team_account_id: 'ws8', accept_status: 'completed', remove_status: 'pending', oauth_status: 'completed', sub2_account_id: i + 1 }))
let mode = 'success', deferred, calls = [], maxActive = 0, active = 0, confirmed = true
const liveAccounts = ref(children.slice(0, 10))
const context = vm.createContext({
  ref, computed, Map, Set, AbortController, URLSearchParams,
  liveAccounts, busy: ref(''), page: ref(1), pageSize: ref(10), teamSpaceFilter: ref(''), pushProvider: ref('sub2'), activeProviderLabel: ref('Sub2'),
  props: { adminAccounts: [{ id: '8', label: 'Mother 8' }, { id: '9', label: 'Mother 9' }] },
  window: { confirm: () => confirmed }, emit: () => {},
  refreshLiveAccounts: async () => {}, setMessage: (text, type) => { context.message = { text, type } },
  runTracked: async (_account, _action, fn) => fn(),
  runOAuthSilently: async account => { calls.push(account.id) },
  api: async (path, options) => {
    if (path.includes('/select-by-admin?')) {
      assert.equal(options.signal instanceof AbortSignal, true)
      if (mode === 'error') throw Error('Fixture request failed')
      if (mode === 'deferred') return new Promise(resolve => { deferred = resolve })
      if (mode === 'mismatch') return { items: [{ ...children[0], admin_account_id: 'other' }] }
      if (mode === 'empty') return { items: [] }
      const admin = new URL(path, 'http://fixture').searchParams.get('admin_account_id')
      return { items: admin === '8' ? children : [{ ...children[0], id: 'b', admin_account_id: '9', team_account_id: 'ws9' }] }
    }
    calls.push(path)
    active++
    maxActive = Math.max(maxActive, active)
    await new Promise(resolve => setTimeout(resolve, 1))
    active--
    return {}
  },
})
vm.runInContext(code + '\nglobalThis.state = { selectedAccountIDs, selectedAccountSnapshots, selectedMotherID, selectedPipelineAccounts, allDisplayedSelected };', context)
const state = context.state
watch(liveAccounts, context.syncPipelineSelection)
state.selectedMotherID.value = '8'
await context.selectMotherChildren()
assert.equal(state.selectedPipelineAccounts.value.length, 23)
assert.equal(state.allDisplayedSelected.value, true)
context.setAccountPage(2)
liveAccounts.value = children.slice(10, 20)
await nextTick()
assert.equal(state.selectedPipelineAccounts.value.length, 23)
context.setAccountPageSize(20)
liveAccounts.value = [{ ...children[10], remove_status: 'completed' }]
await nextTick()
assert.equal(state.selectedPipelineAccounts.value.find(a => a.id === 'a10').remove_status, 'completed')
assert.equal(state.selectedPipelineAccounts.value.length, 23)
context.togglePipelineSelected(children[10])
assert.equal(state.selectedPipelineAccounts.value.length, 22)
context.togglePipelineSelected(children[10])
assert.equal(state.selectedPipelineAccounts.value.length, 23)
liveAccounts.value = children.slice(0, 10)
await nextTick()
context.toggleAllDisplayed()
assert.equal(state.selectedPipelineAccounts.value.length, 13)
context.toggleAllDisplayed()
assert.equal(state.selectedPipelineAccounts.value.length, 23)
mode = 'error'
await context.selectMotherChildren()
assert.equal(state.selectedPipelineAccounts.value.length, 23)
assert.equal(context.message.type, 'error')
mode = 'mismatch'
await context.selectMotherChildren()
assert.equal(state.selectedPipelineAccounts.value.length, 23)
mode = 'deferred'
const pending = context.selectMotherChildren()
context.setTeamSpaceFilter('inside')
deferred({ items: children })
await pending
assert.equal(state.selectedPipelineAccounts.value.length, 0)
assert.equal(context.busy.value, '')
const pendingClear = context.selectMotherChildren()
context.clearPipelineSelection()
deferred({ items: children })
await pendingClear
assert.equal(state.selectedPipelineAccounts.value.length, 0)
mode = 'success'
state.selectedMotherID.value = '9'
await context.selectMotherChildren()
assert.deepEqual([...state.selectedAccountIDs.value], ['b'])
state.selectedMotherID.value = '8'
await context.selectMotherChildren()
confirmed = false
await context.runSelectedPipelineTask('remove')
assert.equal(calls.length, 0)
assert.equal(state.selectedPipelineAccounts.value.length, 23)
confirmed = true
await context.runSelectedPipelineTask('remove')
assert.equal(calls.length, 23)
assert.equal(new Set(calls).size, 23)
assert.equal(maxActive, 1, 'same-mother removal must stay serial')
assert.equal(state.selectedPipelineAccounts.value.length, 0)
calls = []
await context.selectMotherChildren()
await context.runSelectedPipelineTask('quota')
assert.equal(calls.length, 23, 'off-page accounts must be included in bulk quota')
calls = []
await context.selectMotherChildren()
await context.removeSelectedRecords()
assert.equal(calls.length, 23, 'off-page accounts must be included in bulk record deletion')
mode = 'empty'
await context.selectMotherChildren()
assert.equal(state.selectedPipelineAccounts.value.length, 0)
assert.match(source, /watch\(liveAccounts, syncPipelineSelection\)/)
assert.match(source, /@change="clearPipelineSelection"/)
console.log('Passed: cross-page selection, refresh synchronization, row/page toggles, replacement, failures, cancellation, empty results, bulk actions, and serial removal')
