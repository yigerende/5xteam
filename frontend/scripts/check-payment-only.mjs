import assert from 'node:assert/strict'
import { applyPaymentPolicies, paymentOnlySummary } from '../src/payment-only.js'
import { financeOverview, financeReport } from '../src/ejh-finance.js'
import { financeTotals } from '../src/nexovcc-finance.js'
import { reconciliation } from '../src/nexovcc-reconciliation.js'

const at='2026-10-26T10:00:00+08:00'
const policy={card_id:'c',mode:'payment_only',note:'只付款测试',updated_at:at,revision:1}
const ejh={reversal_fee_percent:0,recharge_ledger:{amount:'1000'},balance:{data:{abroadList:[{currency:'USD',balance:'789.50'}]}},
 cards:[{id:'c',issuer_linked:true,balance:'0',balance_at:at,remote:{orderNo:'c',status:'1',cardCurrency:'USD',startTime:at}}],
 cases:[{id:'order',card_id:'c',email:'fixture@example.com',state:'waiting',consumption_total:1000,charge:{trade_no:'a',type:'1',status:'1',card_amount:'200',card_currency:'USD'}}],
 trades:[{id:'a',card_id:'c',trade:{tradeNo:'a',authCode:'auth',tradeType:'1',tradeStatus:'1',cardAmount:'200',cardCurrency:'USD',billCurrency:'USD',billAmount:'200',createdAt:at}},
 {id:'b',card_id:'c',trade:{tradeNo:'b',authCode:'auth',tradeType:'4',tradeStatus:'2',cardAmount:'200',cardCurrency:'USD',billCurrency:'USD',billAmount:'200',createdAt:at}}],payment_policies:[policy]}
const original=structuredClone(ejh), result=financeOverview(ejh)
assert.equal(result.paymentOnly.amount,20000)
assert.equal(result.platformClearingUSD,'0.00')
assert.equal(result.earnings.costUSD,'0.50','recharge fee is disclosed separately from operating cost')
assert.equal(result.gap,'0.00')
assert.equal(result.inFlight.count,0)
assert.equal(applyPaymentPolicies(ejh).cases[0].state,'no_return')
assert.equal(applyPaymentPolicies({...ejh,payment_policies:[{...policy,mode:'normal'}]}).cases[0].state,'waiting')
assert.deepEqual(ejh,original,'display calculations never mutate actual evidence')
const duplicate={...ejh,trades:[...ejh.trades,...ejh.trades]}
assert.equal(paymentOnlySummary(duplicate,'ejh').amount,20000)
for(const mode of ['day','week','month','all'])assert.equal(financeReport(ejh,{},mode,'2026-10-26').summary.clearingUSD,'0.00')
const refunded=structuredClone(ejh)
refunded.cards[0].balance='200'
refunded.trades.push({id:'r',card_id:'c',trade:{tradeNo:'r',authCode:'auth',tradeType:'16',tradeStatus:'2',cardAmount:'200',cardCurrency:'USD',billCurrency:'USD',billAmount:'200',createdAt:at}})
assert.equal(financeOverview(refunded).paymentOnly.amount,0)
assert.equal(financeOverview(refunded).gap,'0.00')
const partial=structuredClone(refunded);partial.cards[0].balance='50';partial.trades.at(-1).trade.cardAmount='50';partial.trades.at(-1).trade.billAmount='50'
assert.equal(financeOverview(partial).paymentOnly.amount,15000)
assert.equal(financeOverview(partial).gap,'0.00')
const pending={...ejh,trades:[ejh.trades[0]]}
assert.equal(financeOverview(pending).gap,'0.00','authorization principal moves from in-flight to separate expense exactly once')
const unknown={...ejh,trades:[],cases:[{...ejh.cases[0],charge:null}]}
assert.equal(financeOverview(unknown).gap,null)
assert.equal(financeOverview(unknown).earnings.profitR,null)
const manual={...unknown,payment_policies:[{...policy,amount_usd:'200'}]}
assert.equal(financeOverview(manual).paymentOnly.amount,20000)
assert.equal(financeOverview(manual).gap,'0.00')

