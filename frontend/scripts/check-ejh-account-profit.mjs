import assert from 'node:assert/strict'
import { accountEarnings, createAccountEarnings } from '../src/ejh-account-profit.js'
import { financeReport, prepareFinanceReport } from '../src/ejh-finance.js'
import { applyPaymentPolicies } from '../src/payment-only.js'
import { applyRefundFeeOverrides } from '../src/ejh-refund-cost.js'

const at='2026-11-19T12:00:00+08:00'
const row={id:'order',email:'one@example.test',card_id:'card',payment_status:'success',opened_at:at,state:'returned',consumption_total:88.6245,returns:[{id:'return',card_id:'card',confirmed:true,reversal_fee:{percent:6},trade:{tradeNo:'return',tradeType:'2',tradeStatus:'2',cardAmount:'765',cardCurrency:'PHP'}}]}
const cards=[{id:'card',remote:{orderNo:'opening'}}]
const operations=[{id:'open',order_no:'opening',method:'open',status:'completed',result:{cardAmount:'1000',cardCurrency:'PHP',billAmount:'100',billCurrency:'USD'}}]
const expected={consumptionR:'88.6245',consumptionNotPushed:false,profitR:'57.8715',estimated:true,reason:''}
assert.deepEqual(accountEarnings(row,cards,operations),expected,'screenshot: 88.6245 - 4.59 * 6.7')
assert.equal(accountEarnings({...row,consumption_total:0},cards,operations).profitR,'-30.7530')
assert.equal(accountEarnings({...row,consumption_total:null},cards,operations).profitR,null)
assert.equal(accountEarnings({...row,consumption_total:null,consumption_state:'not_pushed'},cards,operations).profitR,'-30.7530')
assert.equal(accountEarnings({...row,consumption_total:null,consumption_state:'not_pushed'},cards,operations).consumptionR,null)
assert.equal(accountEarnings({...row,consumption_state:'not_pushed'},cards,operations).consumptionNotPushed,false)
assert.equal(accountEarnings(row,[],[]).reason,'费用待核实')
assert.equal(accountEarnings(row,cards,operations,[],20).profitR,'57.8715','locked rate wins over current rate')
assert.equal(accountEarnings({...row,merchant_fee_usd:'1.00'},cards,operations).profitR,'51.1715')
assert.equal(accountEarnings({...row,returns:row.returns.map(r=>({...r,confirmed:false}))},cards,operations).profitR,null)

const trade=(id,type,amount)=>({id,card_id:'card',trade:{tradeNo:id,authCode:'auth',tradeType:type,tradeStatus:'2',cardAmount:amount,cardCurrency:'USD',billAmount:amount,billCurrency:'USD'}})
const charged={...row,returns:[],consumption_total:100,charge:{id:'charge',trade_no:'charge',type:'1',status:'2',card_amount:'10',card_currency:'USD'}}
const trades=[trade('charge','1','10'),trade('clear','4','10')]
assert.equal(accountEarnings(charged,cards,operations,trades).profitR,'33.0000')
assert.equal(accountEarnings({...charged,pro_dead:{at}},cards,operations,trades).profitR,'33.0000','dead principal and clearing counted once')
assert.equal(accountEarnings({...charged,payment_only:true},cards,operations,trades).profitR,'100.0000','same exclusion as account detail')
assert.equal(accountEarnings({...charged,state:'waiting',requested_at:at},cards,operations,trades).profitR,'100.0000','full capture awaiting refund is recoverable principal')

const data={cases:[row],cards,operations,trades:[],recharge_ledger:{amount:'0'}}
const original=structuredClone(data)
const prepared=prepareFinanceReport(data)
for(const mode of ['day','week','month','all'])assert.deepEqual(financeReport(data,{},mode,'2026-11-19',prepared).openedAccountRows[0].earnings,expected)
assert.equal(financeReport(data,{},'day','2026-11-18',prepared).openedAccounts,0)
assert.deepEqual(data,original,'calculation is read-only')
const repeat={...data,cases:[row,{...row,id:'new',opened_at:'2026-11-20T01:00:00+08:00',consumption_total:10}]}
assert.equal(financeReport(repeat,{},'day','2026-11-19').openedAccountRows[0].earnings.consumptionR,'88.6245')
assert.equal(financeReport(repeat,{},'all').openedAccountRows[0].earnings.consumptionR,'10.0000')

// Indexed and complete evidence must agree, including orderNo-only FX lookup,
// duplicate transactions, payment policy, a deleted card and multiple cards.
for(const variant of [data,{...data,cards:[]}, {...data,cases:[charged],trades}, {...data,cases:[charged],trades,payment_policies:[{card_id:'card',mode:'payment_only'}]}]){
 const normalized=applyRefundFeeOverrides(applyPaymentPolicies(variant))
 normalized.trades.push(trade('unrelated','4','999'))
 normalized.trades.at(-1).card_id='unrelated'
 const indexed=createAccountEarnings(normalized)
 for(const account of normalized.cases){
  assert.deepEqual(indexed(account),accountEarnings(account,normalized.cards,normalized.operations,normalized.trades))
  assert.strictEqual(indexed(account),indexed(account),'date changes reuse cached earnings')
 }
}
console.log('Passed: account revenue/balance, screenshot, unknown/no-push/zero/negative, locked fees, clearing/dead/payment-only, opening-date selection, indexed evidence and immutability')
