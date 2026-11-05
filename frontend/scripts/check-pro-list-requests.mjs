import assert from 'node:assert/strict'
import vm from 'node:vm'
import {readFileSync} from 'node:fs'
import ts from 'typescript'
import {ref} from 'vue'
import {createLatestRequest} from '../src/latestRequest.js'
const source=readFileSync(new URL('../src/components/ProManagementView.vue',import.meta.url),'utf8')
const parsed=ts.createSourceFile('View.js',source.split('<script setup>')[1].split('</script>')[0],ts.ScriptTarget.Latest,true,ts.ScriptKind.JS)
const names=new Set(['loadPage','loadPageHandled','setSpaceFilter','clearSelection'])
const vars=new Set(['accountsLoading','renderedAccountPage','accountsRequest','crossPageSelection','selectionRevision'])
const code=parsed.statements.filter(s=>ts.isFunctionDeclaration(s)?names.has(s.name?.text):ts.isVariableStatement(s)&&s.declarationList.declarations.some(d=>vars.has(d.name.getText(parsed)))).map(s=>s.getText(parsed)).join('\n')
const requests=[], errors=[], rows=ref([]), page=ref(1), filter=ref('all')
const c=vm.createContext({finalStepControlRevision:0,finalStepControlBusy:ref(false),settings:{},ref,createLatestRequest,URLSearchParams,rows,page,pageSize:ref(10),query:ref(''),spaceFilter:filter,total:ref(0),listSummary:{},selectedEmails:ref(new Set()),searchTimer:0,window:{clearTimeout(){}},setMessage:message=>errors.push(message),api:(url,{signal})=>new Promise((resolve,reject)=>requests.push({url,signal,resolve,reject}))})
vm.runInContext(code+'\nglobalThis.state={accountsLoading,renderedAccountPage,accountsRequest};',c)
const tick=()=>new Promise(resolve=>setImmediate(resolve))
const data=email=>({items:[{email}],total:12,summary:{all:12}})
const old=c.loadPage();await tick()
filter.value='deleted';const current=c.loadPage();await tick()
assert.equal(requests[0].signal.aborted,true)
assert.match(requests[1].url,/merge_state=deleted/)
const polls=Array.from({length:12},()=>c.loadPage({background:true}));await tick()
assert.equal(requests.length,2,'polling must not pile up requests or cancel navigation')
requests[1].resolve(data('current@example.test'));await current
requests[0].resolve(data('stale@example.test'));await old;await Promise.all(polls)
assert.equal(rows.value[0].email,'current@example.test')
assert.equal(c.state.accountsLoading.value,false)
c.setSpaceFilter('deleted');await tick();assert.equal(requests.length,2,'same tab must not reload')
c.setSpaceFilter('merged');await tick()
assert.equal(rows.value.length,0,'old tab must not keep actionable rows')
assert.equal(c.state.accountsLoading.value,true)
requests.at(-1).reject(new Error('fixture failure'));await tick()
assert.equal(errors.at(-1),'fixture failure')
page.value=8
const last=c.loadPage();await tick();requests.at(-1).resolve(data('outside-page'));await tick()
assert.equal(page.value,2);requests.at(-1).resolve(data('last@example.test'));await last
assert.equal(rows.value[0].email,'last@example.test')
const pending=c.loadPage();await tick();c.state.accountsRequest.dispose()
assert.equal(requests.at(-1).signal.aborted,true)
requests.at(-1).resolve(data('unmounted@example.test'));await pending
assert.equal(rows.value[0].email,'last@example.test')
console.log('Passed: Pro tab cancellation, latest response wins, shared polling, loading state, duplicate clicks, pagination and unmount')
