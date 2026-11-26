import assert from 'node:assert/strict'
import { financeOverview, financeReport, financeEvents, dailyFinanceCost, cents, beijingDay } from '../src/ejh-finance.js'
const card='ejh-'+ '00b89e962c075293a7d5fcacf1a44fc2',second='ejh-'+'f26ff91ecd69d691bcd81748a2634159'
const oldAt='2026-10-23T10:00:00+08:00',at='2026-11-26T17:55:00+08:00',later='2026-11-27T10:00:00+08:00'
const base={recharge_ledger:{amount:'1000',external_transfer_amount:'100.00'},balance:{data:{abroadList:[{currency:'USD',balance:'700.00'}]}},cards:[{id:card,balance:'0',remote:{cardCurrency:'USD',orderNo:card,status:'1'}}],payment_policies:[{card_id:card,mode:'payment_only',amount_usd:'45.50'}],trades:[{id:'a',card_id:card,trade:{tradeNo:'a',authCode:'auth',tradeType:'1',tradeStatus:'1',cardAmount:'45.50',cardCurrency:'USD',billAmount:'0',billCurrency:'USD',createdAt:at}}],external_transfer_history:[{id:'baseline',at:oldAt,delta:'100.00',baseline:true}]}
const moved=(data=base,explicit=false)=>({...structuredClone(data),recharge_ledger:{...data.recharge_ledger,external_transfer_amount:'54.50'},external_transfer_history:[...data.external_transfer_history,{id:'move',delta:'-45.50',at,note:'重新分类为只付款支出：'+card,...explicit?{kind:'payment_only_reclassification',card_id:card}:{}}]})
for(const explicit of [false,true]){
 const d=moved(base,explicit),copy=structuredClone(d)
 for(const mode of ['day','week','month'])assert.equal(financeReport(d,{},mode,'2026-11-26').summary.clearingUSD,'0.00','classification must not create current expense')
 assert.equal(dailyFinanceCost(d,'2026-11-26').earnings.clearingR,'0.0000')
 const event=financeEvents(d).find(e=>e.paymentOnlyReclassification)
 assert.equal(event.at,null);assert.equal(event.usd,'45.50');assert.equal(event.sourceReference,'baseline')
 assert.equal(financeReport(d,{},'day','2026-11-26').periodIncomplete,false,'old undated classification does not block current profit')
 assert.equal(financeReport(d,{},'all').rows.reduce((s,r)=>s+cents(r.clearingUSD),0n),cents(financeOverview(d).platformClearingUSD),'period grouping reconciles to unchanged cumulative amount')
 assert.deepEqual(copy,d,'source evidence never mutated')
 const cleared=structuredClone(d);cleared.trades.push({id:'b',card_id:card,trade:{...cleared.trades[0].trade,tradeNo:'b',tradeType:'4',tradeStatus:'2',billAmount:'45.50',createdAt:later}})
 assert.equal(financeReport(cleared,{},'day','2026-11-27').summary.clearingUSD,'0.00','later actual clearing remains excluded once')
 assert.equal(financeReport(cleared,{},'day','2026-11-26').summary.clearingUSD,'0.00')
 cleared.trades.push({id:'r',card_id:card,trade:{...cleared.trades[0].trade,tradeNo:'r',tradeType:'16',tradeStatus:'2',billAmount:'45.50',createdAt:'2026-11-28T10:00:00+08:00'}})
 for(const date of ['2026-11-26','2026-11-27','2026-11-28'])assert.equal(financeReport(cleared,{},'day',date).summary.clearingUSD,'0.00','refund/clearing remains neutral to operating cost')
}
const two=moved();two.cards.push({...two.cards[0],id:second,remote:{...two.cards[0].remote,orderNo:second}});two.payment_policies.push({card_id:second,mode:'payment_only',amount_usd:'45.50'});two.trades.push({...two.trades[0],id:'other',card_id:second});two.recharge_ledger.external_transfer_amount='9.00';two.external_transfer_history.push({id:'move2',delta:'-45.50',at:'2026-11-26T18:35:00+08:00',note:'重新分类为只付款支出：'+second})
assert.equal(financeReport(two,{},'day','2026-11-26').summary.earnings.costUSD,'0.00','both authorizations excluded')
const duplicated={...two,external_transfer_history:[...two.external_transfer_history,...two.external_transfer_history]};assert.deepEqual(financeEvents(duplicated),financeEvents(two))
const dated=structuredClone(base);dated.external_transfer_history[0].baseline=false;dated.external_transfer_history[0].at='2026-11-25T10:00:00+08:00'
const reclass=moved(dated,true)
assert.equal(financeReport(reclass,{},'day','2026-11-26').summary.clearingUSD,'0.00')
assert.equal(financeReport(reclass,{},'day','2026-11-25').summary.clearingUSD,'-54.50','old dated exclusion corrected on original date')
const sameDay=structuredClone(dated);sameDay.external_transfer_history[0].at=at
assert.equal(financeReport(moved(sameDay,true),{},'day','2026-11-26').summary.clearingUSD,'-54.50','same-day source correctly nets')
const split=structuredClone(dated);split.external_transfer_history=[{id:'old',delta:'20',at:oldAt,baseline:true},{id:'new',delta:'80',at:'2026-11-25T10:00:00+08:00'}]
const parts=financeEvents(moved(split,true)).filter(e=>e.paymentOnlyReclassification)
assert.deepEqual(parts.map(e=>[e.at===null?null:beijingDay(e.at),e.usd]),[['2026-11-25','25.50'],[null,'20.00']])
const missing=moved({...base,external_transfer_history:[]},true)
assert.equal(financeReport(missing,{},'day','2026-11-26').summary.clearingUSD,'0.00','unknown source date never invented')
const ordinary=moved();ordinary.external_transfer_history.at(-1).note='修正原累计值'
assert.equal(financeReport(ordinary,{},'day','2026-11-26').summary.clearingUSD,'45.50','ordinary total edit keeps existing behavior')
console.log('Passed: payment-only reclassification, two authorizations, legacy/typed records, later clearing/refund, original date allocation, unknown dates, deduplication, cumulative parity and daily profit')
