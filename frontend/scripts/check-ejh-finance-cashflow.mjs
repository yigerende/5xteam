import assert from 'node:assert/strict'
import { financeReport, financeOverview, prepareFinanceReport } from '../src/ejh-finance.js'
import { pendingRefundSettlement } from '../src/ejh-refund-settlement.js'
import { reconciliationCards } from '../src/ejh-reconciliation-view.js'
import { createEJHFinanceWorker } from '../src/ejh-finance-client.js'

const card={id:'card',balance:'5',balance_at:'2026-11-16T11:00:00+08:00',remote:{orderNo:'card',cardNo:'****1234',cardType:'MASTER_B1_5',cardCurrency:'USD',status:'3',startTime:'2026-11-15 10:00:00'}}
const trade=(id,type,amount,day)=>({id,card_id:'card',trade:{tradeNo:id,authCode:'same',cardType:'MASTER_B1_5',tradeType:type,tradeStatus:'2',cardAmount:amount,cardCurrency:'USD',billAmount:amount,billCurrency:'USD',createdAt:day+' 10:00:00',refundBuyPrice:['5','16'].includes(type)?amount:''}})
const clearing=trade('clear','4','100','2026-11-15'),refund=trade('refund','5','95','2026-11-16')
const data={cards:[card],operations:[],trades:[clearing,refund,refund],cases:[{email:'a@example.com',state:'returned',consumption_total:250}],recharge_ledger:{amount:'200'},reversal_fee_percent:0,balance:{data:{abroadList:[{currency:'USD',balance:'100'}]}}}
const history={consumption:[{id:'usage',email:'a@example.com',total:250,start_day:'2026-11-15',end_day:'2026-11-16',daily_checked_at:'2026-11-16T12:00:00+08:00'}],usage_days:[{id:'usage:2026-11-15',day:'2026-11-15',email:'a@example.com',amount:100,source:'sub2_daily'},{id:'usage:2026-11-16',day:'2026-11-16',email:'a@example.com',amount:150,source:'sub2_daily'}]}
let report=financeReport(data,history,'day','2026-11-16')
assert.equal(report.summary.clearingUSD,'0.00','refund cash never creates negative cost today')
assert.equal(report.summary.earnings.costR,'0.0000')
assert.equal(report.summary.refundPrincipalUSD,'95.00','duplicate refund counted once')
assert.equal(report.summary.earnings.costUSD,'0.00')
assert.equal(report.summary.earnings.refundPrincipalR,'636.5000')
assert.equal(report.summary.earnings.profitR,'150.0000')
assert.equal(financeReport(data,history,'day','2026-11-15').summary.refundPrincipalUSD,'0.00','cash receipt stays on the actual refund day')
assert.equal(financeReport(data,history,'day','2026-11-15').summary.clearingUSD,'5.00')
assert.equal(pendingRefundSettlement(data.cards,data.trades).amount,'95.00')
assert.equal(financeReport(data,history,'day','2026-11-15').summary.earnings.profitR,'63.1500','original cost is only the unreturned 5 USD plus opening fee')
const cashOnly={...data,cases:[]},noRevenue={consumption:[],usage_days:[]}
assert.equal(financeReport(cashOnly,noRevenue,'day','2026-11-16').summary.earnings.profitR,'0.0000','principal alone cannot be profit')
const partial={...data,trades:[clearing,trade('refund-a','5','40','2026-11-16'),trade('refund-b','5','55','2026-11-17')]}
assert.equal(financeReport(partial,history,'day','2026-11-15').summary.clearingUSD,'5.00')
assert.equal(financeReport(partial,history,'day','2026-11-16').summary.refundPrincipalUSD,'40.00')
assert.equal(financeReport(partial,history,'day','2026-11-17').summary.earnings.profitR,'0.0000')
const undated={...data,trades:[{...clearing,trade:{...clearing.trade,createdAt:''}},refund]}
assert.equal(financeReport(undated,history,'day','2026-11-16').refundUnallocated,1)
assert.equal(financeReport(undated,history,'day','2026-11-16').summary.earnings.profitR,null)
const excess={...data,trades:[clearing,trade('excess','5','110','2026-11-16')]}
assert.equal(financeReport(excess,history,'day','2026-11-16').summary.earnings.profitR,null,'excess refund must remain unallocated, never fabricated profit')
const paymentOnly={...data,payment_policies:[{card_id:'card',mode:'payment_only'}]}
for(const date of ['2026-11-15','2026-11-16'])assert.equal(financeReport(paymentOnly,history,'day',date).summary.clearingUSD,'0.00','payment-only refund exclusions stay with original cost')
const otherClear={...clearing,id:'other-clear',trade:{...clearing.trade,tradeNo:'other-clear',authCode:'other',billAmount:'30',cardAmount:'30'}}
assert.equal(financeReport({...data,trades:[...data.trades,otherClear]},history,'day','2026-11-15').summary.clearingUSD,'35.00','refund does not consume another authorization cost')

