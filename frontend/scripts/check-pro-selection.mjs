import assert from 'node:assert/strict'
import vm from 'node:vm'
import { readFileSync } from 'node:fs'
import ts from 'typescript'
import { computed, ref } from 'vue'
const source = readFileSync(new URL('../src/components/ProManagementView.vue', import.meta.url), 'utf8')
const parsed = ts.createSourceFile('View.js', source.split('<script setup>')[1].split('</script>')[0], ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
const names = new Set(['clearSelection', 'selectUnloggedAccounts', 'setPage', 'setPageSize', 'setSpaceFilter', 'toggle', 'toggleVisible', 'runBatch'])
const vars = new Set(['selectedEmails', 'crossPageSelection', 'selectionRevision', 'selectedAccounts', 'displayedAccounts', 'allVisibleSelected'])
const code = parsed.statements.filter(s => ts.isFunctionDeclaration(s) ? names.has(s.name?.text) : ts.isVariableStatement(s) && s.declarationList.declarations.some(d => vars.has(d.name.getText(parsed)))).map(s => s.getText(parsed)).join('\n')
const rows = ref([{ email: 'old@example.com' }]), query = ref(''), spaceFilter = ref('unmerged'), busy = ref('')
const requests = [], messages = []
const c = vm.createContext({ rows, query, spaceFilter, busy, ref, computed, Set, Map, URLSearchParams, disposed: false, page: ref(1), pageSize: ref(10), searchTimer: 0, window: { clearTimeout() {} }, loadPageHandled() {}, reload() {}, setManualStage() {}, setMessage: (...args) => messages.push(args), api: (url, options) => new Promise((resolve, reject) => requests.push({ url, options, resolve, reject })) })
vm.runInContext(code + '\nglobalThis.state={selectedEmails,crossPageSelection,selectedAccounts,allVisibleSelected};', c)
c.toggle(rows.value[0])
let pending = c.selectUnloggedAccounts()
assert.equal(requests[0].options, undefined, 'selecting must not submit a workflow')
const emails = Array.from({ length: 27 }, (_, i) => `new-${i}@example.com`)
requests[0].resolve({ emails }); await pending
assert.equal(c.state.selectedEmails.value.has('old@example.com'), false)
assert.equal(c.state.selectedAccounts.value.length, 27, 'all pages must be actionable')
c.setPage(3); rows.value = [{ email: emails[26] }]
assert.equal(c.state.selectedAccounts.value.length, 27)
assert.equal(c.state.allVisibleSelected.value, true)
c.toggleVisible(); assert.equal(c.state.selectedAccounts.value.length, 26)
c.toggleVisible(); assert.equal(c.state.selectedAccounts.value.length, 27)
c.setPageSize(20); assert.equal(c.state.selectedAccounts.value.length, 27)
pending = c.runBatch('quota')
assert.equal(requests.at(-1).options.body.emails.length, 27, 'batch action must include off-page selection')
requests.at(-1).resolve({ succeeded: 27, failed: 0 }); await pending
c.clearSelection(); assert.equal(c.state.selectedAccounts.value.length, 0)
pending = c.selectUnloggedAccounts()
c.setSpaceFilter('in_progress'); requests.at(-1).resolve({ emails }); await pending
assert.equal(c.state.selectedAccounts.value.length, 0, 'stale tab response must not select accounts')
c.setSpaceFilter('unmerged'); query.value = 'new-2'
pending = c.selectUnloggedAccounts()
assert.match(requests.at(-1).url, /query=new-2/)
query.value = 'changed'; requests.at(-1).resolve({ emails }); await pending
assert.equal(c.state.selectedAccounts.value.length, 0, 'stale search must not select accounts')
pending = c.selectUnloggedAccounts(); requests.at(-1).reject(new Error('offline')); await pending
assert.equal(busy.value, '')
assert.equal(messages.at(-1)[0], 'offline')
c.toggle({ email: 'manual@example.com' }); c.setPage(2)
assert.equal(c.state.selectedAccounts.value.length, 0, 'ordinary page selection keeps its existing behavior')
console.log('Passed: all-page selection, batch targets, paging, deselection, filters, stale responses and failures')
