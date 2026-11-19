import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { ref, reactive, computed, watch } from 'vue'
import { createLatestRequest } from '../src/latestRequest.js'
import { forecastDetails, concurrencyDetails } from '../src/restockForecast.js'
import { restockMonitorStatus } from '../src/restockMonitor.js'
import { restockDisplay } from '../src/restockDisplay.js'
import { restockTaskActive, restockTaskProgress, restockTaskRows, restockTaskTimeline } from '../src/restockTasks.js'

const tick = () => new Promise(resolve => setImmediate(resolve))
const source = readFileSync(new URL('../src/components/ProRestockView.vue', import.meta.url), 'utf8')
const code = source.split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm, '')
function fixture() {
  const requests = [], timers = new Map()
  let mounted, unmounted, timerID = 0
  const context = vm.createContext({
    defineProps:()=>({defaultPageSize:50}), ref, reactive, computed, watch, createLatestRequest, forecastDetails, concurrencyDetails, nextTick: tick,
    restockTaskActive, restockTaskProgress, restockTaskRows, restockTaskTimeline, restockMonitorStatus, restockDisplay,
    window: {confirm:()=>true},
    document: {activeElement: {focus() {}}},
    onMounted(fn) { mounted = fn }, onUnmounted(fn) { unmounted = fn },
    setInterval(fn) { const id = ++timerID; timers.set(id, fn); return id },
    clearInterval(id) { timers.delete(id) },
    api(url, options = {}) {
      let resolve, reject
      const promise = new Promise((ok, fail) => { resolve = ok; reject = fail })
      requests.push({ url, options, resolve: value => resolve(url.includes('/records?') && Array.isArray(value) ? { items:value, total:value.length?100:0 } : value), reject })
      return promise
    },
  })
  vm.runInContext(code + '\nglobalThis.exposed = {busy, recordsError, state, form, records, page, refresh, pause, save, evaluate, resetBreaker, loadRecords, error, collectionError, notice, monitorUnavailable, noRestockActive, saveWindow, windowDraft, windowEdited, savingWindow, dirty, forecastReport, concurrencyReport, showDetail, closeDetail, loadDetailTasks, detail, detailLoading, detailError, dialog, applyConfig, addNoRestockWindow, removeNoRestockWindow, scheduleSummary, taskFilter, taskSearch, taskPage, taskPageSize, taskRows, visibleTasks, taskDetail, stopTask, stoppingTask, canStopTask};', context)
  return { requests, timers, mounted: () => mounted(), unmounted: () => unmounted(), ...context.exposed }
}

// Task pagination has independent state, uses global size, clamps after live
// updates, and a displayed task detail follows its own current task only.
{
 const f=fixture()
 f.state.value={tasks:Array.from({length:53},(_,i)=>({id:String(i),email:`account-${i}@example.test`,status:'ready',created_at:new Date(1700000000000+i*60000).toISOString()}))}
 assert.equal(f.taskPageSize.value,50)
 assert.equal(f.visibleTasks.value.length,50)
 assert.equal(f.visibleTasks.value[0].id,'52')
 f.page.value=4;f.taskPage.value=2
 assert.equal(f.visibleTasks.value.length,3)
 f.taskFilter.value='active';await tick()
 assert.equal(f.taskPage.value,1)
 assert.equal(f.page.value,4,'task filter must not change decision pagination')
 assert.equal(f.visibleTasks.value.length,0)
 f.taskFilter.value='all';await tick();f.taskPage.value=2
 f.state.value={tasks:[{id:'latest',email:'latest@example.test',status:'running',stages:{'login:running':'2026-10-20T12:00:00+08:00'}}]}
 await tick();assert.equal(f.taskPage.value,1)
 f.detail.value={id:'latest',email:'latest@example.test',status:'running'}
 f.state.value={tasks:[{id:'latest',email:'latest@example.test',status:'ready',ready_at:'2026-10-20T12:05:00+08:00'}]}
 await tick();assert.equal(f.taskDetail.value.status,'补货成功')
 assert.equal(f.taskDetail.value.success,'2026-10-20T12:05:00+08:00')
 f.unmounted()
}