const nex={balance:{amount:'789.50',at},cards:[{card:{id:'c',serial_no:'s',status:'1',balance:'0',balance_at:at}}],
 finance:{at,recharges:[{status:'1',currency:'USD',amount:'1000',received:'990'}],funds:[{id:'open',kind:3001,serial_no:'s',amount:'-200',currency:'USD',at},{id:'fee',kind:3003,serial_no:'s',amount:'-0.50',currency:'USD',at}]},
 groups:[{id:'g',serial_no:'s',valid:true,payment:'200',returned:'0',difference:'200',settled:true,fully_returned:false,at}],
 cases:[{id:'a',card_id:'c',payment_status:'success',state:'waiting',revenue_r:1000}],payment_policies:[policy]}
assert.equal(financeTotals(nex).cost,1050)
assert.equal(reconciliation(nex).gap,0)
assert.equal(financeTotals(nex).paymentOnly.amount,20000)
const np=structuredClone(nex);np.groups[0].settled=false
assert.equal(financeTotals(np).pending,0)
assert.equal(reconciliation(np).gap,0)
const nr=structuredClone(nex);nr.groups[0].returned='200';nr.groups[0].difference='0';nr.groups[0].fully_returned=true;nr.cards[0].card.balance='200'
assert.equal(financeTotals(nr).paymentOnly.amount,0)
assert.equal(reconciliation(nr).gap,0)
const closed=structuredClone(nex);closed.cards[0].card.status='4'
assert.equal(financeTotals(closed).paymentOnly.amount,20000,'closed cards retain expense history')
const other=structuredClone(nex);other.cards.push({card:{id:'other',serial_no:'other',status:'1',balance:'0',balance_at:at}});other.groups.push({...other.groups[0],id:'other',serial_no:'other',payment:'20',difference:'20'})
assert.equal(financeTotals(other).paymentOnly.amount,20000)
assert.equal(financeTotals(other).cost,3050,'ordinary cards keep their original costs')
const multi=structuredClone(partial)
multi.cards[0].balance='80'
multi.trades.push({...structuredClone(multi.trades.at(-1)),id:'r2',trade:{...multi.trades.at(-1).trade,tradeNo:'r2',cardAmount:'30',billAmount:'30'}})
assert.equal(financeOverview(multi).paymentOnly.amount,12000,'two distinct partial refunds sharing an auth must both count')
assert.equal(financeOverview(multi).gap,'0.00')
const usd=structuredClone(ejh)
usd.trades.forEach(t=>{t.trade.cardCurrency='PHP';t.trade.cardAmount='12000';t.trade.billAmount='200'})
usd.trades[0].trade.billAmount='0'
assert.equal(financeOverview(usd).paymentOnly.amount,20000,'actual USD clearing overrides a missing authorization settlement')
assert.equal(financeOverview(usd).gap,'0.00')
const invalidReturn=structuredClone(refunded)
invalidReturn.trades.at(-1).trade.cardAmount='bad'
invalidReturn.trades.at(-1).trade.billAmount=''
invalidReturn.payment_policies[0].amount_usd='200'
assert.equal(financeOverview(invalidReturn).gap,null,'manual principal cannot invent a missing refund amount')
const nm=structuredClone(nex);nm.groups=[];nm.payment_policies[0].amount_usd='200'
assert.equal(reconciliation(nm).gap,0,'manual principal never adds an inferred cost again')
const invalidNex=structuredClone(nex);invalidNex.groups[0].valid=false;invalidNex.payment_policies[0].amount_usd='200'
assert.equal(reconciliation(invalidNex).gap,null)
console.log('Passed: both issuers, authorization/clearing deduplication, partial/full refunds, closed history, missing/manual amounts, isolated classification, period profits and cash reconciliation')
