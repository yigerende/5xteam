import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { ref } from 'vue'
import ts from 'typescript'

const component = readFileSync(new URL('../src/components/ProSubAccountLink.vue', import.meta.url), 'utf8')
const code = component.split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm, '')
function fixture() {
  const props = { account: { email: 'account+one@example.test' }, enabled: true, disabled: false }
  const events = [], requests = []
  const context = vm.createContext({ ref, defineProps: () => props, defineEmits: () => (...event) => events.push(event), api: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject })) })
  vm.runInContext(code + ';globalThis.test = {link,busy,error,message};', context)
  return { ...context.test, props, events, requests }
}

const f = fixture()
assert.equal(f.requests.length, 0, 'render must not query Sub')
const linking = f.link()
await f.link()
assert.equal(f.requests.length, 1, 'duplicate clicks must not submit a second association')
assert.equal(f.busy.value, true)
assert.equal(f.requests[0].url, '/api/pro-accounts/account%2Bone%40example.test/link-sub')
assert.equal(f.requests[0].options.method, 'POST')
const account = { email: f.props.account.email, push_status: 'completed', sub2_account_id: 42 }
f.requests[0].resolve(account)
await linking
assert.equal(f.busy.value, false)
assert.equal(f.message.value, '已关联，正在继续额度及后续流程')
assert.equal(f.events.find(([name]) => name === 'updated')[1], account)
assert.deepEqual(f.events.filter(([name]) => name === 'busy').map(([, value]) => value), [true, false])

const failed = fixture()
const failure = failed.link()
failed.requests[0].reject(new Error('同邮箱存在多个账号，请先处理重复账号'))
await failure
assert.equal(failed.requests.length, 1, 'failed writes must not be replayed')
assert.equal(failed.busy.value, false)
assert.match(failed.error.value, /多个账号/)
assert.equal(failed.events.some(([name]) => name === 'updated'), false)
const retry = failed.link()
assert.equal(failed.error.value, '')
failed.requests[1].resolve(account)
await retry
for (const flags of [{ enabled: false }, { disabled: true }]) {
  const blocked = fixture()
  Object.assign(blocked.props, flags)
  await blocked.link()
  assert.equal(blocked.requests.length, 0)
}

const source = readFileSync(new URL('../src/components/ProManagementView.vue', import.meta.url), 'utf8').split('<script setup>')[1].split('</script>')[0]
const ast = ts.createSourceFile('view.js', source, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const handler = ast.statements.filter(s => ts.isFunctionDeclaration(s) && ['linkedSubAccount','hasLinkedSubAuthorization','oauthLabel'].includes(s.name?.text)).map(s => s.getText(ast)).join('\n')
const startedAt = '2026-11-18T05:47:50Z', other = { email: 'other@example.test' }
const rows = ref([{ email: account.email, pro_push_started_at: startedAt }, other])
let polls = 0
const context = vm.createContext({ rows, settings: { sub2: { auth_mode: 'siwc' } }, schedulePoll: () => polls++ })
vm.runInContext(handler, context)
context.linkedSubAccount({ email: account.email }, account)
assert.equal(rows.value[0].sub2_account_id, 42)
assert.equal(rows.value[0].pro_push_started_at, startedAt, 'association UI must not reset push duration')
assert.equal(rows.value[1].email, other.email)
context.linkedSubAccount({ email: account.email }, { email: 'wrong@example.test', sub2_account_id: 99 })
assert.equal(rows.value[0].sub2_account_id, 42, 'a response for another account must not replace this row')
assert.equal(polls, 2, 'refresh only through existing page polling')
const linked = { ...account, push_provider: 'sub2', pro_auto: { oauth_source: 'sub_existing' } }
assert.equal(context.oauthLabel(linked), '已在 Sub 授权')
assert.equal(context.oauthLabel({ ...linked, siwc_present: true }), 'SIWC RT / AT 完整')
assert.equal(context.oauthLabel({ ...linked, sub2_account_id: 0 }), '待授权')
console.log('Passed: manual Sub association duplicate clicks, errors, row update, push duration and page refresh')