// An initial request completing after navigation must not resurrect polling.
{
  const f = fixture()
  const mount = f.mounted()
  await tick()
  f.unmounted()
  f.requests[0].resolve({ config: { enabled: true, version: 1 } })
  await tick()
  for (const r of f.requests.slice(1)) r.resolve([])
  await mount
  assert.equal(f.timers.size, 0, 'unmounted page recreated polling')
  assert.equal(f.requests.length, 1, 'unmounted page started another request')
  assert.equal(f.state.value, null, 'unmounted page accepted an old response')
}

// A late GET cannot undo the state returned by a pause/evaluate/reset action.
for (const action of ['pause', 'evaluate', 'resetBreaker']) {
  const f = fixture()
  f.state.value = { config: { enabled: true, version: 1 } }
  const oldPoll = f.refresh()
  await tick()
  const old = f.requests[0]
  const operation = f[action]()
  await tick()
  const mutation = f.requests.find(r => r.options.method)
  assert.ok(mutation, 'missing action request')
  const config = { enabled: false, version: 2 }
  mutation.resolve(action === 'pause' ? config : { config })
  await tick()
  for (const r of f.requests) {
    if (r === old || r === mutation) continue
    r.resolve(r.url.includes('/records') ? [] : { config })
  }
  await tick()
  for (const r of f.requests.filter(r => r.url.includes('/records'))) r.resolve([])
  await operation
  old.resolve({ config: { enabled: true, version: 1 } })
  await tick()
  for (const r of f.requests.filter(r => r.url.includes('/records'))) r.resolve([])
  await oldPoll
  assert.equal(f.state.value.config.version, 2, `${action}: old response replaced new state`)
  if (action === 'pause') assert.equal(f.form.version, 2, 'pause did not refresh settings form')
  f.unmounted()
}

// Paging cannot be overwritten by an older page's records response.
{
  const f = fixture()
  const first = f.loadRecords()
  await tick()
  assert.match(f.requests[0].url, /page_size=50/, 'records must use the global page size')
  f.page.value = 2
  const second = f.loadRecords()
  await tick()
  f.requests[1].resolve([{ id: 'page-2' }])
  await second
  f.requests[0].resolve([{ id: 'page-1' }])
  await first
  assert.equal(f.records.value[0].id, 'page-2', 'old page replaced current records')
  f.unmounted()
}
// Manual retry reports the new collection result and preserves the saved mode.
for (const scenario of ['failed', 'load_failed', 'stale', 'auto', 'observe', 'disabled', 'busy', 'no_restock']) {
  const f = fixture()
  f.state.value = { config: { enabled: true, mode: 'auto' }, snapshot: { valid: false } }
  assert.equal(f.monitorUnavailable.value, true)
  const operation = f.evaluate()
  await tick()
  const mutation = f.requests[0]
  assert.equal(mutation.url, '/api/pro-restock/evaluate')
  if (scenario === 'busy') {
    mutation.reject(new Error('正在采集或更新补货配置，请稍后重试'))
  } else {
    mutation.resolve({
      no_restock_active: scenario === 'no_restock',
      no_restock_reason: '处于每日禁补时段，暂停新增补货',
      config: { enabled: scenario !== 'disabled', mode: scenario === 'observe' ? 'observe' : 'auto', fresh_seconds: 20 },
      snapshot: { valid: scenario !== 'failed', load_valid: scenario !== 'load_failed', at: new Date(Date.now() - (scenario === 'stale' ? 60000 : 0)).toISOString(), error: scenario === 'failed' ? '接口超时' : '' },
    })
    await tick()
    for (const r of f.requests.filter(r => r.url.includes('/records'))) r.resolve([])
  }
  await operation
  if (['failed', 'load_failed', 'stale', 'busy'].includes(scenario)) {
    assert.ok(f.error.value || f.collectionError.value, `${scenario}: missing failure feedback`)
    assert.equal(f.notice.value, '', `${scenario}: false success feedback`)
    assert.equal(f.monitorUnavailable.value, true)
  } else {
    assert.equal(f.error.value, '')
    assert.equal(f.monitorUnavailable.value, false)
    assert.match(f.notice.value, scenario === 'auto' ? /恢复规则判断/ : scenario === 'observe' ? /观察模式不会/ : scenario === 'no_restock' ? /每日禁补/ : /手动暂停/)
    assert.equal(f.noRestockActive.value, scenario === 'no_restock')
    if (scenario === 'disabled') assert.equal(f.state.value.config.enabled, false)
  }
  if (scenario === 'failed') {
    const poll = f.refresh(false, true)
    await tick()
    f.requests.findLast(r => r.url === '/api/pro-restock').resolve({ config: {enabled: true, mode: 'auto', fresh_seconds: 20}, snapshot: {valid: true, load_valid: true, at: new Date().toISOString()} })
    await tick()
    f.requests.findLast(r => r.url.includes('/records')).resolve([])
    await poll
    assert.equal(f.collectionError.value, '', 'automatic recovery left old manual retry failure visible')
    assert.equal(f.monitorUnavailable.value, false)
  }
  f.unmounted()
}
// Invalid windows are rejected before sending a configuration request.
for (const [start, end] of [['', '07:00'], ['7:00', '23:00'], ['24:00', '07:00'], ['23:00', '23:00']]) {
  const f = fixture()
  Object.assign(f.form, {no_restock_enabled: true, no_restock_start: start, no_restock_end: end})
  await f.save()
  assert.equal(f.requests.length, 0)
  assert.match(f.error.value, /每日禁补/)
  f.unmounted()
}
// Cross-midnight values survive save, subsequent polling, and form refresh.
{
  const f = fixture()
  const config = {enabled: false, version: 2, no_restock_enabled: true, no_restock_start: '23:00', no_restock_end: '07:00'}
  Object.assign(f.form, config, {version: 1})
  const saving = f.save()
  await tick()
  assert.equal(f.requests[0].url, '/api/pro-restock/config')
  assert.equal(f.requests[0].options.body.no_restock_start, '23:00')
  assert.equal(f.requests[0].options.body.no_restock_end, '07:00')
  f.requests[0].resolve(config)
  await tick()
  f.requests.findLast(r => r.url === '/api/pro-restock').resolve({config, no_restock_active: true})
  await tick()
  f.requests.findLast(r => r.url.includes('/records')).resolve([])
  await saving
  assert.equal(f.form.version, 2)
  assert.equal(f.form.no_restock_enabled, true)
  assert.equal(f.form.no_restock_end, '07:00')
  assert.equal(f.noRestockActive.value, true)
  assert.equal(f.error.value, '')
  f.unmounted()
}
console.log('Passed: restock request ordering, paging, retry recovery, and daily no-restock configuration')

