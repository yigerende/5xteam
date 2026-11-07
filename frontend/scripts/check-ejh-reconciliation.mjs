import assert from 'node:assert/strict'
import { reconciliationStatus, reconciliationCards } from '../src/ejh-reconciliation-view.js'

const at='2026-10-17T12:00:00+08:00', now=Date.parse(at)
const base={recharge_ledger:{amount:'100.00'},balance:{at,data:{abroadList:[{currency:'USD',balance:'99.00'}]}},sync:{last_success:at},cards:[],trades:[],cases:[],operations:[]}
const withBalance=n=>({...base,balance:{...base.balance,data:{abroadList:[{currency:'USD',balance:n}]}}})
assert.equal(reconciliationStatus(base,now).state,'balanced')
for (const [balance,state,gap] of [['98.00','small','1.00'],['100.00','small','-1.00'],['97.99','difference','1.01'],['100.01','difference','-1.01']]) {
 const result=reconciliationStatus(withBalance(balance),now)
 assert.equal(result.state,state);assert.equal(result.value.gap,gap)
 assert.equal(result.value.earnings.costUSD,'1.00','difference must not be deducted as cost')
}
assert.equal(reconciliationStatus({...base,balance:null},now).state,'unknown')
assert.equal(reconciliationStatus({...base,recharge_ledger:{}},now).value.gap,null)
assert.equal(reconciliationStatus({...base,sync:{...base.sync,error:'failure'}},now).state,'stale')
assert.equal(reconciliationStatus(base,now+601000).state,'stale')
assert.deepEqual(reconciliationStatus(base,now+601000).freshness.map(r=>r.label),['EJH 账户余额','卡片与交易'])
assert.equal(reconciliationStatus(base,now+601000,1200).state,'balanced','respect longer refresh intervals')
const card={id:'c',issuer_linked:true,remote:{orderNo:'order',cardNo:'**** 1234',status:'8',cardCurrency:'USD'},balance:'40.00',balance_at:at}
const data={...withBalance('48.50'),cards:[card],cases:[{id:'case',ejh_card_id:'c',state:'waiting',charge:{type:'1',status:'1',trade_no:'held',card_amount:'10.00',card_currency:'USD'}}]}
let r=reconciliationStatus(data,now)
assert.equal(r.state,'balanced');assert.equal(r.value.assets,'98.50');assert.equal(r.value.earnings.costUSD,'1.50')
let rows=reconciliationCards(data)
assert.equal(rows[0].processing,true);assert.equal(rows[0].pending,'10.00');assert.equal(rows[0].total,'50.00')
assert.equal(reconciliationCards({...data,cards:[{...card,remote:{...card.remote,status:'2'}}]}).length,0)
assert.equal(reconciliationStatus({...data,cards:[{...card,balance_error:'failed'}]},now).state,'stale')
const pending={...card,normal_pending:true,balance_at:'2026-10-17T10:00:00+08:00'}
assert.equal(reconciliationStatus({...data,cards:[pending]},now).freshness[0].label,'卡片 1234（正常待定）')
assert.equal(reconciliationStatus({...data,cards:[{...pending,balance_at:at}]},now).state,'balanced','refreshed pending-card balance must clear staleness')
assert.equal(reconciliationStatus({...data,cards:[{...card,balance_at:null}]},now).state,'unknown')
assert.equal(reconciliationCards({...data,cards:[{...card,balance_at:null}]} )[0].total,null)
console.log('Passed: reconciliation balance, signed tolerance, no cost double-counting, stale/missing data, pending closure and per-card assets')