assert.equal(reconciliationCards(data)[0].refund,'95.00')
assert.equal(reconciliationCards(data)[0].total,'100.00')
const before=financeOverview(data)
const closed={...data,cards:[{...card,remote:{...card.remote,status:'2'}}],trades:[...data.trades,trade('closure','17','100','2026-11-16')],balance:{data:{abroadList:[{currency:'USD',balance:'200'}]}}}
assert.equal(pendingRefundSettlement(closed.cards,closed.trades).amount,'0.00')
assert.equal(financeOverview(closed).assets,before.assets,'card balance and held refund move to main balance once')
assert.equal(financeOverview(closed).loss,before.loss)
assert.equal(financeReport(closed,history,'day','2026-11-16').summary.earnings.profitR,report.summary.earnings.profitR,'closure is not another profit')
assert.equal(pendingRefundSettlement(data.cards,[{...refund,trade:{...refund.trade,tradeStatus:'1'}}]).amount,'0.00','pending refund is not received money')
assert.equal(pendingRefundSettlement(data.cards,[{...refund,trade:{...refund.trade,refundBuyPrice:''}}]).missing,1,'unknown settlement amount is not zero')
assert.equal(pendingRefundSettlement(data.cards,[{...refund,trade:{...refund.trade,cardType:'OTHER'}}]).amount,'0.00','do not infer other issuer product accounting')
const old={id:'old',email:'old@example.com',total:12,start_day:'2026-11-12',end_day:'2026-11-12',daily_checked_at:'2026-11-12T23:00:00+08:00',daily_error:'old connection',baseline:2}
const oldHistory={...history,consumption:[...history.consumption,old],usage_days:[...history.usage_days,{id:'old:2026-11-12',email:old.email,day:'2026-11-12',amount:10,source:'sub2_daily'}]}
report=financeReport(data,oldHistory,'day','2026-11-16')
assert.equal(report.dailyPending,0);assert.equal(report.historicalPending,1)
assert.equal(report.periodIncomplete,false);assert.notEqual(report.summary.earnings.profitR,null)
assert.equal(financeReport(data,oldHistory,'day','2026-11-12').summary.earnings.profitR,null,'keep genuinely affected dates incomplete')
assert.equal(financeReport(data,oldHistory,'month','2026-11-16').summary.earnings.profitR,null)
assert.equal(financeReport(data,oldHistory,'all','2026-11-16').rows.find(row=>row.day==='2026-11').earnings.profitR,null,'all-time month rows still disclose incomplete dates')
const currentError={...history,consumption:[{...history.consumption[0],daily_error:'timeout'}]}
assert.equal(financeReport(data,currentError,'day','2026-11-16').summary.earnings.profitR,null)
const prepared=prepareFinanceReport(data,oldHistory)
for(const mode of ['day','week','month','all'])assert.deepEqual(financeReport(data,oldHistory,mode,'2026-11-16',prepared),financeReport(data,oldHistory,mode,'2026-11-16'))
const worker=createEJHFinanceWorker()
for(const date of ['2026-11-16','2026-11-15'])assert.deepEqual(await worker.calculate(data,oldHistory,'day',date),financeReport(data,oldHistory,'day',date))
worker.dispose();await assert.rejects(()=>worker.calculate(data,history,'all','2026-11-16'))
console.log('Passed: original-period net cost, principal is never revenue, cross-day/partial/multiple/unknown refunds, no closure double profit/assets, unknown settlement, date-scoped revenue gaps, cached period calculation')