// Every row is validated; editing drafts must not mutate the live saved config.
for (const windows of [[], [{start:'02:00',end:'08:00'},{start:'',end:'12:00'}], [{start:'02:00',end:'08:00'},{start:'12:00',end:'12:00'}]]) {
 const f=fixture();f.applyConfig({no_restock_enabled:true,no_restock_windows:windows})
 await f.save();assert.equal(f.requests.length,0);assert.match(f.error.value,/每日禁补/);f.unmounted()
}
{
 const f=fixture()
 const before={version:1,enabled:false,no_restock_enabled:true,money_window_minutes:3,no_restock_start:'02:00',no_restock_end:'08:00'}
 f.state.value={config:before};f.applyConfig(before)
 assert.equal(f.form.no_restock_windows[0].start,'02:00','legacy interval was lost')
 f.addNoRestockWindow();Object.assign(f.form.no_restock_windows[1],{start:'23:00',end:'01:00',allowed_provider:'nexovcc'})
 assert.equal(f.dirty.value,true)
 assert.match(f.scheduleSummary(f.form),/02:00～08:00 完全禁补、23:00～次日 01:00 仅环球/)
 const saving=f.save();await tick()
 const put=f.requests[0]
 assert.equal(JSON.stringify(put.options.body.no_restock_windows),JSON.stringify([{start:'02:00',end:'08:00',allowed_provider:'',takeover_enabled:false},{start:'23:00',end:'01:00',allowed_provider:'nexovcc',takeover_enabled:false}]))
 const saved={...put.options.body,version:2};put.resolve(saved);await tick()
 f.requests.findLast(r=>r.url==='/api/pro-restock').resolve({config:saved});await tick()
 f.requests.findLast(r=>r.url.includes('/records')).resolve([]);await saving
 assert.equal(f.form.no_restock_windows.length,2)
 assert.equal(f.form.no_restock_windows[1].allowed_provider,'nexovcc','issuer was lost on save/reload')
 f.form.no_restock_windows[1].start='22:00';f.dirty.value=true
 assert.equal(f.state.value.config.no_restock_windows[1].start,'23:00','draft mutated saved config')
 const poll=f.refresh();await tick();f.requests.findLast(r=>r.url==='/api/pro-restock').resolve({config:saved});await tick()
 f.requests.findLast(r=>r.url.includes('/records')).resolve([]);await poll
 assert.equal(f.form.no_restock_windows[1].start,'22:00','poll overwrote unsaved interval')
 f.windowDraft.value=4
 const inline=f.saveWindow();await tick()
 const inlinePut=f.requests.findLast(r=>r.options.method==='PUT')
 assert.equal(inlinePut.options.body.no_restock_windows[1].start,'23:00','inline save leaked schedule draft')
 inlinePut.reject(new Error('fixture stop'));await inline
 f.removeNoRestockWindow(0)
 assert.equal(f.form.no_restock_windows.length,1)
 assert.equal(f.form.no_restock_windows[0].start,'22:00')
 assert.equal(f.state.value.config.no_restock_windows.length,2,'remove mutated saved config')
 f.removeNoRestockWindow(0);assert.equal(f.form.no_restock_windows.length,1,'removed last interval')
 f.unmounted()
}
console.log('Passed: multiple pause windows, legacy migration, save, removal, and draft isolation')

