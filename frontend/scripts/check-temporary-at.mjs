import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { runMailAccountTask, mailTaskTerminal } from '../src/mailAccountTask.js'

for (const mode of ['login', 'oauth']) {
  const calls = [], updates = []
  const states = [{ job: { job_id: 'id+1', status: 'queued' } }, { state: 'running' }, { status: 'success' }]
  await runMailAccountTask({ email: 'pro+test@example.com' }, mode, job => updates.push(job), { request: async (...args) => { calls.push(args); return states.shift() }, wait: async () => {} })
  assert.equal(calls[0][0], `/api/mail/accounts/pro%2Btest%40example.com/${mode}`)
  assert.equal(calls[0][1].method, 'POST')
  assert.equal(calls[1][0], `/api/mail/${mode}/id%2B1`)
  assert.equal(calls[1][1].cache, 'no-store')
  assert.equal(updates.length, 3)
}
for (const state of ['failed', 'cancelled', 'challenge']) {
  assert.ok(mailTaskTerminal({ state }))
  await assert.rejects(runMailAccountTask({ email: 'a@example.com' }, 'login', () => {}, { request: async () => ({ job_id: 'id', state, error: 'fixture failure' }) }), /fixture failure/)
}
await assert.rejects(runMailAccountTask({ email: 'a@example.com' }, 'login', () => {}, { request: async () => ({}) }), /任务 ID/)
{
  const controller = new AbortController()
  let calls = 0
  const task = runMailAccountTask({ email: 'a@example.com' }, 'login', () => {}, { signal: controller.signal, request: async () => { calls++; return { job_id: 'id', status: 'running' } } })
  controller.abort()
  await assert.rejects(task, { name: 'AbortError' })
  assert.equal(calls, 1, 'Unmount must stop polling without re-posting')
}
{
  let calls = 0
  await assert.rejects(runMailAccountTask({ email: 'a@example.com' }, 'login', () => {}, { request: async () => { calls++; throw new Error('network') } }), /network/)
  assert.equal(calls, 1, 'Never retry ambiguous creation failure')
}

const source = readFileSync(new URL('../src/components/TemporaryATDialog.vue', import.meta.url), 'utf8')
const script = source.split('<script setup>')[1].split('</script>')[0]
const parsed = ts.createSourceFile('dialog.js', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const code = parsed.statements.filter(node => !ts.isImportDeclaration(node)).map(node => node.getText(parsed)).join('\n')
function fixture() {
  const calls = [], events = []
  let unmount
  const context = vm.createContext({ ref: value => ({ value }), reactive: value => value, computed: fn => ({ get value() { return fn() } }),
    defineEmits: () => (...args) => events.push(args), defineExpose() {}, onBeforeUnmount(fn) { unmount = fn }, AbortController,
    runMailAccountTask: async (task, mode, update, options) => new Promise((resolve, reject) => calls.push({ task, mode, update, options, resolve, reject })) })
  vm.runInContext(code + ';this.view={start,confirm,close,open,phase,tasks,counts}', context)
  return { ...context.view, calls, events, unmount }
}
{
  const f = fixture()
  f.start([{ email: 'one@example.com' }, { email: 'ONE@example.com' }, { email: 'two@example.com' }], true)
  assert.equal(f.tasks.value.length, 2)
  assert.equal(f.calls.length, 0, 'Confirmation must precede login')
  const pending = f.confirm()
  await f.confirm()
  f.start([{ email: 'three@example.com' }])
  f.close()
  assert.equal(f.calls.length, 2, 'Parallel selected-only requests, no duplicate starts')
  assert.equal(f.open.value, true)
  f.calls[0].update({ status: 'running', logs: [{ message: '正在验证' }], result: { access_token: 'never-display' } })
  assert.equal(f.tasks.value[0].result, undefined)
  f.calls[0].resolve({ status: 'success' })
  f.calls[1].reject(new Error('fixture login failed'))
  await pending
  assert.equal(f.counts.ok, 1)
  assert.equal(f.counts.fail, 1)
  assert.equal(f.counts.done, 2)
  assert.equal(f.phase.value, 'done')
  assert.equal(f.tasks.value[1].error, 'fixture login failed')
  assert.equal(f.events.filter(e => e[0] === 'finished').length, 1)
  f.close()
  assert.equal(f.open.value, false)
}
{
  const f = fixture()
  f.start([{ email: 'one@example.com' }])
  const pending = f.confirm()
  f.unmount()
  assert.equal(f.calls[0].options.signal.aborted, true)
  f.calls[0].resolve({ status: 'success' })
  await pending
  assert.equal(f.counts.done, 0)
  assert.equal(f.events.filter(e => e[0] === 'finished').length, 0, 'No late completion after unmount')
}
console.log('Temporary AT: shared Mail/Pro endpoints, OAuth regression, terminal errors, no POST retry, abort, confirmation, selected-only parallelism, deduplication, progress, failure isolation and teardown passed')
