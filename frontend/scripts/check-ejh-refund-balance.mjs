import assert from 'node:assert/strict'
import {balanceProfit,financeProfit} from '../src/ejh-profit.js'
import {accountEarnings} from '../src/ejh-account-profit.js'
import {applyRefundFeeOverrides} from '../src/ejh-refund-cost.js'
import {financeReport} from '../src/ejh-finance.js'

const opened='2026-11-16T19:10:58+08:00',returned='2026-11-18T10:20:32+08:00'
const record=(id,type,amount,bill,at)=>({id,card_id:'card',confirmed:true,confirmed_amount:amount,confirmed_currency:'PHP',reversal_fee:{percent:5},trade:{tradeNo:id,authCode:'auth',tradeType:type,tradeStatus:'2',cardAmount:amount,cardCurrency:'PHP',billAmount:bill,billCurrency:'USD',createdAt:at,tradeTime:at}})
const auth=record('auth','1','5794.64','0',opened),clear=record('clear','4','5794.64','91.08',opened),refund=record('refund','5','5779.68','90.84',returned)
const row={id:'account',email:'refund@example.test',card_id:'card',payment_status:'success',state:'returned',opened_at:opened,requested_at:opened,consumption_total:66.3566,charge:{id:'auth',trade_no:'auth',type:'1',status:'2',card_amount:'5794.64',card_currency:'PHP'},returns:[refund]}
const card={id:'card',issuer_linked:true,remote:{orderNo:'open',cardCurrency:'PHP',startTime:opened}}
const data={cases:[row],cards:[card],trades:[auth,clear,refund],operations:[],reversal_fee_percent:8,recharge_ledger:{amount:'0'}}
const input={openingUSD:'0.50',reversalUSD:'0.00',rechargeUSD:0,clearingUSD:'0.24',refundUSD:'4.54',consumption:66.3566}
assert.equal(balanceProfit(input).costUSD,'5.28')
assert.equal(balanceProfit(input).costR,'35.3760')
assert.equal(balanceProfit(input).profitR,'30.9806','card includes refund and opening fees')
assert.equal(balanceProfit({...input,openingUSD:0}).profitR,'34.3306','account excludes opening fees')
assert.equal(financeProfit(input).costUSD,'0.74','operating costs remain unchanged')
assert.equal(financeProfit(input).profitR,'61.3986','operating profit still excludes refund fee')
for(const refundUSD of [null,'invalid'])assert.equal(balanceProfit({...input,refundUSD}).profitR,null)
assert.equal(balanceProfit({...input,refundMissing:1}).profitR,null)
assert.equal(balanceProfit({...input,consumption:null}).profitR,null)
assert.equal(balanceProfit({...input,refundUSD:'0.00'}).profitR,'61.3986')
assert.equal(balanceProfit({...input,consumption:0}).profitR,'-35.3760')
assert.equal(balanceProfit({...input,rechargeUSD:'999.99'}).profitR,'30.9806','shared recharge not assigned to a card')
assert.equal(accountEarnings(row,[card],[],data.trades,8).profitR,'34.3306','locked refund rate, clearing already net of principal')
assert.equal(accountEarnings({...row,returns:[refund,refund]},[card],[],[...data.trades,refund],8).profitR,'34.3306','duplicate receipts charge fee once')
assert.equal(accountEarnings({...row,returns:[{...refund,confirmed:false}]},[card],[],data.trades,8).profitR,null)
assert.equal(accountEarnings({...row,returns:[{...refund,reversal_fee:{percent:'invalid'}}]},[card],[],data.trades,8).profitR,null)
const next={...refund,id:'refund2',trade:{...refund.trade,tradeNo:'refund2',cardAmount:'0.00',billAmount:'1.00'}}
assert.equal(accountEarnings({...row,returns:[refund,next]},[card],[],data.trades,8).profitR,'33.9956','distinct partial refunds each contribute their own fee')
const manual=applyRefundFeeOverrides({...data,refund_fees:[{id:'fee',revision:1,amount:'3.00',at:returned,source:'manual',trade_ids:['refund']}]})
assert.equal(accountEarnings(manual.cases[0],manual.cards,[],manual.trades,8).profitR,'44.6486','manual fee replaces estimate instead of being added again')
const noRefund={...row,returns:[{...refund,trade:{...refund.trade,tradeType:'2',cardAmount:'100',cardCurrency:'USD'},reversal_fee:{percent:6}}]}
assert.equal(accountEarnings(noRefund,[card],[],data.trades).profitR,'24.5486','reversal fee remains deducted once')
const saved=structuredClone(data)
for(const mode of ['day','week','month','all']){
 const result=financeReport(data,{},mode,'2026-11-16')
 assert.equal(result.openedAccountRows[0].earnings.profitR,'34.3306','date selects opening; cross-day fee remains assigned to account')
}
assert.equal(financeReport(data,{},'day','2026-11-18').openedAccountRows.length,0)
assert.equal(financeReport(data,{},'day','2026-11-18').summary.refundUSD,'4.54')
assert.deepEqual(data,saved,'display correction never writes financial evidence')
console.log('Passed: cross-day refund fees deducted once in account/card balances, screenshot amounts, manual/partial/locked fees, unknown/negative/zero, unchanged report policy and opening dates')
