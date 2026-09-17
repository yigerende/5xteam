import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { canJoinAdmin } from '../src/adminRotation.js'

const admin = { id: 'mother', team_account_id: 'team', rotation_disabled: true }
assert.equal(canJoinAdmin({}, { ...admin, rotation_disabled: false }), true)
assert.equal(canJoinAdmin({}, { id: 'legacy' }), true)
for (const status of ['running', 'completed']) {
  const child = { admin_account_id: 'mother', team_account_id: 'team', invite_status: status, remove_status: 'pending' }
  assert.equal(canJoinAdmin(child, admin), true)
  assert.equal(canJoinAdmin({ ...child, remove_status: 'completed' }, admin), false)
  assert.equal(canJoinAdmin({ ...child, remote_removed_at: '2026-09-17' }, admin), false)
  assert.equal(canJoinAdmin({ ...child, admin_account_id: 'other' }, admin), false)
}
assert.equal(canJoinAdmin({ admin_account_id: 'mother', team_account_id: 'team', invite_status: 'failed' }, admin), false)

const source = readFileSync(new URL('../src/components/AdminAccountsView.vue', import.meta.url), 'utf8')
const parsed = ts.createSourceFile('view.js', source.split('<script setup>')[1].split('</script>')[0], ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const code = parsed.statements.filter(node => ts.isFunctionDeclaration(node) && node.name?.text === 'toggleRotation').map(node => node.getText(parsed)).join('\n')
for (const initial of [false, true]) {
  let resolve
  const calls = [], notices = [], emitted = []
  const account = { id: 'mother', label: 'Mother', rotation_disabled: initial }
  const state = {
    rotationSaving: { value: new Set() }, rows: { value: [account] },
    api: (path, options) => { calls.push({ path, options }); return new Promise(done => { resolve = done }) },
    setMessage: (...args) => notices.push(args), emit: (...args) => emitted.push(args),
  }
  vm.createContext(state)
  vm.runInContext(code + '\nthis.toggle = toggleRotation', state)
  const pending = state.toggle(account)
  await state.toggle(account)
  assert.equal(calls.length, 1, 'Double-click must not send duplicate requests')
  assert.equal(calls[0].options.body.disabled, !initial)
  resolve({ ...account, rotation_disabled: !initial })
  await pending
  assert.equal(state.rows.value[0].rotation_disabled, !initial)
  assert.equal(state.rotationSaving.value.size, 0)
  assert.equal(emitted[0][0], 'reload')
  assert.equal(notices[0][1], 'success')
}
{
  const state = { rotationSaving: { value: new Set() }, rows: { value: [admin] }, api: async () => { throw new Error('save failed') }, setMessage: () => {}, emit: () => { throw new Error('Must not reload on failure') } }
  vm.createContext(state); vm.runInContext(code + '\nthis.toggle = toggleRotation', state)
  await state.toggle(admin)
  assert.equal(state.rows.value[0].rotation_disabled, true, 'Failed save must retain persisted state')
  assert.equal(state.rotationSaving.value.size, 0)
}
console.log('Admin scheduling frontend tests passed')
