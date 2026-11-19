import assert from 'node:assert/strict'
import { applyRefundFeeOverrides, summarizeRefundFees } from '../src/ejh-refund-cost.js'
import { summarizeClearingCosts, casePaymentTrades } from '../src/ejh-clearing.js'
import { summarizeInFlightPayments } from '../src/ejh-account-amounts.js'
import { financeOverview, financeReport } from '../src/ejh-finance.js'
import { sortCardsByReturnState } from '../src/card-workflow-sort.js'

const at='2026-10-26T11:31:47+08:00'
const trade=(id,type,amount,bill,status='2')=>({id,card_id:'c',confirmed:true,reversal_fee:{percent:5,locked_at:at},trade:{tradeNo:id,authCode:'A',tradeType:type,tradeStatus:status,cardAmount:amount,cardCurrency:'PHP',billAmount:bill,billCurrency:'USD',tradeTime:at,createdAt:at}})
const auth=trade('auth','1','8919.64','0','1'),clear=trade('clear','4','8919.64','141.00'),refund=trade('refund','5','8919.64','141.00')
const card={id:'c',balance:'0',balance_at:at,remote:{orderNo:'c',status:'1',cardCurrency:'PHP'}}
const op={id:'open',method:'open',status:'completed',card_id:'c',result:{cardAmount:'10000',cardCurrency:'PHP',billAmount:'159.24',billCurrency:'USD'}}
const row={id:'account',email:'test@example.test',card_id:'c',ejh_card_id:'c',state:'waiting',requested_at:at,consumption_total:148.272083898,charge:{id:'clear',trade_no:'clear',type:'4',status:'2',card_amount:'8919.64',card_currency:'PHP',bill_amount:'141.00',bill_currency:'USD'},returns:[]}
const base={cards:[card],operations:[op],cases:[row],trades:[auth,clear],balance:{data:{abroadList:[{currency:'USD',balance:'848.50'}]}},recharge_ledger:{amount:'1000'},reversal_fee_percent:8}
let x=financeOverview(base)
assert.equal(x.platformClearingUSD,'0.00','full clearing with a refund request is recoverable principal')
assert.equal(x.inFlight.paymentUSD.amount,'141.00','use actual settlement, not opening FX')
assert.equal(x.inFlight.count,1)
assert.equal(x.earnings.costUSD,'0.50')
assert.equal(x.gap,'0.00')
assert.equal(financeReport(base,{},'day','2026-10-26').summary.clearingUSD,'0.00')
const complete={...base,cases:[{...row,state:'returned',returns:[refund]}],trades:[auth,clear,refund]}
x=financeOverview(complete)
assert.equal(x.platformClearingUSD,'0.00')
assert.equal(x.refundFees.amount,'7.05','locked 5% of actual USD refund; current 8% cannot reprice')
assert.equal(x.inFlight.count,0)
assert.equal(x.earnings.refundR,'47.2350')
assert.equal(summarizeRefundFees([refund,refund],8).usd.amount,'7.05')
assert.equal(summarizeClearingCosts(casePaymentTrades(complete.cases[0],complete.trades),[card],[op],complete.cases).usd.amount,'0.00')
for(const mode of ['day','week','month','all'])assert.equal(financeReport(complete,{},mode,'2026-10-26').summary.refundUSD,'7.05')
assert.equal(financeReport(complete,{},'day','2026-10-27').summary.refundUSD,'0.00')
const half=trade('partial','16','4459.82','70.50')
const partial={...base,cases:[{...row,returns:[half]}],trades:[auth,clear,half]}
assert.equal(financeOverview(partial).platformClearingUSD,'0.00')
assert.equal(financeOverview(partial).inFlight.paymentUSD.amount,'70.50')
assert.equal(financeOverview(partial).refundFees.amount,'3.53','per-refund rounding')
assert.equal(financeOverview({...partial,trades:[...partial.trades,trade('partial2','5','4459.82','70.50')]}).refundFees.amount,'7.06','distinct partial refunds cannot collapse')
assert.equal(financeOverview({...base,cases:[{...row,state:'opened',requested_at:''}]}).platformClearingUSD,'141.00','no refund request must not hide ordinary purchases')
const small={...clear,trade:{...clear.trade,cardAmount:'13.34',billAmount:'0.22'}}
assert.equal(financeOverview({...base,trades:[auth,small]}).platformClearingUSD,'0.22','real partial clearing costs remain costs')
assert.equal(summarizeRefundFees([{...refund,reversal_fee:{percent:0}}],8).usd.amount,'0.00')
assert.equal(summarizeRefundFees([{...refund,reversal_fee:{percent:null}}],8).missing,1)
assert.equal(summarizeRefundFees([{...refund,reversal_fee:undefined}],8).usd.amount,'11.28')
assert.equal(summarizeRefundFees([trade('pending','5','8919.64','141','1')]).pending,1)
assert.equal(summarizeRefundFees([auth,clear,trade('closure','17','10000','159.24')]).count,0)
assert.equal(summarizeRefundFees([{...refund,trade:{...refund.trade,billAmount:'',cardCurrency:'USD',cardAmount:'100'}}]).usd.amount,'5.00')
assert.equal(summarizeRefundFees([{...refund,trade:{...refund.trade,billAmount:''}}]).missing,1)

const second={...refund,id:'refund2',card_id:'other',trade:{...refund.trade,tradeNo:'refund2'}}
const manual={id:'manual',revision:1,amount:'12.34',at,source:'manual',note:'verified total',trade_ids:['refund','refund2']}
const overridden=applyRefundFeeOverrides({...complete,trades:[...complete.trades,second],refund_fees:[manual]})
assert.equal(summarizeRefundFees(overridden.trades).usd.amount,'12.34')
assert.equal(summarizeRefundFees(overridden.cases[0].returns).usd.amount,'6.17')
assert.equal(financeOverview(overridden).refundFees.amount,'12.34','registered total replaces linked estimates once')
assert.equal(financeReport(overridden,{},'day','2026-10-26').summary.refundUSD,'12.34')
const voided=applyRefundFeeOverrides({...overridden,refund_fees:[{...manual,voided:true}]})
assert.equal(summarizeRefundFees(voided.trades).usd.amount,'14.10','void removes stale overrides and resumes estimates')
const odd=applyRefundFeeOverrides({...overridden,refund_fees:[{...manual,amount:'12.35'}]})
assert.equal(summarizeRefundFees(odd.trades).usd.amount,'12.35','allocation retains every cent')
const noReturn={...complete,payment_policies:[{card_id:'c',mode:'payment_only'}]}
assert.equal(financeOverview(noReturn).refundFees.amount,'7.05','payment-only classification does not remove actual fees')

const cards=['returned','waiting','opening','opened','new','mixed','only'].map(id=>({id,local:{pending_accounts:id==='opening'?1:0}}))
const cases=[{card_id:'returned',state:'returned'},{card_id:'waiting',state:'waiting'},{card_id:'opened',state:'opened'},{card_id:'mixed',state:'returned'},{card_id:'mixed',state:'opened'},{card_id:'only',state:'no_return'}]
const expected=['opening','new','opened','mixed','waiting','returned','only']
assert.deepEqual(sortCardsByReturnState(cards,cases).map(c=>c.id),expected)
assert.deepEqual(sortCardsByReturnState(cards.map(c=>({card:{id:c.id},local:c.local})),cases).map(c=>c.card.id),expected)
assert.equal(cards[0].id,'returned','sorting never mutates shared data')
console.log('Passed: full/partial refunds, in-flight principal, actual USD fee basis, locked/current/zero rates, deduplication, manual replacement, period reports and both card sorts')
