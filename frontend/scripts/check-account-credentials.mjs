import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'

const source = readFileSync(new URL('../src/components/AccountCredentialsDialog.vue', import.meta.url), 'utf8')
const script = source.split('<script setup>')[1].split('</script>')[0]
const parsed = ts.createSourceFile('dialog.js', script, ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const code = parsed.statements.filter(node => !ts.isImportDeclaration(node)).map(node => node.getText(parsed)).join('\n')
const credentials = { revision: 'test-revision', access_token: 'test-at', refresh_token: 'test-rt', gpt_password: 'test-password', totp_secret: 'test-secret', chatgpt_session: { accessToken: 'session-at' } }
function fixture(request = async () => credentials) {
  const calls = [], copied = [], downloads = [], events = []
  const context = vm.createContext({
    reactive: value => value,
    computed: fn => ({ get value() { return fn() } }),
    defineEmits: () => (...args) => events.push(args),
    defineExpose: () => {},
    onBeforeUnmount: fn => { context.unmount = fn },
    window: { confirm: () => true, clearInterval() {}, setInterval: () => 1, setTimeout: () => 1 },
    performance: { now: () => 0 },
    copyText: async value => { copied.push(value) },
    api: async (...args) => { calls.push(args); return request(...args) },
    downloads,
  })
  vm.runInContext(code + '\ndownloadJSON = (...args) => downloads.push(args); this.view = { openCredentialDialog, closeCredentialDialog, saveCredentials, parseSessionAccessToken, showTotpCode, hasChanges, copyCredential, exportCredentialFormat, credentialDialog, totpCode }', context)
  return { ...context.view, calls, copied, downloads, events, unmount: context.unmount }
}
const view = fixture()
assert.equal(view.calls.length, 0, 'No credential fetch before clicking')
await view.openCredentialDialog({ email: 'pro+fixture@example.com' })
assert.equal(view.calls[0][0], '/api/mail/accounts/pro%2Bfixture%40example.com/credentials')
for (const [kind, expected] of [['at', 'test-at'], ['rt', 'test-rt'], ['password', 'test-password'], ['totp', 'test-secret'], ['session', JSON.stringify(credentials.chatgpt_session, null, 2)]]) {
  await view.copyCredential(kind)
  assert.equal(view.copied.at(-1), expected)
}
for (const format of ['cpa', 'sub2']) {
  await view.exportCredentialFormat(format)
  assert.equal(view.calls.at(-1)[0].endsWith('?format=' + format), true)
  assert.equal(view.downloads.at(-1)[0], 'pro_fixture@example.com-' + (format === 'cpa' ? 'cpa-auth' : 'sub2api-account') + '.json')
}
view.unmount()
assert.equal(view.credentialDialog.accessToken, '')
assert.equal(view.credentialDialog.refreshToken, '')
assert.equal(view.credentialDialog.totpSecret, '')
assert.equal(view.credentialDialog.open, false)

{
  const missing = fixture(async () => ({}))
  await missing.openCredentialDialog({ email: 'empty@example.com' })
  await missing.copyCredential('at')
  await missing.exportCredentialFormat('cpa')
  assert.equal(missing.calls.length, 1)
  assert.equal(missing.copied.length, 0)
  assert.equal(missing.downloads.length, 0)
}
{
  let resolve
  const switched = fixture(path => path.includes('first') ? new Promise(done => { resolve = done }) : Promise.resolve({ access_token: 'second-at' }))
  const first = switched.openCredentialDialog({ email: 'first@example.com' })
  await switched.openCredentialDialog({ email: 'second@example.com' })
  resolve(credentials)
  await first
  assert.equal(switched.credentialDialog.accessToken, 'second-at', 'Late credentials leaked to another account')
}
{
  let resolve
  const exporting = fixture(path => path.includes('?format=') ? new Promise(done => { resolve = done }) : Promise.resolve(credentials))
  await exporting.openCredentialDialog({ email: 'first@example.com' })
  const pending = exporting.exportCredentialFormat('cpa')
  await exporting.exportCredentialFormat('cpa')
  assert.equal(exporting.calls.length, 2, 'Duplicate exports must not run')
  exporting.closeCredentialDialog()
  await exporting.openCredentialDialog({ email: 'second@example.com' })
  resolve(credentials)
  await pending
  assert.equal(exporting.downloads.length, 0, 'A closed dialog must not download stale account credentials')
  assert.equal(exporting.credentialDialog.email, 'second@example.com')
}
{
  const failed = fixture(async () => { throw new Error('credentials unavailable') })
  await failed.openCredentialDialog({ email: 'missing@example.com' })
  assert.equal(failed.credentialDialog.error, 'credentials unavailable')
  assert.equal(failed.credentialDialog.loading, false)
}
for (const component of ['MailManagementView.vue', 'ProManagementView.vue']) {
  const parent = readFileSync(new URL('../src/components/' + component, import.meta.url), 'utf8')
  assert.ok(parent.includes('<AccountCredentialsDialog ref="credentialViewer"'), component + ' must reuse the same dialog')
}
console.log('Shared mail/Pro credentials dialog tests passed')

{
  const stored = { ...credentials }
  const edit = fixture(async (path, options) => {
    if (options?.method === 'PATCH') Object.assign(stored, options.body)
    return stored
  })
  await edit.openCredentialDialog({ email: 'edit@example.com' })
  await edit.saveCredentials()
  assert.equal(edit.calls.length, 1, 'Unchanged data must not be saved')
  edit.credentialDialog.refreshToken = 'edited-rt'
  edit.credentialDialog.chatgptSession = '{"accessToken":"new-session"}'
  await edit.exportCredentialFormat('cpa')
  assert.equal(edit.calls.length, 1, 'Unsaved edits must not export stale data')
  await edit.saveCredentials()
  const patch = edit.calls.find(([, options]) => options?.method === 'PATCH')[1].body
  assert.deepEqual(JSON.parse(JSON.stringify(patch)), { revision: 'test-revision', refresh_token: 'edited-rt', chatgpt_session: '{"accessToken":"new-session"}' })
  assert.equal(edit.hasChanges.value, false)
  assert.equal(edit.credentialDialog.saved, true)
  await edit.copyCredential('rt')
  assert.equal(edit.copied.at(-1), 'edited-rt')
  assert.ok(edit.events.some(([name]) => name === 'saved'))
  edit.credentialDialog.totpSecret = 'changed-secret'
  const count = edit.calls.length
  await edit.showTotpCode()
  assert.equal(edit.calls.length, count, 'Unsaved 2FA must not request old secret code')
}
{
  let resolve
  const edit = fixture((path, options) => options?.method === 'PATCH' ? new Promise(done => { resolve = done }) : Promise.resolve(credentials))
  await edit.openCredentialDialog({ email: 'edit@example.com' })
  edit.credentialDialog.accessToken = 'changed-at'
  const pending = edit.saveCredentials()
  await edit.saveCredentials()
  edit.closeCredentialDialog()
  assert.equal(edit.credentialDialog.open, true, 'Saving dialog should not close')
  assert.equal(edit.calls.length, 2, 'Duplicate save must not run')
  edit.unmount()
  resolve({})
  await pending
  assert.equal(edit.credentialDialog.open, false)
  assert.equal(edit.credentialDialog.accessToken, '')
}
{
  const edit = fixture(async (path, options) => { if (options?.method === 'PATCH') throw new Error('conflict'); return credentials })
  await edit.openCredentialDialog({ email: 'edit@example.com' })
  edit.credentialDialog.gptPassword = 'edited-password'
  await edit.saveCredentials()
  assert.equal(edit.credentialDialog.error, 'conflict')
  assert.equal(edit.credentialDialog.gptPassword, 'edited-password')
  assert.equal(edit.credentialDialog.saving, false)
  assert.equal(edit.hasChanges.value, true)
}
console.log('Credential editing, save isolation, conflict and duplicate-save tests passed')
{
  const edit = fixture()
  await edit.openCredentialDialog({ email: 'session@example.com' })
  for (const key of ['accessToken', 'access_token']) {
    edit.credentialDialog.chatgptSession = JSON.stringify({ [key]: 'parsed-at-' + key })
    edit.parseSessionAccessToken()
    assert.equal(edit.credentialDialog.accessToken, 'parsed-at-' + key)
  }
  for (const raw of ['{', '[]', 'null', '{"user":{}}', '{"accessToken":123}', '']) {
    edit.credentialDialog.chatgptSession = raw
    edit.parseSessionAccessToken()
    assert.equal(edit.credentialDialog.accessToken, 'parsed-at-access_token', 'Invalid or missing session token must not clear AT')
  }
}
console.log('Session AT extraction tests passed')
