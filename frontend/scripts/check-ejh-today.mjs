import assert from 'node:assert/strict'
import { dailyFinanceCost, financeReport, financeOverview } from '../src/ejh-finance.js'
import { calculateEJHOverview } from '../src/ejh-overview.js'
import { summarizeInFlightPayments } from '../src/ejh-account-amounts.js'

const date='2026-11-07', at='2026-11-06T16:00:00Z'
const data={cards:[],cases:[],operations:[
 {id:'today',method:'open',status:'completed',order_no:'today',created_at:at},
 {id:'yesterday',method:'open',status:'completed',order_no:'yesterday',created_at:'2026-11-06T15:59:59Z'},
 {id:'tomorrow',method:'open',status:'completed',order_no:'tomorrow',created_at:'2026-11-07T16:00:00Z'},
],trades:[],recharge_ledger:{amount:'1200'},recharge_history:[
 {id:'baseline',at,before:'0',after:'1000',delta:'1000',baseline:true},
 {id:'recharge',at,before:'1000',after:'1200',delta:'200',baseline:false},
],external_transfer_history:[],merchant_fees:[{request_id:'api',at,amount:'1.00'}]}
const unchanged=JSON.stringify(data), allCost=financeOverview(data).earnings.costUSD
const today=dailyFinanceCost(data,date)
assert.equal(today.earnings.costUSD,'3.50')
assert.equal(today.earnings.costR,'23.4500')
assert.equal(today.undated,1,'baseline must not be charged on its recording date')
assert.equal(dailyFinanceCost(data,'2026-11-08').earnings.costUSD,'0.50')
assert.equal(dailyFinanceCost(data,'2026-11-09').earnings.costUSD,'0.00')
assert.equal(calculateEJHOverview(data,Date.parse(at)-1).today.date,'2026-11-06')
assert.equal(calculateEJHOverview(data,Date.parse(at)).today.date,date)
const history={recharges:data.recharge_history,external_transfers:data.external_transfer_history}
assert.equal(today.earnings.costUSD,financeReport(data,history,'day',date).summary.earnings.costUSD)
assert.equal(dailyFinanceCost({...data,consumption_history:[{id:'unknown',email:'unknown@example.com',total:null}]},date).earnings.costUSD,'3.50','unknown revenue cannot hide dated costs')
const missing=dailyFinanceCost({...data,recharge_ledger:{amount:''}},date)
assert.equal(missing.earnings.costUSD,null)
assert.equal(missing.earnings.costR,null)
assert.equal(missing.earnings.knownCostUSD,'3.50')
const datedFee={id:'fee',at,amount:'2',revision:1}
assert.equal(dailyFinanceCost({...data,refund_fees:[datedFee]},date).earnings.costUSD,'5.50')
const transfer={id:'external',at,delta:'1',baseline:false}
const adjusted={...data,recharge_ledger:{amount:'1200',external_transfer_amount:'1'},external_transfer_history:[transfer]}
assert.equal(dailyFinanceCost(adjusted,date).earnings.costUSD,'2.50','dated external reclassification must not inflate costs')
assert.equal(JSON.stringify(data),unchanged)
assert.equal(financeOverview(data).earnings.costUSD,allCost)

const card={id:'card',remote:{status:'1',cardCurrency:'PHP'}}
const waiting={id:'wait',email:'missing@example.com',ejh_card_id:card.id,state:'waiting'}
let inflight=summarizeInFlightPayments([waiting],[card])
assert.equal(inflight.paymentUSD.missing,1)
assert.deepEqual(inflight.missingRows,[{id:waiting.id,email:waiting.email,card_id:card.id,reason:'尚未匹配到付款流水'}])
const charged={...waiting,charge:{type:'1',status:'1',trade_no:'charge',card_amount:'100',card_currency:'PHP'}}
inflight=summarizeInFlightPayments([charged],[card])
assert.equal(inflight.missingRows[0].reason,'付款币种或 USD 折算依据缺失')
assert.equal(inflight.paymentUSD.missing,1)
assert.equal(summarizeInFlightPayments([{...charged,charge:{...charged.charge,card_currency:'USD'}}],[card]).missingRows.length,0)
assert.equal(summarizeInFlightPayments([waiting],[{...card,remote:{status:'2'}}]).missingRows.length,0,'closed cards do not create new in-flight obligations')
assert.equal(summarizeInFlightPayments([{...waiting,state:'returned'}],[card]).missingRows.length,0)
console.log('Passed: Beijing daily costs, date boundaries, historical baseline, dated recharges/fees/exclusions, missing data diagnostics and unchanged all-time costs')
