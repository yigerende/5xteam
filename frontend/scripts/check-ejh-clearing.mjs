import assert from 'node:assert/strict'
import { summarizeClearingCosts } from '../src/ejh-clearing.js'
import { summarizeInFlightPayments, cardAmounts, accountAmounts } from '../src/ejh-account-amounts.js'
import { authorizationDiscrepancies } from '../src/ejh-reconciliation.js'
import { financeOverview,financeReport } from '../src/ejh-finance.js'
import { newestReturnTrades } from '../src/ejh-trade-sort.js'

const card={id:'c',balance:'4205.36',balance_at:'2026-10-17T10:00:00+08:00',issuer_linked:true,remote:{orderNo:'order',status:'3',cardCurrency:'PHP'}}
const op={id:'open',method:'open',status:'completed',card_id:'c',order_no:'order',result:{cardAmount:'10000',cardCurrency:'PHP',billAmount:'160.29',billCurrency:'USD'}}
const trade=(id,type,amount,bill='0')=>({id,card_id:'c',trade:{tradeNo:id,authCode:'A',tradeType:type,tradeStatus:type==='1'||type==='2'?'1':'2',cardAmount:amount,cardCurrency:'PHP',billAmount:bill,billCurrency:'USD',tradeTime:'2026-10-16 20:24:40',createdAt:'2026-10-17 08:00:38'}})
const auth=trade('auth','1','5794.64'),clear=trade('clear','4','13.34','0.22')
const row={id:'case',ejh_card_id:'c',state:'waiting',charge:{id:'clear',trade_no:'clear',type:'4',status:'2',card_amount:'13.34',card_currency:'PHP'}}
const evidence=[auth,clear,{...clear}]
let inFlight=summarizeInFlightPayments([row],[card],[op],evidence)
assert.deepEqual(inFlight.payment,[{currency:'PHP',amount:'5781.30'}],'small clearing is a cost; unreleased authorization remains in flight')
assert.equal(inFlight.paymentUSD.amount,'92.67')
const cost=summarizeClearingCosts(evidence,[card],[op])
assert.equal(cost.count,1);assert.equal(cost.usd.amount,'0.22','prefer actual bill settlement over historical FX')
assert.deepEqual(cost.rows,[{currency:'PHP',amount:'13.34'}])
assert.equal(summarizeInFlightPayments([row],[card],[op],[clear]).missingPayment,1)
assert.equal(summarizeInFlightPayments([row],[card],[op],[auth,{...clear,trade:{...clear.trade,cardAmount:'5794.64'}}]).paymentUSD.total,0)
assert.equal(summarizeInFlightPayments([{...row,state:'returned'}],[card],[op],evidence).payment.length,0)
assert.equal(summarizeInFlightPayments([row,{...row,id:'duplicate'}],[card],[op],evidence).paymentUSD.amount,'92.67')
const reversal=trade('reversal','2','5781.30')
assert.equal(summarizeInFlightPayments([row],[card],[op],[auth,clear,reversal]).payment.length,0,'released balance leaves in-flight principal')
assert.equal(authorizationDiscrepancies([], [auth,clear,reversal],[card],[op]).shortfalls.length,0,'true clearing costs are not closure-recoverable principal')
const returned=trade('returned','2','5780.48')
let difference=authorizationDiscrepancies([], [auth,returned,returned],[card],[op])
assert.deepEqual(difference.rows,[{currency:'PHP',amount:'14.16'}])
assert.equal(difference.shortfallUSD.amount,'0.23')
assert.equal(difference.pendingUSD.amount,'0.23')
difference=authorizationDiscrepancies([], [auth,returned],[{...card,remote:{...card.remote,status:'2'}}],[op])
assert.equal(difference.completedUSD.amount,'0.23');assert.equal(difference.pendingUSD.amount??'0.00','0.00')
const data={cards:[card],operations:[op],cases:[row],trades:evidence,recharge_ledger:{amount:'3000'},balance:{data:{abroadList:[{currency:'USD',balance:'2013.95'}]}}}
let overview=financeOverview(data)
assert.equal(overview.earnings.clearingR,'1.4740')
assert.equal(overview.earnings.costUSD,'0.72')
assert.equal(financeReport(data,{},'day','2026-10-17').summary.clearingUSD,'0.22','cost follows issuer posting day')
assert.equal(financeReport(data,{},'day','2026-10-16').summary.clearingUSD,'0.00')
assert.equal(financeOverview({...data,trades:[auth,returned]}).earnings.clearingR,'0.0000','reversal shortfall is not a cost')
const refund=trade('refund','16','13.34','0.22')
assert.equal(summarizeClearingCosts([...evidence,refund],[card],[op]).usd.amount,'0.00','posted clearing refund offsets cost once')
assert.equal(summarizeClearingCosts([{...clear,trade:{...clear.trade,cardAmount:'bad'}}],[card],[op]).missing,1)
const rows=[{id:'old',trade:{tradeTime:'2026-10-15 17:24:02'}},{id:'latest',trade:{tradeTime:'2026-10-17 09:33:59'}},{id:'missing',trade:{}},{id:'middle',trade:{createdAt:'2026-10-16T01:00:00Z'}}]
assert.deepEqual(newestReturnTrades(rows).map(r=>r.id),['latest','middle','old','missing'])
assert.equal(rows[0].id,'old','sorting must not mutate shared issuer records')
const delayedRefund={id:'posted-latest',trade:{tradeType:'5',tradeTime:'2026-10-28 11:51:10',createdAt:'2026-10-29 10:11:29'}}
const earlierRefund={id:'posted-earlier',trade:{tradeType:'2',tradeTime:'2026-10-28 20:00:00',createdAt:'2026-10-29 09:00:00'}}
assert.deepEqual(newestReturnTrades([earlierRefund,delayedRefund]).map(r=>r.id),['posted-latest','posted-earlier'],'return sorting follows posting date')
const paymentRows=[{id:'payment-latest',trade:{tradeType:'1',tradeTime:'2026-10-29 10:00:00',createdAt:'2026-10-29 10:01:00'}},{id:'payment-earlier',trade:{tradeType:'4',tradeTime:'2026-10-28 10:00:00',createdAt:'2026-10-29 11:00:00'}}]
assert.deepEqual(newestReturnTrades(paymentRows).map(r=>r.id),['payment-latest','payment-earlier'],'non-return trade ordering remains unchanged')
console.log('Passed: partial clearing, outstanding principal, refunds, original/USD cost, non-cost closure differences, posting dates and descending return records')

