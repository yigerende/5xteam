import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import ts from 'typescript'
import { ref, reactive } from 'vue'
import { createLatestRequest } from '../src/latestRequest.js'
import { formatTime } from '../src/utils.js'

const tick = () => new Promise(resolve => setImmediate(resolve))
function deferred() {
  let resolve, reject
  const promise = new Promise((ok, fail) => { resolve = ok; reject = fail })
  return { promise, resolve, reject }
}

for (const view of ['MailManagementView', 'FreePipelineView']) {
  const source = readFileSync(new URL(`../src/components/${view}.vue`, import.meta.url), 'utf8')
  const parsed = ts.createSourceFile('View.js', source.split('<script setup>')[1].split('</script>')[0], ts.ScriptTarget.Latest, true, ts.ScriptKind.JS)
  const team = view === 'FreePipelineView'
  const names = new Set(['loadAccounts', 'refreshLiveAccounts', 'runTracked'])
  const vars = new Set(['accountsLoading', 'renderedAccountPage', 'accountsRequest', 'activityRefreshTimer', 'viewDisposed'])
  const code = parsed.statements.filter(statement => ts.isFunctionDeclaration(statement)
    ? names.has(statement.name?.text)
    : ts.isVariableStatement(statement) && statement.declarationList.declarations.some(declaration => vars.has(declaration.name.getText(parsed))))
    .map(statement => statement.getText(parsed)).join('\n')
  const requests = [], timers = new Map()
  let timerID = 0, emitted = 0
  const accounts = ref([]), page = ref(1), pageSize = ref(10), filter = ref(team ? '' : 'all')
  const context = vm.createContext({
    ref, reactive, createLatestRequest, URLSearchParams, Date,
    accounts, liveAccounts: accounts, accountPage: page, page, accountPageSize: pageSize, pageSize,
    spaceFilter: filter, teamSpaceFilter: filter, accountQuery: ref(''), pipelineAccounts: ref([]),
    accountTotal: ref(0), counts: ref({}), spaceFilterCounts: {}, accountSummary: {}, serverClockOffset: ref(0),
    activities: {}, operationMeta: { quota: { stage: 'quota' } },
    syncActivityStage() {}, activityText: () => 'running', setMessage() {}, emit: () => { emitted++ },
    window: {
      setInterval(fn, delay) { const id = ++timerID; timers.set(id, { fn, delay }); return id },
      clearInterval(id) { timers.delete(id) },
    },
    api(url, { signal }) {
      const request = { ...deferred(), url, signal }
      requests.push(request)
      // Intentionally allow aborted requests to finish out of order.
      return request.promise
    },
  })
  vm.runInContext(code + '\nglobalThis.listRequest = accountsRequest; globalThis.loading = accountsLoading; globalThis.renderedPage = renderedAccountPage;', context)
  const refresh = team ? context.refreshLiveAccounts : context.loadAccounts
  const response = label => ({ items: [{ id: label }], total: 5, summary: { all: 5 }, counts: { all: 5 } })

  filter.value = 'outside'
  const outside = refresh()
  await tick()
  filter.value = 'inside'
  const inside = refresh()
  await tick()
  assert.equal(requests[0].signal.aborted, true)
  assert.match(requests[1].url, /space_state=inside/)
  requests[1].resolve(response('inside'))
  await inside
  requests[0].resolve(response('outside'))
  await outside
  assert.equal(accounts.value[0].id, 'inside', `${view}: stale filter replaced current rows`)
  assert.match(context.renderedPage.value, /space_state=inside/)
  assert.equal(context.loading.value, false)

  const pending = refresh()
  await tick()
  const last = requests.at(-1)
  if (team) {
    const size = requests.length
    const polls = Array.from({ length: 20 }, () => refresh({ background: true }))
    await tick()
    assert.equal(requests.length, size, 'background refresh duplicated an in-flight list query')
    assert.equal(last.signal.aborted, false, 'poll aborted a user request')
    last.resolve(response('polled'))
    await Promise.all([pending, ...polls])
  } else {
    last.resolve(response('refreshed'))
    await pending
  }

  page.value = 3
  const fallback = refresh()
  await tick()
  requests.at(-1).resolve(response('empty-page'))
  await tick()
  assert.equal(page.value, 1)
  assert.match(requests.at(-1).url, /page=1&/)
  requests.at(-1).resolve(response('last-page'))
  await fallback
  assert.equal(accounts.value[0].id, 'last-page')

  const failure = refresh()
  await tick()
  requests.at(-1).reject(new Error('list failed'))
  await assert.rejects(failure, /list failed/)
  assert.equal(context.loading.value, false)

  if (team) {
    const operations = Array.from({ length: 20 }, deferred)
    const jobs = operations.map((op, i) => context.runTracked({ id: String(i), email: `${i}@example.invalid` }, 'quota', () => op.promise))
    assert.equal(timers.size, 1, 'parallel operations must share one poll timer')
    const timer = [...timers.values()][0]
    assert.equal(timer.delay, 800)
    const priorRequests = requests.length
    for (let i = 0; i < 20; i++) timer.fn()
    await tick()
    assert.equal(requests.length, priorRequests + 1, 'slow responses caused overlapping polling')
    requests.at(-1).resolve(response('running'))
    await tick()
    for (let i = 0; i < operations.length; i++) {
      operations[i].resolve(i)
      await tick()
      requests.at(-1).resolve(response(`finished-${i}`))
      await jobs[i]
      assert.equal(timers.size, i === operations.length - 1 ? 0 : 1)
    }
    assert.equal(emitted, 20, 'completion reload events must be preserved')
  }

  const unloading = refresh()
  await tick()
  const beforeUnload = accounts.value[0].id
  const requestCount = requests.length
  context.listRequest.dispose()
  assert.equal(requests.at(-1).signal.aborted, true)
  requests.at(-1).resolve(response('unmounted'))
  await unloading
  await refresh()
  assert.equal(accounts.value[0].id, beforeUnload)
  assert.equal(requests.length, requestCount, 'disposed view started a new request')
}
for (const value of ['2026-09-20T12:00:00Z', '2026-01-01T00:00:00+08:00', new Date('2000-02-29T10:10:10Z'), '', null]) {
  const expected = value ? new Intl.DateTimeFormat('zh-CN', {
    month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false,
    timeZone: 'Asia/Shanghai',
  }).format(new Date(value)) : '-'
  assert.equal(formatTime(value), expected, 'timestamp format changed')
}
console.log('Passed: current filter wins, cancellation, shared polling, no overlap, pagination fallback, errors, and unmount cleanup')