// Inline window saves update only the saved config and never send other drafts.
for (const unsaved of [false,true]) {
 const f=fixture()
 const before={enabled:false,version:1,money_window_minutes:3,trigger_minutes:5}
 f.state.value={config:before};Object.assign(f.form,before)
 if(unsaved){f.form.trigger_minutes=9;f.dirty.value=true}
 f.windowDraft.value=5;f.windowEdited.value=true
 const saving=f.saveWindow();await tick()
 const put=f.requests[0]
 assert.equal(put.options.body.money_window_minutes,5)
 assert.equal(put.options.body.trigger_minutes,5,'inline save sent unrelated draft')
 assert.equal(put.options.body.enabled,false,'inline save enabled automatic replenishment')
 assert.equal(f.savingWindow.value,true)
 const config={...before,version:2,money_window_minutes:5}
 put.resolve(config);await tick()
 f.requests.findLast(r=>r.url==='/api/pro-restock').resolve({config})
 await tick();f.requests.findLast(r=>r.url.includes('/records')).resolve([]);await saving
 assert.equal(f.windowDraft.value,5);assert.equal(f.windowEdited.value,false)
 assert.equal(f.state.value.config.money_window_minutes,5)
 assert.equal(f.form.trigger_minutes,unsaved?9:5)
 assert.equal(f.form.version,unsaved?1:2,'unsaved form lost conflict protection')
 assert.equal(f.savingWindow.value,false)
 f.unmounted()
}
for(const value of [0,-1,61,1.5,'',NaN]){
 const f=fixture();f.state.value={config:{version:1}};f.windowDraft.value=value
 await f.saveWindow();assert.equal(f.requests.length,0);assert.match(f.error.value,/1～60/);f.unmounted()
}
{
 const f=fixture();f.state.value={config:{version:1,money_window_minutes:3}}
 f.windowDraft.value=5;f.windowEdited.value=true
 const poll=f.refresh();await tick();f.requests[0].resolve({config:{version:1,money_window_minutes:3}})
 await tick();f.requests.findLast(r=>r.url.includes('/records')).resolve([]);await poll
 assert.equal(f.windowDraft.value,5,'poll overwrote window input')
 const saving=f.saveWindow();await tick();f.requests.findLast(r=>r.options.method).reject(new Error('配置已被其他页面修改'))
 await saving
 assert.equal(f.windowDraft.value,5);assert.equal(f.state.value.config.money_window_minutes,3)
 assert.match(f.error.value,/其他页面/);assert.equal(f.savingWindow.value,false);f.unmounted()
}
console.log('Passed: editable money window, bounds, draft isolation, polling and save-failure behavior')

