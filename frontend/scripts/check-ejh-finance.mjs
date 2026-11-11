import assert from 'node:assert/strict'
import { financeOverview, financeReport, periodRange, financeTime, beijingDay, cents, money, consumptionSummary, eventAmountR } from '../src/ejh-finance.js'

const card={id:'card',issuer_linked:true,balance:'22595.26',balance_at:'2026-10-16T17:17:20+08:00',remote:{orderNo:'opened',status:'3',cardCurrency:'PHP'}}
const closed={id:'closed',issuer_linked:true,balance:'999999',balance_at:card.balance_at,remote:{orderNo:'closed-order',status:'2',cardCurrency:'PHP'}}
const operation={id:'open',method:'open',status:'completed',card_id:'card',order_no:'opened',created_at:'2026-10-15T12:00:00+08:00',request:{cardAmount:'10000',cardCurrency:'PHP'},result:{cardAmount:'10000',cardCurrency:'PHP',billAmount:'160.27',billCurrency:'USD'}}
const trade={id:'reversal',card_id:'card',trade:{tradeNo:'trade',tradeType:'2',tradeStatus:'1',cardAmount:'5780.48',cardCurrency:'PHP',orderNo:'opened',tradeTime:'2026-10-16 17:17:20'}}
const data={recharge_ledger:{amount:'3000.00'},balance:{data:{abroadList:[{currency:'USD',balance:'2273.15'}]}},cards:[card,closed],operations:[operation,{...operation,id:'duplicate-open'}],trades:[trade,{...trade,id:'duplicate-trade'}],cases:[{id:'pay',email:'user@example.com',ejh_card_id:'card',state:'waiting',consumption_total:120,charge:{type:'1',status:'1',trade_no:'hold',card_amount:'17383.92',card_currency:'PHP'}},{id:'returned',email:'user@example.com',state:'returned',consumption_total:120}],reversal_fee_percent:6}
let total=financeOverview(data)
assert.equal(total.net,'2970.00');assert.equal(total.depositFee,'30.00')
assert.equal(total.openingFee,'1.00','closed card fees must remain, duplicate operations must not add fees')
assert.equal(total.reversal.rows[0].amount,'346.83','fee rounds each distinct reversal once')
assert.equal(total.reversal.count,1)
assert.equal(total.consumption.total,120)
assert.equal(total.earnings.reversalR,'37.2520')
assert.equal(total.earnings.openingR,'6.7000')
assert.equal(total.earnings.rechargeR,'201.0000')
assert.equal(total.earnings.costR,'244.9520')
assert.equal(total.earnings.profitR,'-124.9520','profit deducts all three costs, including cancelled-card fees')
assert.equal(financeOverview({...data,operations:[]}).earnings.profitR,null,'profit requires full fee conversion')
assert.equal(financeOverview({...data,recharge_ledger:{amount:''}}).earnings.profitR,null,'unknown recharge ledger cannot count as zero cost')
assert.equal(financeOverview({...data,balance:null}).earnings.profitR,total.earnings.profitR,'cash reconciliation must not be deducted again or block profit')
assert.equal(financeOverview({...data,cases:[{id:'unknown',consumption_total:null}]}).earnings.profitR,null)
assert.equal(financeOverview({...data,trades:[{...trade,trade:{...trade.trade,tradeStatus:'0'}}]}).earnings.profitR,null,'pending reversal must not create overstated profit')
assert.equal(total.loss,money(297000n-227315n-cents(total.cardUSD.amount)-cents(total.inFlight.paymentUSD.amount)))
assert.equal(total.cardUSD.total,1,'closed card balance excluded')
assert.equal(financeOverview({...data,cards:[{...card,balance_at:null},closed]}).loss,null,'unknown balances cannot turn into zero')
assert.equal(financeOverview({...data,operations:[]}).loss,null,'unknown FX prevents loss calculation')
assert.equal(financeOverview({...data,cases:[{id:'unmatched',state:'waiting'}]}).loss,null)
assert.equal(financeOverview({...data,balance:{data:{abroadList:[{currency:'EUR',balance:'12'}]}}}).loss,null)
assert.equal(financeOverview({...data,cards:[{...card,remote:{...card.remote,status:'8'}}]}).cardUSD.total,1)
assert.equal(consumptionSummary([{email:'deleted@example.com',consumption_total:null}],[{id:'old',email:'deleted@example.com',total:40}]).total,40)

