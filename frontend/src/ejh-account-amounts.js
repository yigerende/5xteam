import { summarizeCardAmountsUSD } from './ejh-card-funding.js'
import { authorizationGroups, remainingAuthorization, casePaymentTrades } from './ejh-clearing.js'
import { returnPostedAt } from './ejh-trade-sort.js'

// Original currency totals use six decimal places and integer arithmetic.
const scale=1000000n
function units(value){
 const s=String(value??'')
 if(!/^\d{1,12}(\.\d{1,6})?$/.test(s))return null
 const [whole,fraction='']=s.split('.')
 return BigInt(whole)*scale+BigInt(fraction.padEnd(6,'0'))
}
function format(value){
 const n=value<0n?-value:value
 return (value<0n?'-':'')+String(n/scale)+'.'+String(n%scale).padStart(6,'0').replace(/0+$/,'').padEnd(2,'0')
}
const returnReceived=t=>['2','5','16'].includes(t?.tradeType)&&(t.tradeStatus==='2'||t.tradeType==='2'&&t.tradeStatus==='1')
const currencyOK=value=>/^[A-Z]{3}$/.test(value||'')
function tradeAt(t){
 const parse=s=>{if(!s)return 0;return Date.parse(/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d$/.test(s)?s.replace(' ','T')+'+08:00':s)||0}
 const at=parse(t.trade?.tradeTime),recorded=parse(t.trade?.createdAt)
 if(!at)return recorded
 const corrected=at+8*3600000,lag=recorded-corrected
 return !String(t.trade.tradeTime).includes('T')&&recorded&&lag>=0&&lag<=30*60000?corrected:at
}
const earlier=(a,b)=>tradeAt(a)!==tradeAt(b)?tradeAt(a)<tradeAt(b):String(a.id||'')<String(b.id||'')
// Issuer card amounts, deduplicated by authorization. Never add clearing to
// its authorization, or count card-closure balance transfers as account refunds.
export function cardAmounts(card,trades=[]){
 const payments=new Map(),returns=new Map(),totals=new Map()
 const key=t=>t.trade.authCode?'auth:'+t.trade.authCode:'trade:'+t.trade.tradeNo
 const rank=t=>t.trade.tradeType==='1'?3:t.trade.tradeType==='4'&&t.trade.tradeStatus==='2'?2:1
 let invalid=false,pending=0
 for(const t of trades){
  if(t.card_id!==card.id)continue
  const v=t.trade||{},paid=['1','4'].includes(v.tradeType),returned=['2','5','16'].includes(v.tradeType)
  if(!paid&&!returned)continue
  if(returned&&!returnReceived(v)){pending++;continue}
  if(paid&&!['1','2'].includes(v.tradeStatus))continue
  const n=units(v.cardAmount)
  if(n===null||!currencyOK(v.cardCurrency)||!v.tradeNo){invalid=true;continue}
  const group=paid?payments:returns,k=key(t),old=group.get(k)
  const linked=t.confirmed&&!!t.payment_order_id,oldLinked=old?.confirmed&&!!old.payment_order_id
  const prefer=old&&(paid?(rank(t)>rank(old)||rank(t)===rank(old)&&String(t.id||'')<String(old.id||'')):(Boolean(linked)!==Boolean(oldLinked)?linked:earlier(t,old)))
  if(!old||prefer)group.set(k,t)
 }
 for(const [group,field] of [[payments,'paid'],[returns,'returned']])for(const t of group.values()){
  const v=t.trade,row=totals.get(v.cardCurrency)||{currency:v.cardCurrency,paid:0n,returned:0n,authorization:false}
  row[field]+=units(v.cardAmount);if(field==='paid'&&v.tradeType==='1')row.authorization=true
  totals.set(v.cardCurrency,row)
 }
 const rows=[...totals.values()].sort((a,b)=>a.currency.localeCompare(b.currency)).map(r=>({...r,paid:format(r.paid),returned:format(r.returned),difference:invalid?null:format(r.paid-r.returned)}))
 return {rows,invalid,pending,fee:'0.50 USDT'}
}
export function accountAmounts(row,trades=[]){
 let charge=row?.charge
 const original=casePaymentTrades(row,trades).filter(r=>r.trade?.tradeType==='1'&&['1','2'].includes(r.trade.tradeStatus)&&units(r.trade.cardAmount)!==null).sort((a,b)=>units(a.trade.cardAmount)===units(b.trade.cardAmount)?0:units(a.trade.cardAmount)>units(b.trade.cardAmount)?-1:1)[0]
 if(original)charge={...charge,type:'1',status:original.trade.tradeStatus,card_amount:original.trade.cardAmount,card_currency:original.trade.cardCurrency}
 const paid=charge&&['1','4'].includes(charge.type)&&['1','2'].includes(charge.status)&&currencyOK(charge.card_currency)?units(charge.card_amount):null
 const totals=new Map(),seen=new Set()
 let pending=0,invalid=false,returnedAt=0
 for(const t of row?.returns||[]){
  if(!['2','5','16'].includes(t.trade?.tradeType))continue
  if(!t.confirmed||!returnReceived(t.trade)){pending++;continue}
  if(t.id&&seen.has(t.id))continue
  if(t.id)seen.add(t.id)
  const n=units(t.confirmed_amount),currency=t.confirmed_currency
  if(n===null||!currencyOK(currency)){invalid=true;continue}
  totals.set(currency,(totals.get(currency)||0n)+n)
  returnedAt=Math.max(returnedAt,returnPostedAt(t))
 }
 const returned=[...totals].sort(([a],[b])=>a.localeCompare(b)).map(([currency,n])=>({amount:format(n),currency}))
 const comparable=paid!==null&&(charge.status==='2'||charge.type==='1'&&charge.status==='1')&&!invalid&&[...totals.keys()].every(c=>c===charge.card_currency)
 const hold=remainingAuthorization(row,authorizationGroups(trades))
 const remaining=!['opened','waiting'].includes(row?.state)?'0.00':hold?.missing?null:(hold?.amount?format(units(hold.amount)):null)??(comparable?format(paid-(totals.get(charge.card_currency)||0n)):null)
 return {paid:paid===null?null:format(paid),currency:paid===null?'':charge.card_currency,authorization:charge?.type==='1',posted:charge?.status==='2',returned,pending,invalid,returnedAt,
  remaining,
  difference:comparable?format(paid-(totals.get(charge.card_currency)||0n)):null,
  reason:paid===null?'等待自动识别付款或金额缺失':charge.status!=='2'&&charge.type!=='1'?'付款流水待入账':invalid?'回款金额待核实':!comparable?'币种不同，不能直接相减':''}
}