// Detail requests belong to a single opening, including reopening the same ID.
for (const finish of ['success', 'failure']) {
 const f=fixture();let opened=0
 f.dialog.value={open:false,showModal(){this.open=true;opened++},close(){this.open=false}}
 const item={id:'same-id',snapshot:{id:'s'},reason:'stored reason'}
 const first=f.showDetail(item);await tick();await tick()
 const old=f.requests[0]
 assert.equal(f.detailLoading.value,true)
 f.closeDetail()
 assert.equal(old.options.signal.aborted,true)
 const second=f.showDetail(item);await tick();await tick()
 f.requests[1].resolve([{id:'current',status:'ready'}]);await second
 if(finish==='success')old.resolve([{id:'old'}]);else old.reject(new Error('old error'))
 await first
 assert.equal(f.detail.value.related_tasks[0].id,'current')
 assert.equal(f.detailError.value,'')
 assert.equal(f.detailLoading.value,false)
 assert.equal(opened,2)
 assert.equal(item.related_tasks,undefined,'original record mutated')
 f.unmounted()
}
{
 const f=fixture();const item={id:'retry',snapshot:{id:'s'},reason:'saved evidence'}
 const opening=f.showDetail(item);await tick();await tick()
 f.requests[0].reject(new Error('network unavailable'));await opening
 assert.equal(f.error.value,'','detail failure leaked behind dialog')
 assert.match(f.detailError.value,/network unavailable/)
 assert.equal(f.detail.value.reason,'saved evidence')
 const retry=f.loadDetailTasks();await tick();f.requests[1].resolve([]);await retry
 assert.equal(f.detailError.value,'');assert.equal(f.detail.value.related_tasks.length,0)
 const pending=f.loadDetailTasks();await tick();f.unmounted()
 assert.equal(f.requests[2].options.signal.aborted,true)
 f.requests[2].resolve([{id:'after-unmount'}]);await pending
 assert.equal(f.detail.value.related_tasks.length,0)
}
{
 const f=fixture();let opened=false
 f.dialog.value={showModal(){opened=true},close(){}}
 const opening=f.showDetail({id:'close-before-tick',snapshot:{id:'s'}})
 f.closeDetail();await opening
 assert.equal(opened,false);assert.equal(f.requests.length,0)
 f.unmounted()
}
console.log('Passed: detail cancellation, reopening, loading, retry and unmount isolation')

// Capacity percentage is validated before saving and survives refresh/polling.
for(const percent of [0,-1,101,60.5,'']){
 const f=fixture();f.form.capacity_percent=percent;await f.save()
 assert.equal(f.requests.length,0)
 assert.match(f.error.value,/总并发容量使用比例/)
 f.unmounted()
}
{
 const f=fixture(),config={version:2,enabled:false,capacity_percent:60,trigger_percent:70}
 Object.assign(f.form,config,{version:1})
 const saving=f.save();await tick()
 assert.equal(f.requests[0].options.body.capacity_percent,60)
 f.requests[0].resolve(config);await tick()
 const at=new Date().toISOString()
 f.requests.findLast(r=>r.url==='/api/pro-restock').resolve({config,server_time:at,snapshot:{valid:true,load_valid:true,at,accounts:[{id:1,ready:true,capacity:80,used:33}]}})
 await tick();f.requests.findLast(r=>r.url.includes('/records')).resolve([]);await saving
 assert.equal(f.form.capacity_percent,60)
 assert.equal(f.concurrencyReport.value.capacity,48)
 assert.equal(f.concurrencyReport.value.triggerCount,33)
 assert.equal(f.concurrencyReport.value.percent,'68.8%')
 f.unmounted()
}
console.log('Passed: capacity percentage validation, save round-trip and live dashboard recalculation')

// Daily scheduling fields survive drafts and saves independently of replenishment.
{
 const f=fixture()
 const config={version:1,enabled:false,mode:'observe',capacity_percent:60,money_window_minutes:3,no_restock_enabled:true,no_restock_windows:[{start:'17:00',end:'20:50'}],daily_ejh_merge_enabled:true,daily_ejh_merge_time:'17:30'}
 f.applyConfig(config);f.state.value={config};f.dirty.value=true
 f.form.daily_ejh_merge_time='24:00';await f.save()
 assert.equal(f.requests.length,0,'invalid daily schedule sent to server')
 assert.match(f.error.value,/定点合并时间/)
 f.form.daily_ejh_merge_time='17:30'
 const saving=f.save();await tick()
 const request=f.requests[0]
 assert.equal(request.options.body.no_restock_fallback,undefined)
 assert.equal(request.options.body.daily_ejh_merge_enabled,true)
 assert.equal(request.options.body.daily_ejh_merge_time,'17:30')
 assert.deepEqual(JSON.parse(JSON.stringify(request.options.body.no_restock_windows)),[{start:'17:00',end:'20:50',allowed_provider:'',takeover_enabled:false}])
 request.resolve({...config,version:2});await tick()
 f.requests.at(-1).resolve({config:{...config,version:2}});await tick()
 f.requests.at(-1).resolve([]);await saving
 assert.equal(f.form.daily_ejh_merge_time,'17:30');assert.equal(f.dirty.value,false)
 f.unmounted()
}
console.log('Passed: 17:00–20:50 no-restock schedule and 17:30 daily merge settings validation/save')