const history={consumption:[{id:'recorded',email:'user@example.com',total:120,baseline:100}],usage_days:[{id:'day1',email:'user@example.com',day:'2026-10-15',amount:8},{id:'day2',email:'user@example.com',day:'2026-10-16',amount:12}],recharges:[{id:'deposit',at:'2026-10-16T02:00:00+08:00',before:'2000.00',after:'3000.00',delta:'1000.00',baseline:false}]}
let report=financeReport(data,history,'day','2026-10-16')
assert.equal(report.summary.r,12);assert.equal(report.summary.rechargeUSD,'10.00');assert.equal(report.summary.usdt,'0.00');assert.equal(report.baseline,100)
assert.equal(report.events.filter(e=>e.kind==='reversal').length,1)
assert.equal(report.summary.earnings.costR,'104.2520')
assert.equal(report.summary.earnings.profitR,null,'legacy increments with unknown dates must not imply complete period profit')
const openDay=financeReport(data,history,'day','2026-10-15')
assert.equal(openDay.summary.earnings.openingR,'3.3500')
assert.equal(openDay.summary.earnings.profitR,null)
for(const period of ['week','month']){
 const scoped=financeReport(data,history,period,'2026-10-16')
 assert.equal(scoped.summary.earnings.costR,'107.6020')
 assert.equal(scoped.summary.earnings.profitR,null,'unknown historical usage prevents exact period profit')
}
assert.equal(eventAmountR({kind:'opening',amount:'0.50',currency:'USDT'}),'3.3500')
assert.equal(eventAmountR({kind:'reversal',usd:'5.56'}),'37.2520')
assert.equal(eventAmountR({kind:'reversal',usd:null,incomplete:true}),null)
assert.equal(eventAmountR({kind:'consumption',amount:12}),'12.0000')
assert.equal(eventAmountR({kind:'recharge',usd:'-1.00'}),'-6.7000')
report=financeReport(data,history,'all')
assert.equal(report.summary.r,120);assert.equal(report.summary.rechargeUSD,'30.00');assert.equal(report.summary.usdt,'1.00')
assert.deepEqual(report.summary.earnings,report.overview.earnings,'all-time finance and dashboard must agree')
assert.equal(report.summary.earnings.costR,total.earnings.costR)
assert.equal(report.rows.find(r=>r.day==='未分期历史').earnings.costR,'137.3500')
assert.equal(report.rows.find(r=>r.day==='未分期历史').earnings.profitR,'-37.3500')
const multiple={...data,trades:Array.from({length:12},(_,i)=>({...trade,id:'many-'+i,trade:{...trade.trade,tradeNo:'many-'+i}}))}
assert.equal(financeReport(multiple,history,'all').summary.reversalUSD,financeOverview(multiple).reversal.usd.amount,'USD must round after summing, matching the card dashboard')
assert.equal(report.rows.reduce((n,r)=>n+r.r,0),120,'period rows plus undated baseline reconcile to all-time usage')
assert.equal(report.rows.reduce((n,r)=>n+(cents(r.rechargeUSD)||0n),0n),3000n)
assert.equal(financeReport(data,history,'day','2026-10-14').events.length,0)
const unknownLedger=financeReport({...data,recharge_ledger:{amount:''}},history,'month','2026-10-16')
assert.equal(unknownLedger.summary.earnings.profitR,null)
assert.ok(unknownLedger.rows.every(row=>row.earnings.profitR===null),'period rows must not imply a complete profit when the ledger is unknown')
const invalidUsage=financeReport(data,{...history,usage_days:[{id:'unknown',day:'2026-10-16',amount:null}]},'day','2026-10-16')
assert.equal(invalidUsage.summary.earnings.profitR,null,'unknown period usage is not zero revenue')
assert.equal(invalidUsage.summary.earnings.costR,'104.2520','unknown revenue should not hide known costs')
assert.equal(beijingDay(financeTime('2026-10-15T16:00:00Z')),'2026-10-16')
const week=periodRange('week','2026-10-18');assert.equal(beijingDay(week.start),'2026-10-12');assert.equal(beijingDay(week.end-1),'2026-10-18')
const month=periodRange('month','2026-12-31');assert.equal(beijingDay(month.end),'2027-01-01')
assert.equal(financeTime('2026-10-16 00:00:00'),Date.parse('2026-10-15T16:00:00Z'))
const snapshot={...data,at:'2026-10-16T00:05:00+08:00'}
assert.equal(financeReport(data,{...history,snapshots:[snapshot]},'day','2026-10-16').lossChange,null,'one observation is not a period loss')
assert.equal(financeReport(data,{...history,snapshots:[snapshot,{...snapshot,at:'2026-10-16T23:59:00+08:00',balance:{data:{abroadList:[{currency:'USD',balance:'2270.15'}]}}}]},'day','2026-10-16').lossChange,'3.00')
console.log('Passed: all three costs and net profit in R, day/week/month/all-time totals, missing ledger and consumption, signed adjustments, no cash-loss double deduction, exact fee arithmetic, duplicate transactions/cards/accounts, closed and pending cards, missing FX, Beijing day/week/month, undated baselines, deleted accounts and snapshot loss changes')

