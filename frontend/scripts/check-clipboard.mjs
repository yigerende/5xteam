import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'

const source = readFileSync(new URL('../src/clipboard.js', import.meta.url), 'utf8').replace('export async function', 'async function')
function fixture({ secure = true, modern = 'ok', legacy = true } = {}) {
  const writes = [], commands = [], nodes = []
  const active = { isConnected: true, selectionStart: 2, selectionEnd: 5, selectionDirection: 'backward', focus() { this.focused = true }, setSelectionRange(...range) { this.restored = range } }
  const document = {
    activeElement: active,
    body: { appendChild(node) { nodes.push(node) } },
    createElement() { return { style: {}, value: '', setAttribute() {}, focus() {}, select() { this.selected = true }, setSelectionRange(start, end) { this.range = [start, end] }, remove() { this.removed = true } } },
    execCommand(command) {
      const node = nodes.at(-1)
      assert.equal(node.selected, true)
      assert.deepEqual(node.range, [0, node.value.length])
      commands.push([command, node.value])
      if (legacy === 'throw') throw new Error('blocked')
      return legacy
    },
  }
  const navigator = modern === 'absent' ? {} : { clipboard: { async writeText(value) { writes.push(value); if (modern === 'denied') throw new Error('NotAllowedError') } } }
  const context = vm.createContext({ window: { isSecureContext: secure }, document, navigator })
  vm.runInContext(source + '; this.copy = copyText', context)
  return { copy: context.copy, writes, commands, nodes, active }
}
const sample = '密码 中文 000123\n' + 'AT'.repeat(10000) + '\n{"accessToken":"fixture"}'
{
  const f = fixture()
  await f.copy(sample)
  assert.deepEqual(f.writes, [sample])
  assert.equal(f.nodes.length, 0)
}
for (const config of [{ secure: false, modern: 'absent' }, { secure: true, modern: 'denied' }, { secure: true, modern: 'absent' }, { secure: false, modern: 'ok' }]) {
  const f = fixture(config)
  await f.copy(sample)
  assert.deepEqual(f.commands, [['copy', sample]])
  assert.equal(f.nodes[0].value, '', 'Temporary secret must be cleared')
  assert.equal(f.nodes[0].removed, true)
  assert.equal(f.active.focused, true)
  assert.deepEqual(f.active.restored, [2, 5, 'backward'])
}
for (const legacy of [false, 'throw']) {
  const f = fixture({ secure: false, modern: 'absent', legacy })
  await assert.rejects(f.copy(sample))
  assert.equal(f.nodes[0].value, '')
  assert.equal(f.nodes[0].removed, true)
  assert.equal(f.active.focused, true)
}
{
  const f = fixture({ secure: false, modern: 'absent' })
  await f.copy('')
  assert.equal(f.nodes.length, 0)
}
for (const component of ['AccountCredentialsDialog', 'MailManagementView', 'ProManagementView', 'AdminAccountsView', 'SmsManagementView']) {
  const view = readFileSync(new URL('../src/components/' + component + '.vue', import.meta.url), 'utf8')
  assert.ok(view.includes('await copyText('), component + ' must use compatible copy')
  assert.ok(!view.includes('navigator.clipboard.writeText'), component + ' bypasses fallback')
}
console.log('Clipboard: HTTPS, HTTP/IP, permission fallback, Unicode/long text, selection restoration, cleanup and failure paths passed')