// Reject conflicting allowed issuers even across midnight; preserve full-block rows.
for(const windows of [
 [{start:'23:00',end:'02:00',allowed_provider:'nexovcc'},{start:'01:00',end:'03:00',allowed_provider:'ejh'}],
 [{start:'17:00',end:'20:50',allowed_provider:'unknown'}]
]){
 const f=fixture();f.applyConfig({no_restock_enabled:true,no_restock_windows:windows})
 await f.save();assert.equal(f.requests.length,0);assert.match(f.error.value,/卡台/);f.unmounted()
}
{
 const f=fixture();f.applyConfig({no_restock_enabled:true,no_restock_windows:[{start:'17:00',end:'20:50',allowed_provider:'nexovcc'},{start:'18:00',end:'19:00',allowed_provider:''}],daily_ejh_merge_enabled:true,daily_ejh_merge_time:'17:30'})
 const saving=f.save();await tick();assert.equal(f.requests.length,1,'full prohibition overlap should be accepted')
 assert.equal(f.requests[0].options.body.no_restock_windows[0].allowed_provider,'nexovcc')
 assert.equal(f.requests[0].options.body.daily_ejh_merge_time,'17:30')
 f.requests[0].reject(new Error('fixture stop'));await saving;f.unmounted()
}
console.log('Passed: issuer exceptions round-trip, overlap validation, and independent daily merge')

// Per-window takeover selection survives edits and only applies to full prohibitions.
{
 const f=fixture()
 f.applyConfig({no_restock_enabled:true,no_restock_windows:[{start:'02:00',end:'08:00'},{start:'17:00',end:'20:00',takeover_enabled:true},{start:'23:00',end:'01:00',allowed_provider:'nexovcc',takeover_enabled:true}]})
 assert.equal(f.form.no_restock_windows[0].takeover_enabled,false)
 assert.equal(f.form.no_restock_windows[1].takeover_enabled,true)
 assert.equal(f.form.no_restock_windows[2].takeover_enabled,false)
 assert.match(f.scheduleSummary(f.form),/17:00～20:00 完全禁补 · 环球接管/)
 const saving=f.save();await tick()
 const put=f.requests[0]
 assert.deepEqual(JSON.parse(JSON.stringify(put.options.body.no_restock_windows.map(w=>w.takeover_enabled))),[false,true,false])
 put.reject(new Error('fixture finished'));await saving;f.unmounted()
}
console.log('Passed: per-window takeover flags, default opt-out, multi-window payload and issuer exclusion')

{
 const f=fixture()
 const config={version:1,enabled:false,mode:'observe',capacity_percent:60,money_window_minutes:3,no_restock_enabled:true,no_restock_windows:[{start:'23:00',end:'09:00',high_demand_enabled:true,high_demand_threshold:20,high_demand_confirmations:3}]}
 f.applyConfig(config);f.state.value={config};f.dirty.value=true
 f.form.no_restock_windows[0].high_demand_threshold=0;await f.save()
 assert.equal(f.requests.length,0,'invalid high demand threshold was saved')
 f.form.no_restock_windows[0].high_demand_threshold=20
 const saving=f.save();await tick();const req=f.requests[0]
 assert.equal(req.options.body.no_restock_windows[0].high_demand_threshold,20)
 assert.equal(req.options.body.no_restock_windows[0].high_demand_confirmations,3)
 assert.equal(req.options.body.no_restock_windows[0].high_demand_enabled,true)
 req.resolve({...config,version:2});await tick()
 f.requests.at(-1).resolve({config:{...config,version:2}});await tick()
 f.requests.at(-1).resolve([]);await saving
 assert.equal(f.form.no_restock_windows[0].high_demand_threshold,20)
 f.unmounted()
}
console.log('Passed: high-demand visual settings validation and save/load')

