import assert from 'node:assert/strict'
import {financeReport, openedAccountCount, openedAccountRows, periodRange} from '../src/ejh-finance.js'
import {createEJHFinanceWorker} from '../src/ejh-finance-client.js'

const opened=(email,opened_at,extra={})=>({email,opened_at,payment_status:'success',state:'opened',...extra})
const cases=[
 opened('a@example.test','2026-11-17T16:00:00Z'),
 opened(' A@example.test ','2026-11-18T18:00:00+08:00',{state:'returned',deleted:true,plan:'pro5',last4:'1234',access_token:'must-not-be-copied'}),
 opened('b@example.test','2026-11-18T23:59:59+08:00',{state:'waiting'}),
 opened('c@example.test','2026-11-18T16:00:00Z'),
 opened('yesterday@example.test','2026-11-17T15:59:59Z',{requested_at:'2026-11-18T12:00:00+08:00',state:'returned'}),
 opened('unknown@example.test',null,{created_at:'2026-11-18T12:00:00+08:00',requested_at:'2026-11-18T13:00:00+08:00'}),
 opened('invalid@example.test','invalid'),
 opened('failed@example.test','2026-11-18T12:00:00+08:00',{payment_status:'failed'}),
 opened('points@example.test','2026-11-18T12:00:00+08:00',{plan:'codex_point'}),
 opened('','2026-11-18T12:00:00+08:00'),
 opened('last-month@example.test','2026-10-31T12:00:00+08:00'),
]
assert.equal(openedAccountCount(cases,periodRange('day','2026-11-18')),2)
assert.equal(openedAccountCount(cases,periodRange('day','2026-11-19')),1)
const details=openedAccountRows(cases,periodRange('day','2026-11-18'))
assert.deepEqual(details.map(r=>r.email),['b@example.test','a@example.test'])
assert.deepEqual(details[1],{email:'a@example.test',opened_at:'2026-11-18T18:00:00+08:00',state:'returned',deleted:true,plan:'pro5',last4:'1234'})
assert.equal(openedAccountRows(cases,periodRange('day','2026-11-20')).length,0)
const data={cases,recharge_ledger:{amount:'0'}}
const original=JSON.stringify(data)
const worker=createEJHFinanceWorker()
for(const [mode,count] of [['day',2],['week',4],['month',4],['all',5]]){
 const report=financeReport(data,{},mode,'2026-11-18')
 assert.equal(report.openedAccounts,count,mode)
 assert.equal(report.openedAccountRows.length,count,mode)
 const computed=await worker.calculate(data,{},mode,'2026-11-18')
 assert.equal(computed.openedAccounts,count)
 assert.deepEqual(computed.openedAccountRows,report.openedAccountRows)
}
worker.dispose()
assert.equal(JSON.stringify(data),original)
console.log('Passed: opened-time account count, Beijing boundaries, deduplication, returned/deleted accounts, payment failures, unknown dates, points exclusion and all periods')