assert.equal(cardAmounts(card,evidence).rows[0].paid,'5794.64','card details retain the original authorized payment')
assert.equal(accountAmounts(row,evidence).paid,'5794.64','account details retain the original authorized payment')
assert.equal(accountAmounts(row,evidence).remaining,'5781.30')
assert.equal(financeOverview(data).closurePending,'0.00')
assert.equal(financeReport({...data,trades:[auth,{...clear,trade:{...clear.trade,tradeStatus:'1'}}]}, {}, 'day','2026-10-17').summary.earnings.costR,null,'pending clearing does not imply a complete daily cost')

const reversedCase={...row,charge:{...row.charge,id:'auth',trade_no:'auth',type:'1',status:'1',card_amount:'5794.64'}}
const holdingDifference=financeOverview({...data,cases:[reversedCase],trades:[auth,returned]})
assert.equal(holdingDifference.reconciliation.pendingUSD.amount,'0.23')
assert.equal(holdingDifference.closurePending,'0.00','principal already in flight must not be deducted from the unexplained gap twice')
const closedCard={...card,remote:{...card.remote,status:'2'}}
assert.equal(summarizeInFlightPayments([reversedCase],[closedCard],[op],[auth,returned]).count,0,'completed card closure must not retain a stale in-flight case')
assert.equal(financeOverview({...data,cases:[{...reversedCase,state:'returned'}],trades:[auth,returned]}).closurePending,'0.23')
