import { ejhDeadLoss } from './dead-account-loss.js'
import { casePaymentTrades, summarizeClearingCosts } from './ejh-clearing.js'
import { paymentOnlySummary, paymentMoney } from './payment-only.js'
import { balanceProfit } from './ejh-profit.js'
import { summarizeReversalFees, reversalProfit } from './ejh-reversal-fees.js'
import { summarizeRefundFees } from './ejh-refund-cost.js'
import { accountConsumption, accountConsumptionForProfit, consumptionNotPushed } from './ejh-consumption.js'

// Shared with the account detail. Opening/recharge fees are not allocated here.
export function accountCaseCosts(row, cards = [], operations = [], trades = []) {
 const dead = ejhDeadLoss({cases:[row],cards,operations,trades})
 const raw = summarizeClearingCosts(casePaymentTrades(row,trades),cards,operations,[row])
 let clearing = raw
 if(row.pro_dead) clearing = {...raw,usd:{...raw.usd,amount:raw.usd.amount==null?null:paymentMoney(Math.round(Number(raw.usd.amount)*100)-dead.alreadyCost)}}
 else if(row.payment_only){
  const id=row.ejh_card_id||row.card_id
  const summary=paymentOnlySummary({payment_policies:[{card_id:id,mode:'payment_only'}],cards,cases:[row],operations,trades:casePaymentTrades(row,trades)},'ejh')
  clearing={...raw,usd:{...raw.usd,amount:raw.usd.amount===null?null:paymentMoney(Math.round(Number(raw.usd.amount)*100)-summary.costExcluded)},missing:raw.missing+summary.missing}
 }
 return {dead,clearing}
}

export function accountProfit({fee,refund,clearing=null,deadFeeUSD='0.00',merchantFeeUSD='0.00',consumption=null,consumptionMissing=0}) {
 const base=reversalProfit(fee,consumption,consumptionMissing)
 if(deadFeeUSD!==null&&!Number(deadFeeUSD)&&!clearing&&!Number(merchantFeeUSD)&&!refund.count&&!refund.missing&&!refund.pending)return base
 const earnings=balanceProfit({openingUSD:0,rechargeUSD:0,reversalUSD:fee.usd.amount??(!fee.count?'0.00':null),refundUSD:refund.usd.amount??'0.00',deadUSD:deadFeeUSD,merchantUSD:merchantFeeUSD,clearingUSD:clearing?.usd.amount||'0.00',consumption,consumptionMissing,costMissing:refund.missing+refund.pending+fee.missing+fee.pending+fee.usd.missing+(clearing?.missing||0)+(clearing?.pending||0)})
 return {...base,profit:earnings.profitR,estimated:base.estimated||refund.estimated||clearing?.estimated}
}

export function accountEarnings(row, cards=[], operations=[], trades=[], feePercent=6) {
 const {dead,clearing}=accountCaseCosts(row,cards,operations,trades)
 const consumption=accountConsumptionForProfit(row)
 const result=accountProfit({fee:summarizeReversalFees(row.returns||[],feePercent,true,cards,operations),refund:summarizeRefundFees(row.returns||[],feePercent,true,cards,operations),clearing,deadFeeUSD:dead.missing?null:paymentMoney(dead.amount),merchantFeeUSD:row.merchant_fee_usd||'0.00',consumption})
 return {consumptionR:accountConsumption(row)?.toFixed(4)??null,consumptionNotPushed:consumptionNotPushed(row),profitR:result.profit,estimated:!!result.estimated,reason:result.profit===null?(consumption===null?'消费未查询完整':'费用待核实'):''}
}

// Preserve original ordering (including duplicate evidence) while indexing once.
function indexRows(rows, keys) {
 const index=new Map()
 rows.forEach((row,i)=>{for(const key of new Set(keys(row))){if(!index.has(key))index.set(key,[]);index.get(key).push(i)}})
 return values=>[...new Set(values.flatMap(key=>index.get(key)||[]))].sort((a,b)=>a-b).map(i=>rows[i])
}

// A date switch reuses cached results. No per-account network requests or
// repeated full-ledger scans; fees still use the original card's opening rate.
export function createAccountEarnings(data) {
 const cardsFor=indexRows((data.cards||[]).map(c=>c.card||c),c=>[c.id&&'id:'+c.id,c.remote?.orderNo&&'order:'+c.remote.orderNo].filter(Boolean))
 const operationsFor=indexRows(data.operations||[],o=>[o.card_id&&'id:'+o.card_id,o.order_no&&'order:'+o.order_no,o.result?.orderNo&&'order:'+o.result.orderNo].filter(Boolean))
 const tradesFor=indexRows(data.trades||[],r=>[r.card_id])
 const cache=new Map()
 return row=>{
  if(cache.has(row))return cache.get(row)
  const id=row.ejh_card_id||row.card_id,trades=tradesFor([id])
  const keys=new Set(id?['id:'+id]:[])
  for(const record of [...trades,...(row.returns||[])]){
   if(record.card_id)keys.add('id:'+record.card_id)
   if(record.trade?.orderNo)keys.add('order:'+record.trade.orderNo)
  }
  const cards=cardsFor([...keys])
  for(const card of cards){if(card.id)keys.add('id:'+card.id);if(card.remote?.orderNo)keys.add('order:'+card.remote.orderNo)}
  const result=accountEarnings(row,cards,operationsFor([...keys]),trades,data.reversal_fee_percent??6)
  cache.set(row,result)
  return result
 }
}