// Use the one payment attributed to each case, not all raw authorization and
// clearing rows. Keep card, transaction and settlement currencies independent.
export function summarizeInFlightPayments(cases=[],cards=[],operations=[],trades=[]){
 const groups=authorizationGroups(trades)
 const closedCards=new Set(cards.filter(c=>c.remote?.status==='2'||c.payment_policy?.mode==='payment_only').map(c=>c.id))
 const orders=new Map(),payments=new Map(),payment=new Map(),transaction=new Map(),settlement=new Map(),conversions=[]
 for(const row of cases)if(!row.pro_dead&&!row.payment_only&&['opened','waiting'].includes(row.state)&&!closedCards.has(row.ejh_card_id||row.card_id))orders.set(row.id||row,row)
 let missingPayment=0,missingTransaction=0,missingSettlement=0,pendingSettlement=0,authorizations=0
 const missingRows=[]
 const missing=(row,reason)=>missingRows.push({id:row.id,email:row.email||'',card_id:row.ejh_card_id||row.card_id||'',reason})
 for(const row of orders.values()){
  let charge=row.charge
  const valid=charge&&(['1','4'].includes(charge.type)&&charge.status==='2'||charge.type==='1'&&charge.status==='1')
  if(!valid){missing(row,charge?'付款流水状态待确认':'尚未匹配到付款流水');missingPayment++;missingTransaction++;missingSettlement++;continue}
  const card=row.ejh_card_id||row.card_id||''
  const remaining=remainingAuthorization(row,groups)
  if(remaining?.missing){missing(row,'授权、清算或回款金额不完整');missingPayment++;missingTransaction++;missingSettlement++;continue}
  if(remaining){
   if(units(remaining.amount)===0n)continue
   charge={...charge,type:remaining.refundPending?'4':'1',status:remaining.refundPending?'2':'1',card_amount:remaining.amount,card_currency:remaining.currency,bill_amount:remaining.settlementUSD??'0',bill_currency:'USD',trade_amount:'',trade_currency:'',refund_pending:remaining.refundPending}
  }
  const key=remaining?.key||(charge.trade_no?`${card}:trade:${charge.trade_no}`:charge.id?`id:${charge.id}`:row.id||row)
  const previous=payments.get(key)
  if(!previous||(Date.parse(charge.synced_at)||0)>(Date.parse(previous.charge.synced_at)||0))payments.set(key,{charge,card_id:card,row})
 }
 const add=(totals,value,currency)=>{
  const n=units(value)
  if(n===null||!currencyOK(currency))return false
  totals.set(currency,(totals.get(currency)||0n)+n);return true
 }
 const unrecognized=missingPayment
 for(const {charge,card_id,row} of payments.values()){
  const actualRefundUSD=charge.refund_pending&&units(charge.bill_amount)>0n
  conversions.push({card_id,amount:actualRefundUSD?charge.bill_amount:units(charge.card_amount)===null?null:charge.card_amount,currency:actualRefundUSD?'USD':charge.card_currency,row})
  if(charge.type==='1')authorizations++
  if(!add(payment,charge.card_amount,charge.card_currency))missingPayment++
  if(!add(transaction,charge.trade_amount,charge.trade_currency))missingTransaction++
  // EJH can return zero transaction/bill fields while cardAmount contains a
  // real authorization hold. Zero is not an actual USD settlement in this case.
  if(units(charge.card_amount)>0n&&units(charge.bill_amount)===0n){
   if(charge.type==='1'&&charge.status==='1')pendingSettlement++
   else missingSettlement++
  }else if(!add(settlement,charge.bill_amount,charge.bill_currency))missingSettlement++
 }
 const rows=totals=>[...totals].sort(([a],[b])=>a.localeCompare(b)).map(([currency,n])=>({amount:format(n),currency}))
 const paymentUSD=summarizeCardAmountsUSD(conversions,cards,operations)
 if(paymentUSD.missing)for(const entry of conversions){
  if(summarizeCardAmountsUSD([entry],cards,operations).missing)missing(entry.row,units(entry.amount)===null?'付款金额缺失':'付款币种或 USD 折算依据缺失')
 }
 // Cases without a recognized charge still represent unknown in-flight money.
 paymentUSD.missing+=unrecognized;paymentUSD.total+=unrecognized
 return {count:orders.size,payment:rows(payment),transaction:rows(transaction),settlement:rows(settlement),missingPayment,missingTransaction,missingSettlement,pendingSettlement,authorizations,paymentUSD,missingRows,authorizationKeys:[...payments.keys()]}
}
