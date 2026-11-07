import { ejhDeadLoss } from './dead-account-loss.js'
import { applyPaymentPolicies, paymentOnlySummary, paymentMoney } from './payment-only.js'
import { summarizeReversalFees } from './ejh-reversal-fees.js'
import { financeProfit, usdToR } from './ejh-profit.js'
import { summarizeCardBalancesUSD } from './ejh-card-funding.js'
import { summarizeInFlightPayments } from './ejh-account-amounts.js'
import { authorizationDiscrepancies } from './ejh-reconciliation.js'
import { summarizeClearingCosts } from './ejh-clearing.js'
import { refundFeeSummary, refundFeeEntries } from './ejh-refund-fees.js'
import { applyRefundFeeOverrides, summarizeRefundFees } from './ejh-refund-cost.js'

// Decimal cents for all USD/USDT arithmetic; R remains the downstream counter.
export function cents(value) {
  if (!/^-?\d{1,15}(\.\d{1,6})?$/.test(String(value ?? ''))) return null
  const negative=String(value).startsWith('-'),[whole,fraction='']=String(value).replace('-','').split('.')
  return (BigInt(whole)*100n+(BigInt(fraction.padEnd(6,'0'))+5000n)/10000n)*(negative?-1n:1n)
}
export function money(value) {
  if(value===null||value===undefined)return null
  const n=value<0n?-value:value
  return `${value<0n?'-':''}${n/100n}.${String(n%100n).padStart(2,'0')}`
}
const feeCents = value => {const n=cents(value);return n===null?null:(n+50n)/100n}
export function financeTime(value) {
  if(!value)return null
  const text=String(value),n=Date.parse(/^\d{4}-\d\d-\d\d[ T]\d\d:\d\d:\d\d(?:\.\d+)?$/.test(text)?text.replace(' ','T')+'+08:00':text)
  return Number.isFinite(n)&&n>0?n:null
}
export function beijingDay(value=Date.now()) {
  const n=typeof value==='number'?value:financeTime(value)
  return n===null?'':new Date(n+8*3600000).toISOString().slice(0,10)
}
export function consumptionSummary(cases=[],history=[]) {
  const accounts=new Map(),seen=new Set()
  for(const row of history){
    if(seen.has(row.id)||!Number.isFinite(row.total)||row.total<0)continue
    seen.add(row.id);const key=row.email?.toLowerCase()||row.id
    accounts.set(key,(accounts.get(key)||0)+row.total)
  }
  for(const row of cases){
    const key=row.email?.trim().toLowerCase()||row.id,n=row.consumption_total
    if(!accounts.has(key))accounts.set(key,null)
    if(typeof n==='number'&&Number.isFinite(n)&&n>=0)accounts.set(key,Math.max(accounts.get(key)||0,n))
  }
  let total=0,missing=0
  for(const n of accounts.values()){if(n===null)missing++;else total+=n}
  return {total:accounts.size&&missing===accounts.size?null:total,missing,count:accounts.size}
}
export function openingEntries(data) {
  const unique=new Map(),ops=data.operations||[]
  for(const card of data.cards||[]){
    if(card.issuer_linked===false||!card.remote?.orderNo)continue
    const op=ops.filter(o=>o.method==='open'&&['completed','resolved','import_pending'].includes(o.status)&&(o.order_no===card.remote.orderNo||o.card_id===card.id||o.result?.orderNo===card.remote.orderNo)).sort((a,b)=>(financeTime(a.created_at)||Infinity)-(financeTime(b.created_at)||Infinity))[0]
    const at=financeTime(op?.created_at||card.remote?.startTime)
    unique.set(card.remote.orderNo,{id:'open:'+card.remote.orderNo,kind:'opening',at,card:card.local?.name||card.remote.cardNo||card.remote.orderNo,reference:card.remote.orderNo,amount:'0.50',currency:'USDT',usd:null,note:at?'每张 0.50 USDT':'历史导入卡，开卡日期未记录'})
  }
  for(const op of ops){
    const order=op.order_no||op.result?.orderNo
    if(op.method!=='open'||!['completed','resolved','import_pending'].includes(op.status)||!order||unique.has(order))continue
    unique.set(order,{id:'open:'+order,kind:'opening',at:financeTime(op.created_at),card:order,reference:order,amount:'0.50',currency:'USDT',usd:null,note:'每张 0.50 USDT'})
  }
  return [...unique.values()]
}
export function financeOverview(data={}) {
  data=applyRefundFeeOverrides(applyPaymentPolicies(data))
  const paymentOnly=paymentOnlySummary(data,'ejh'),dead=ejhDeadLoss(data)
  const cards=data.cards||[],operations=data.operations||[],cases=data.cases||[]
  const reversal=summarizeReversalFees(data.trades||[],data.reversal_fee_percent??6,false,cards,operations)
  const opening=openingEntries(data),consumption=consumptionSummary(cases,data.consumption_history||[])
  const gross=cents(data.recharge_ledger?.amount),deposit=feeCents(data.recharge_ledger?.amount),net=gross===null?null:gross-deposit
  const cardUSD=summarizeCardBalancesUSD(cards,operations),inFlight=summarizeInFlightPayments(cases,cards,operations,data.trades||[])
  const clearing=summarizeClearingCosts(data.trades||[],cards,operations,cases)
  const externalTransfer=cents(data.recharge_ledger?.external_transfer_amount||'0')
  const rawClearingUSD=clearing.usd.amount
  const platformClearingUSD=externalTransfer===null||cents(rawClearingUSD)===null?null:money(cents(rawClearingUSD)-externalTransfer-BigInt(paymentOnly.costExcluded)-BigInt(dead.alreadyCost))
  const merchantFee=money((data.merchant_fees||[]).reduce((sum,row)=>sum+(cents(row.amount)||0n),0n))
  const registeredRefunds=refundFeeSummary(data.refund_fees||[])
  const automaticRefunds=summarizeRefundFees((data.trades||[]).filter(r=>!r.refund_fee_override),data.reversal_fee_percent??6,false,cards,operations)
  const refundFees={...registeredRefunds,amount:money((cents(registeredRefunds.amount)||0n)+(cents(automaticRefunds.usd.amount)||0n)),count:registeredRefunds.count+automaticRefunds.count,manualCount:registeredRefunds.count,automaticCount:automaticRefunds.count,missing:registeredRefunds.missing+automaticRefunds.missing+automaticRefunds.pending,estimated:registeredRefunds.estimated||automaticRefunds.estimated}
  const reasons=[];let account=0n
  const balances=data.balance?.data?.abroadList
  if(!Array.isArray(balances)||!balances.length)reasons.push('EJH 账户余额未查询')
  else {
    const seen=new Set()
    for(const item of balances){
      const n=cents(item.balance)
      if(n===null||seen.has(item.currency)){reasons.push('EJH 账户余额待核实');continue}
      seen.add(item.currency)
      if(item.currency==='USD')account+=n
      else if(n!==0n)reasons.push('EJH 非 USD 余额缺少折算依据')
    }
  }
  if(net===null)reasons.push('充值总账未填写')
  if(paymentOnly.missing)reasons.push('只付款支出金额待核实')
  if(externalTransfer===null)reasons.push('平台外转出金额待核实')
  if(cardUSD.missing)reasons.push(`${cardUSD.missing} 张卡余额或折算依据缺失`)
  if(inFlight.paymentUSD.missing)reasons.push(`${inFlight.paymentUSD.missing} 笔在途付款待核实`)
  const loss=reasons.length?null:net-externalTransfer-BigInt(paymentOnly.amount)-account-(cents(cardUSD.amount)||0n)-(cents(inFlight.paymentUSD.amount)||0n)
  const openingFee=money(BigInt(opening.length)*50n)
  const earnings=financeProfit({openingUSD:openingFee,reversalUSD:reversal.usd.amount??(!reversal.count?'0.00':null),rechargeUSD:money(deposit),clearingUSD:platformClearingUSD,refundUSD:refundFees.amount,merchantUSD:merchantFee,deadUSD:money(BigInt(dead.amount)),consumption:consumption.total,
    costMissing:dead.missing+paymentOnly.missing+reversal.missing+reversal.pending+reversal.usd.missing+clearing.missing+clearing.pending+refundFees.missing,consumptionMissing:consumption.missing,estimated:!!reversal.count||clearing.estimated||refundFees.estimated})
  const reconciliation=authorizationDiscrepancies(cases,(data.trades||[]).filter(t=>!paymentOnly.rows.some(p=>p.card_id===t.card_id)),cards,operations,inFlight.authorizationKeys)
  const gap=loss===null||earnings.costUSD===null?null:money(loss-(cents(earnings.costUSD)-deposit-(cents(merchantFee)||0n)))
  const closurePending=reconciliation.unaccountedUSD.missing?null:reconciliation.unaccountedUSD.amount??'0.00'
  const unexplained=gap===null||closurePending===null?null:money(cents(gap)-cents(closurePending))
  const assets=reasons.length?null:money(account+(cents(cardUSD.amount)||0n)+(cents(inFlight.paymentUSD.amount)||0n))
  return {dead,paymentOnly,reversal,earnings,merchantFee,openingCount:opening.length,openingFee,consumption,netR:usdToR(money(net)),lossR:usdToR(money(loss)),
    reconciliation,gap,clearing,rawClearingUSD,platformClearingUSD,externalTransfer:money(externalTransfer),refundFees,closurePending,unexplained,assets,
    gross:money(gross),depositFee:money(deposit),net:money(net),loss:money(loss),lossReasons:[...new Set(reasons)],
    estimated:!!(cardUSD.estimated||inFlight.paymentUSD.estimated),stale:cards.filter(c=>c.remote?.status!=='2'&&c.balance_error).length,
    accountUSD:money(account),cardUSD,inFlight}
}
export function periodRange(mode,date=beijingDay()) {
  const at=financeTime(date+'T00:00:00+08:00')
  if(mode==='all')return {start:-Infinity,end:Infinity,label:'全部记录'}
  if(at===null)return {start:Infinity,end:-Infinity,label:'日期无效'}
  let start=at,end=at+86400000,label=date
  if(mode==='week'){
    const weekday=new Date(at+8*3600000).getUTCDay()||7
    start=at-(weekday-1)*86400000;end=start+7*86400000;label=beijingDay(start)+' — '+beijingDay(end-1)
  }else if(mode==='month'){
    start=financeTime(date.slice(0,7)+'-01T00:00:00+08:00')
    const d=new Date(start+8*3600000);d.setUTCMonth(d.getUTCMonth()+1)
    end=d.getTime()-8*3600000;label=date.slice(0,7)
  }
  return {start,end,label}
}
export function financeEvents(data,history={}) {
  data=applyRefundFeeOverrides(applyPaymentPolicies(data))
  const paymentOnly=paymentOnlySummary(data,'ejh'),dead=ejhDeadLoss(data)
  const events=openingEntries(data),seen=new Map(),cards=data.cards||[],operations=data.operations||[]
  for(const r of dead.rows){events.push({id:"dead:"+r.id,kind:"dead",at:financeTime(r.at),email:r.email,card:r.card_id,usd:r.amount==null?null:paymentMoney(r.amount),amount:r.amount==null?null:paymentMoney(r.amount),currency:"USD",incomplete:r.missing,estimated:r.estimated,note:"死号本金亏损，已扣实际回款"});if(r.alreadyCost)events.push({id:"dead-reclass:"+r.id,kind:"clearing",at:financeTime(r.at),usd:paymentMoney(-r.alreadyCost),amount:paymentMoney(-r.alreadyCost),currency:"USD",note:"已计清算转入死号亏损，避免重复成本"})}
 for(const fee of data.merchant_fees||[])events.push({id:'merchant:'+fee.request_id,kind:'merchant',at:financeTime(fee.at),email:fee.email,card:fee.card_id,reference:fee.request_id,amount:fee.amount,currency:'USD',usd:fee.amount,note:'API 商户最终成功 · 外部收费'})
  for(const fee of refundFeeEntries(data.refund_fees||[])) {
    const valid=/^\d{1,12}(\.\d{1,2})?$/.test(String(fee.amount??''))&&(cents(fee.amount)||0n)>0n
    events.push({id:'refund-fee:'+fee.id,kind:'refund',at:financeTime(fee.at),reference:fee.id,amount:valid?fee.amount:null,currency:'USD',usd:valid?fee.amount:null,incomplete:!valid,estimated:fee.source==='balance_reconciliation',note:(fee.source==='balance_reconciliation'?'余额核对登记':'手动登记')+' · '+fee.note})
  }
  const historyMonth=/^20\d{2}-(0[1-9]|1[0-2])$/.test(data.recharge_ledger?.history_month||'')?data.recharge_ledger.history_month:null
  for(const e of summarizeClearingCosts(data.trades||[],cards,operations,data.cases||[]).entries){
    const t=e.record.trade
    events.push({id:'clearing:'+e.record.card_id+':'+(t.tradeNo||e.record.id),kind:'clearing',at:financeTime(t.createdAt)||financeTime(t.tradeTime),card:cards.find(c=>c.id===e.record.card_id)?.local?.name||t.orderNo,reference:t.tradeNo,amount:e.amount,currency:e.currency,usd:e.usd,usd_precise:e.usd,incomplete:e.incomplete,note:e.refundPending?'已申请退款的清算本金 · 计入在途，不计成本':e.pending?'清算待入账，未计成本':e.refund?'清算退款冲减成本':e.estimated?'清算成本 · USD 估算':'清算成本 · 已入账'})
  }
  for(const record of (data.trades||[]).filter(r=>['5','16'].includes(r.trade?.tradeType)&&!r.refund_fee_override)){
    const fee=summarizeRefundFees([record],data.reversal_fee_percent??6,false,cards,operations),t=record.trade
    events.push({id:'refund-estimate:'+record.id,kind:'refund',at:financeTime(t.createdAt)||financeTime(t.tradeTime),card:cards.find(c=>c.id===record.card_id)?.local?.name||t.orderNo,reference:t.tradeNo,amount:fee.usd.amount,currency:'USD',usd:fee.usd.amount,incomplete:!!(fee.missing||fee.pending),estimated:true,note:fee.pending?'退款尚未入账，手续费待确认':`退款手续费 · 按 ${record.reversal_fee?.percent??data.reversal_fee_percent??6}% ${record.reversal_fee?'已回款锁定费率':'当前配置'}计算`})
  }
  for(const row of paymentOnly.rows){
    if(row.missing){events.push({id:'payment-only-missing:'+row.card_id,kind:'clearing',at:null,usd:null,incomplete:true,note:'只付款支出待核实'});continue}
    const net=row.entries.reduce((s,e)=>s+(cents(e.usd)||0n),0n)
    // Full source evidence can be reclassified on its original date. A partial
    // manual fallback stays explicitly undated instead of inventing dates.
    if(net===BigInt(row.costExcluded))for(const e of row.entries){
      const t=e.record.trade,n=cents(e.usd)
      if(n===null||e.incomplete)continue
      events.push({id:'payment-only:'+row.card_id+':'+(t.tradeNo||e.record.id),kind:'clearing',at:financeTime(t.createdAt)||financeTime(t.tradeTime),card:row.last4,reference:t.tradeNo,usd:money(-n),amount:money(-n),currency:'USD',note:'只付款本金单列，冲减业务清算成本'})
    }else if(row.costExcluded)events.push({id:'payment-only:'+row.card_id,kind:'clearing',at:null,card:row.last4,usd:paymentMoney(-row.costExcluded),amount:paymentMoney(-row.costExcluded),currency:'USD',paymentOnlyUndated:true,note:'只付款本金单列 · 历史日期待核实'})
  }
  // A separate signed exclusion preserves issuer trades. The first cumulative
  // amount has no known spend date; later edits are dated accounting changes.
  let recordedTransfer=0n
  for(const event of new Map((history.external_transfers||data.external_transfer_history||[]).map(e=>[e.id,e])).values()){
    const delta=cents(event.delta)
    if(delta===null){
      events.push({id:'external:'+event.id,kind:'clearing',externalTransfer:true,at:financeTime(event.at),reference:event.id,amount:null,usd:null,currency:'USD',incomplete:true,note:'平台外转出调整金额待核实'})
      continue
    }
    recordedTransfer+=delta
    if(delta===0n)continue
    events.push({id:'external:'+event.id,kind:'clearing',externalTransfer:true,at:event.baseline?null:financeTime(event.at),reference:event.id,amount:money(-delta),usd:money(-delta),currency:'USD',note:'平台外转出 · '+(event.baseline?'累计历史扣减，消费日期未记录':'按登记时间调整清算成本')+(event.note?' · '+event.note:'')})
  }
  const transfer=cents(data.recharge_ledger?.external_transfer_amount||'0')
  if(transfer!==null&&transfer!==recordedTransfer){
    events.push({id:'external-transfer-baseline',kind:'clearing',externalTransfer:true,at:null,amount:money(recordedTransfer-transfer),usd:money(recordedTransfer-transfer),currency:'USD',note:'平台外转出 · 未记录日期的累计扣减'+(data.recharge_ledger?.external_transfer_note?' · '+data.recharge_ledger.external_transfer_note:'')})
  }
  for(const record of data.trades||[]){
    if(record.trade?.tradeType!=='2')continue
    const key=(record.card_id||record.trade.orderNo||'')+':'+(record.trade.tradeNo||record.id)
    const prev=seen.get(key)
    if(!prev||(financeTime(record.synced_at)||0)>=(financeTime(prev.synced_at)||0))seen.set(key,record)
  }
  for(const [key,record] of seen){
    const fee=summarizeReversalFees([record],data.reversal_fee_percent??6,false,cards,operations,true),t=record.trade
    events.push({id:'reversal:'+key,kind:'reversal',at:financeTime(t.tradeTime)||financeTime(t.createdAt),card:cards.find(c=>c.id===record.card_id)?.local?.name||t.orderNo,reference:t.tradeNo,
      email:(data.cases||[]).find(c=>c.id===record.payment_order_id)?.email||'',amount:fee.rows[0]?.amount??null,currency:fee.rows[0]?.currency||t.cardCurrency||'',usd:money(cents(fee.usd.amount)),usd_precise:fee.usd.amount,
      incomplete:!!(fee.missing||fee.pending||fee.usd.missing),note:fee.pending?'待确认，未计入':`按 ${fee.percent}% 估算${record.reversal_fee?'（已回款锁定）':'（当前设置）'}`})
  }
  for(const event of history.recharges||data.recharge_history||[]){
    const after=feeCents(event.after||'0'),before=feeCents(event.before||'0')
    events.push({id:event.id,kind:'recharge',at:event.baseline?null:financeTime(event.at),month:event.baseline&&(!data.recharge_ledger?.history_month_recorded_at||financeTime(event.at)<=financeTime(data.recharge_ledger.history_month_recorded_at))?historyMonth:null,reference:event.id,amount:money(after-before),currency:'USD',usd:money(after-before),note:event.baseline?(historyMonth?'历史充值归属 '+historyMonth+'，具体日期未记录':'历史充值基数，未分摊日期'):'充值总账变动 '+event.delta+' USD'})
  }
  const usageDays=[...new Map((history.usage_days||[]).map(row=>[row.id,row])).values()]
  for(const row of usageDays)if(row.amount!==0)events.push({id:row.id,day:row.day,kind:'consumption',at:financeTime(row.day+'T12:00:00+08:00'),email:row.email,reference:row.id,amount:row.amount,currency:'R',note:row.source==='sub2_daily'?'Sub2 实际用户扣费 · 北京时间':'旧采集增量 · 等待按实际消费日期核对'})
  const deposit=feeCents(data.recharge_ledger?.amount)
  const recordedDeposit=events.filter(e=>e.kind==='recharge').reduce((n,e)=>n+(cents(e.usd)||0n),0n)
  if(deposit!==null&&deposit!==recordedDeposit)events.push({id:'legacy-recharge',kind:'recharge',at:null,month:historyMonth,amount:money(deposit-recordedDeposit),usd:money(deposit-recordedDeposit),currency:'USD',note:historyMonth?'历史充值归属 '+historyMonth+'，具体日期未记录':'未记录日期的历史充值手续费'})
  const consumption=consumptionSummary(data.cases||[],history.consumption||data.consumption_history||[])
  const recordedR=usageDays.reduce((n,r)=>n+(Number.isFinite(r.amount)?r.amount:0),0)
  if(consumption.total!==null&&consumption.total>recordedR+0.0000001)events.push({id:'legacy-consumption',kind:'consumption',at:null,amount:consumption.total-recordedR,currency:'R',note:'首次采集的历史累计消费，无法还原每天发生额'})
  return events.sort((a,b)=>(b.at??-Infinity)-(a.at??-Infinity)||String(a.id).localeCompare(String(b.id)))
}
export function eventAmountR(event) {
  if(event.incomplete)return null
  if(event.kind==='consumption')return financeProfit({openingUSD:0,reversalUSD:0,rechargeUSD:0,consumption:event.amount}).consumptionR
  return usdToR(event.kind==='opening'?event.amount:event.usd)
}
function summarizeEvents(events) {
  let usd=0n,usdt=0n,reversalUSD=0n,rechargeUSD=0n,clearingUSD=0n,externalTransferUSD=0n,refundUSD=0n,merchantUSD=0n,deadUSD=0n,r=0,missing=0,consumptionMissing=0,opening=0
  const original=new Map()
  for(const e of events){
    if(e.kind==='consumption'){
      if(e.amount===null||e.amount===undefined||e.amount===''||!Number.isFinite(Number(e.amount))||Number(e.amount)<0)consumptionMissing++
      else r+=Number(e.amount)
      continue
    }
    if(e.kind==='opening'){usdt+=cents(e.amount)||0n;opening++;continue}
    if(e.kind==='reversal'&&e.amount!==null){const n=cents(e.amount);if(n!==null)original.set(e.currency,(original.get(e.currency)||0n)+n)}
    const raw=e.usd_precise??e.usd
    const n=cents(raw)===null?null:BigInt(String(raw).replace('-','').split('.')[0])*1000000n+BigInt((String(raw).split('.')[1]||'').padEnd(6,'0'))
    if(n===null||e.incomplete){missing++;continue}
    const signed=String(raw).startsWith('-')?-n:n
    usd+=signed
    if(e.kind==='reversal')reversalUSD+=signed
    if(e.kind==='recharge')rechargeUSD+=signed
    if(e.kind==='clearing')clearingUSD+=signed
    if(e.externalTransfer)externalTransferUSD-=signed
    if(e.kind==='refund')refundUSD+=signed
    if(e.kind==='merchant')merchantUSD+=signed
 if(e.kind==='dead')deadUSD+=signed
  }
  const rounded=n=>money(((n<0n?-n:n)+5000n)/10000n*(n<0n?-1n:1n))
  const earnings=financeProfit({openingUSD:money(usdt),reversalUSD:rounded(reversalUSD),rechargeUSD:rounded(rechargeUSD),clearingUSD:rounded(clearingUSD),refundUSD:rounded(refundUSD),merchantUSD:rounded(merchantUSD),deadUSD:rounded(deadUSD),consumption:r,costMissing:missing,consumptionMissing,estimated:events.some(e=>e.kind==='reversal'||e.estimated)})
  return {usd:rounded(usd),usdt:money(usdt),reversalUSD:rounded(reversalUSD),rechargeUSD:rounded(rechargeUSD),clearingUSD:rounded(clearingUSD),externalTransferUSD:rounded(externalTransferUSD),refundUSD:rounded(refundUSD),merchantUSD:rounded(merchantUSD),deadUSD:rounded(deadUSD),r,missing:missing+consumptionMissing,opening,earnings,original:[...original].map(([currency,n])=>({currency,amount:money(n)}))}
}
export function financeReport(data,history={},mode='day',date=beijingDay()) {
  const range=periodRange(mode,date),all=financeEvents(data,history),overview=financeOverview({...data,consumption_history:history.consumption||data.consumption_history})
  const events=all.filter(e=>mode==='all'||e.month&&mode==='month'&&e.month===date.slice(0,7)||e.at!==null&&e.at>=range.start&&e.at<range.end)
  const summary=summarizeEvents(events)
  if(mode!=='all'&&all.some(e=>e.paymentOnlyUndated)){summary.earnings.profitR=null;summary.earnings.incomplete=true}
  if(mode==='all'){
    summary.r=overview.consumption.total
    summary.rechargeUSD=overview.depositFee
    summary.externalTransferUSD=overview.externalTransfer
    summary.clearingUSD=overview.platformClearingUSD
    summary.usd=overview.depositFee===null?null:money((cents(summary.reversalUSD)||0n)+(cents(summary.clearingUSD)||0n)+(cents(summary.refundUSD)||0n)+(cents(summary.merchantUSD)||0n)+BigInt(overview.dead.amount)+cents(overview.depositFee))
    summary.missing+=overview.consumption.missing
    summary.earnings=overview.earnings
  }
  const groups=new Map()
  for(const e of events){
    const key=financeEventPeriod(e,mode)
    if(!groups.has(key))groups.set(key,[])
    groups.get(key).push(e)
  }
  const rows=[...groups].map(([day,list])=>({day,...summarizeEvents(list)})).sort((a,b)=>b.day.localeCompare(a.day))
  // If the current ledger or an account's consumption is unknown, recorded
  // period entries are still useful but cannot establish a complete profit.
  for(const row of [summary,...rows]){
    if(overview.depositFee===null){row.missing++;row.earnings={...row.earnings,costR:null,costIncomplete:true,profitR:null,incomplete:true}}
    if(overview.consumption.missing||overview.paymentOnly.missing||mode!=='all'&&all.some(e=>e.paymentOnlyUndated))row.earnings={...row.earnings,profitR:null,incomplete:true}
  }
  const baseline=all.filter(e=>e.kind==='consumption'&&e.at===null).reduce((n,r)=>n+Number(r.amount),0)
  const dailyPending=(history.consumption||[]).filter(r=>!r.daily_checked_at||r.daily_error||r.source_at&&financeTime(r.source_at)>financeTime(r.daily_source_at)).length
  const dailyErrors=(history.consumption||[]).filter(r=>r.daily_error).length
  const monthlyUndated=all.filter(e=>e.month&&(mode==='all'||Number.isFinite(range.start)&&Number.isFinite(range.end)&&e.month>=beijingDay(range.start).slice(0,7)&&e.month<=beijingDay(range.end-1).slice(0,7))).length
  const undatedTransfers=all.filter(e=>e.externalTransfer&&e.at===null).length
  const periodIncomplete=mode!=='all'&&(baseline>0.000001||dailyPending>0||undatedTransfers>0||(history.usage_days||[]).some(r=>r.source!=='sub2_daily')||(['day','week'].includes(mode)&&monthlyUndated>0))
  if(periodIncomplete)for(const row of [summary,...rows])row.earnings={...row.earnings,profitR:null,incomplete:true}
  const snapshots=(history.snapshots||[]).filter(s=>{const at=financeTime(s.at);return at!==null&&at>=range.start&&at<range.end}).sort((a,b)=>financeTime(a.at)-financeTime(b.at))
  const first=snapshots[0],last=snapshots.at(-1)
  const firstLoss=first?financeOverview({...first,operations:data.operations||[]}).loss:null,lastLoss=last?financeOverview({...last,operations:data.operations||[]}).loss:null
  const samePolicies=JSON.stringify(first?.payment_policies||[])===JSON.stringify(last?.payment_policies||[])
  const lossChange=samePolicies&&first&&last&&financeTime(first.at)!==financeTime(last.at)&&firstLoss!==null&&lastLoss!==null?money(cents(lastLoss)-cents(firstLoss)):null
  return {range,summary,events,rows,overview,baseline,dailyPending,dailyErrors,periodIncomplete,monthlyUndated,undatedTransfers,undated:all.filter(e=>e.at===null&&!e.month&&e.kind!=='consumption').length,lossChange,firstAt:first?.at,lastAt:last?.at,
    coverage:(history.usage_days||[]).map(r=>r.day).sort()[0]||null}
}

// Reuse the dated ledger events without loading daily usage or full historical
// snapshots. Missing dates stay unallocated; cash differences are never fees.
export function dailyFinanceCost(data, date=beijingDay()) {
  const range=periodRange('day',date)
  const all=financeEvents(data).filter(e=>e.kind!=='consumption')
  const events=all.filter(e=>e.at!==null&&e.at>=range.start&&e.at<range.end)
  const earnings=summarizeEvents(events).earnings
  if(cents(data.recharge_ledger?.amount)===null){
    earnings.costUSD=null;earnings.costR=null;earnings.costIncomplete=true
    earnings.profitR=null;earnings.incomplete=true
  }
  return {date,earnings,undated:all.filter(e=>e.at===null&&(!e.month||e.month===date.slice(0,7))).length}
}

export function financeEventPeriod(event,mode) {
  if(event.month)return mode==='all'?event.month:event.month+'（日期未记录）'
  return event.at===null?'未分期历史':mode==='all'?beijingDay(event.at).slice(0,7):beijingDay(event.at)
}