// A local stop removes only one task, never toggles global restock or resets the breaker.
{
 const f=fixture(),task={id:'stop/1',email:'stop@example.test',status:'attention',decision_id:'batch'}
 f.state.value={config:{enabled:true,version:7},tasks:[task,{id:'other',status:'running'}]}
 f.detail.value=task
 const operation=f.stopTask(task);await tick()
 assert.equal(f.requests[0].url,'/api/pro-restock/tasks/stop%2F1/stop')
 assert.equal(f.requests[0].options.body.confirm,true)
 const duplicate=f.stopTask(task);await duplicate
 assert.equal(f.requests.length,1)
 f.requests[0].resolve({...task,status:'cancelled',stopped_at:'2026-10-21T06:00:00Z',message:'已手动停止'})
 await tick()
 assert.equal(f.state.value.tasks[0].status,'cancelled')
 assert.equal(f.state.value.tasks[1].status,'running')
 assert.equal(f.state.value.config.enabled,true)
 f.requests[1].resolve(f.state.value);await tick()
 for(const r of f.requests.slice(2))r.resolve([])
 await operation
 assert.equal(f.stoppingTask.value,'')
 assert.equal(f.taskDetail.value.status,'已手动停止')
 assert.equal(f.canStopTask({status:'running'}),false)
 f.unmounted()
}

// Slow history must never hold the first screen or a completed save hostage.
{
 const f=fixture(),config={enabled:false,version:1,capacity_percent:60,money_window_minutes:3}
 const first=f.refresh(true);await tick()
 assert.equal(f.requests[0].options.timeoutMs,15000)
 f.requests[0].resolve({config});await first;await tick()
 const history=f.requests.find(r=>r.url.includes('/records?'));assert.ok(history)
 assert.equal(f.state.value.config.version,1)
 Object.assign(f.form,config)
 const saving=f.save();await tick()
 f.requests.find(r=>r.options.method==='PUT').resolve({...config,version:2})
 await saving;assert.equal(f.busy.value,false,'save remains busy behind refresh/history')
 assert.equal(f.state.value.config.version,2)
 history.reject(new Error('slow records'));await tick()
 assert.match(f.recordsError.value,/slow records/);assert.equal(f.error.value,'')
 f.unmounted()
}
// A successful poll does not refetch unchanged history every five seconds.
{
 const f=fixture()
 const first=f.refresh(true);await tick();f.requests[0].resolve({config:{version:1}});await first;await tick()
 f.requests[1].resolve([]);await tick()
 const poll=f.refresh(false,true);await tick();f.requests[2].resolve({config:{version:1}});await poll;await tick()
 assert.equal(f.requests.filter(r=>r.url.includes('/records?')).length,1)
 f.unmounted()
}
// First-load failure followed by polling still initializes the settings draft.
{
 const f=fixture(),first=f.refresh(true);await tick();f.requests[0].reject(new Error('temporary'));await first
 const retry=f.refresh(false,true);await tick();f.requests[1].resolve({config:{version:7}});await retry;await tick()
 assert.equal(f.form.version,7);assert.equal(f.error.value,'')
 f.unmounted()
}
console.log('Passed: independent history, nonblocking save, polling throttle and initial-load recovery')

// Cadence settings survive the actual form save and server refresh without
// restoring the former 30-second floor, fixed concurrency, or linked cooldowns.
{
 const f=fixture()
 const config={version:1,enabled:false,check_seconds:1,quota_seconds:10,quota_concurrency:9,cooldown_seconds:0,emergency_cooldown_seconds:120}
 f.applyConfig(config);f.state.value={config}
 const saving=f.save();await tick()
 const put=f.requests.find(r=>r.options.method==='PUT')
 for(const key of ['check_seconds','quota_seconds','quota_concurrency','cooldown_seconds','emergency_cooldown_seconds'])assert.equal(put.options.body[key],config[key])
 const saved={...put.options.body,version:2};put.resolve(saved);await saving
 const refresh=f.requests.find(r=>r.url==='/api/pro-restock');if(refresh)refresh.resolve({config:saved})
 await tick()
 assert.equal(f.form.quota_concurrency,9);assert.equal(f.form.quota_seconds,10);assert.equal(f.form.cooldown_seconds,0)
 f.unmounted()
}
console.log('Passed: configurable restock cadence save and reload')
