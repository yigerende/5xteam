import assert from 'node:assert/strict'
import {financeProfit} from '../src/ejh-profit.js'
import {financeOverview,financeReport,dailyFinanceCost} from '../src/ejh-finance.js'
import {createEJHFinanceWorker} from '../src/ejh-finance-client.js'

// The reported screenshot: 14 card openings, 73.38 USD reversals and a
// 53.63 USD recharge fee. Only the first two belong to operating cost.
const screenshot=financeProfit({openingUSD:'7.00',reversalUSD:'73.38',rechargeUSD:'53.63',consumption:1008.0335})
assert.equal(screenshot.costUSD,'80.38')
assert.equal(screenshot.costR,'538.5460')
assert.equal(screenshot.rechargeR,'359.3210')
assert.equal(screenshot.profitR,'469.4875')

const at='2026-11-19T08:00:00+08:00'
const data={recharge_ledger:{amount:'1000',history_month:'2026-11'},balance:{data:{abroadList:[{currency:'USD',balance:'989.50'}]}},cards:[],cases:[],trades:[],operations:[{id:'open',method:'open',status:'completed',order_no:'order',created_at:at}]}
const history={recharges:[{id:'recharge',at,before:'0',after:'1000',delta:'1000',baseline:false}]}
const overview=financeOverview(data)
assert.equal(overview.earnings.costUSD,'0.50')
assert.equal(overview.depositFee,'10.00')
assert.equal(overview.net,'990.00')
assert.equal(overview.loss,'0.50')
assert.equal(overview.gap,'0.00','net receipts already include the fee deduction; no artificial cash gap')
const worker=createEJHFinanceWorker()
for(const mode of ['day','week','month','all']){
 const report=financeReport(data,history,mode,'2026-11-19')
 assert.equal(report.summary.rechargeUSD,'10.00')
 assert.equal(report.summary.earnings.costUSD,'0.50')
 assert.equal(report.summary.earnings.profitR,'-3.3500')
 assert.equal(report.summary.usd,'0.00','non-opening cost subtotal excludes recharge')
 assert.equal(report.events.filter(row=>row.kind==='recharge').length,1)
 assert.equal(report.rows.reduce((sum,row)=>sum+Number(row.earnings.costUSD),0),0.5)
 assert.deepEqual((await worker.calculate(data,history,mode,'2026-11-19')).summary,report.summary)
 const unknown=financeReport({...data,recharge_ledger:{amount:''}},history,mode,'2026-11-19')
 assert.equal(unknown.summary.rechargeUSD,null)
 assert.equal(unknown.summary.earnings.rechargeR,null)
 assert.equal(unknown.summary.earnings.costUSD,'0.50')
 assert.equal(unknown.summary.earnings.profitR,'-3.3500')
}
worker.dispose()
assert.equal(dailyFinanceCost(data,'2026-11-19').earnings.costUSD,'0.50')
const correction={...history,recharges:[...history.recharges,{id:'correction',at:'2026-11-20T08:00:00+08:00',before:'1000',after:'900',delta:'-100',baseline:false}]}
const negative=financeReport({...data,recharge_ledger:{amount:'900'}},correction,'day','2026-11-20')
assert.equal(negative.summary.rechargeUSD,'-1.00')
assert.equal(negative.summary.earnings.costUSD,'0.00')
assert.equal(negative.summary.earnings.profitR,'0.0000','negative recharge correction is not operating revenue')
for(const mode of ['day','week']){
 const monthly=financeReport(data,{},mode,'2026-11-19')
 assert.equal(monthly.monthlyUndated,1)
 assert.equal(monthly.periodIncomplete,false)
 assert.equal(monthly.summary.earnings.profitR,'-3.3500')
}
assert.equal(financeOverview({...data,recharge_ledger:{amount:''}}).gap,null,'unknown cash inputs still block reconciliation')
console.log('Passed: recharge disclosure only, screenshot arithmetic, all periods, unknown/negative/undated fees, Worker consistency and unchanged net cash reconciliation')