const daily={...history,consumption:[{id:'recorded',email:'user@example.com',total:120,daily_checked_at:'2026-10-17T01:00:00+08:00'}],usage_days:[{id:'day1',email:'user@example.com',day:'2026-10-15',amount:90,source:'sub2_daily'},{id:'day2',email:'user@example.com',day:'2026-10-16',amount:30,source:'sub2_daily'}]}
for(const mode of ['day','week','month','all']){
 const corrected=financeReport(data,daily,mode,'2026-10-16')
 assert.equal(corrected.summary.r,mode==='day'?30:120)
 assert.equal(corrected.baseline,0)
 assert.equal(corrected.periodIncomplete,false)
 assert.notEqual(corrected.summary.earnings.profitR,null)
}
assert.equal(financeReport(data,{...daily,usage_days:[...daily.usage_days,daily.usage_days[0]]},'week','2026-10-16').summary.r,120,'duplicate daily snapshots must not inflate revenue')
assert.equal(total.earnings.costUSD,'36.56')

// User-confirmed historical month changes attribution only, never total costs.
const monthData={...data,recharge_ledger:{amount:'3000.00',history_month:'2026-10'}}
const baselineRecharge={recharges:[{id:'baseline-recharge',at:'2026-11-01T08:00:00+08:00',before:'',after:'3000.00',delta:'3000.00',baseline:true}]}
const monthAll=financeReport(monthData,baselineRecharge,'all','2026-10-17')
assert.equal(monthAll.rows.find(r=>r.day==='2026-10').rechargeUSD,'30.00')
assert.ok(!monthAll.rows.find(r=>r.day==='未分期历史')||monthAll.rows.find(r=>r.day==='未分期历史').rechargeUSD==='0.00')
assert.equal(financeReport(monthData,baselineRecharge,'month','2026-10-17').summary.rechargeUSD,'30.00')
assert.equal(financeReport(monthData,baselineRecharge,'month','2026-11-17').summary.rechargeUSD,'0.00')
for(const mode of ['day','week']){
 const report=financeReport(monthData,baselineRecharge,mode,'2026-10-17')
 assert.equal(report.summary.rechargeUSD,'0.00','unknown day must not be made up')
 assert.equal(report.monthlyUndated,1)
 assert.equal(report.summary.earnings.profitR,null,'unallocated monthly cost cannot imply complete daily profit')
}
assert.equal(financeReport(monthData,{},'month','2026-10-17').summary.rechargeUSD,'30.00','legacy ledger without recharge events must use confirmed month too')
assert.equal(monthAll.summary.earnings.costR,financeReport(data,baselineRecharge,'all').summary.earnings.costR)
console.log('Passed: historical recharge month, no invented dates, unchanged total cost')

const noPush={email:'never@example.com',id:'never',consumption_state:'not_pushed'}
const withoutSource=financeOverview({...data,cases:[...data.cases,noPush]})
assert.equal(withoutSource.consumption.missing,0)
assert.equal(withoutSource.consumption.notPushed,1)
assert.equal(withoutSource.earnings.profitR,total.earnings.profitR)
assert.equal(withoutSource.earnings.costR,total.earnings.costR)
assert.equal(financeReport({...data,cases:[...data.cases,noPush]},history,'all').summary.earnings.profitR,report.summary.earnings.profitR)
assert.equal(financeOverview({...data,cases:[...data.cases,noPush,{email:'unknown@example.com'}]}).earnings.profitR,null)
console.log('Passed: no-push explanation does not change saved revenue/costs or conceal real missing consumption')
