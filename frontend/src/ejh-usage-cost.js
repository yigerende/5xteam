import { authorizationGroups, casePaymentTrades, tradeUnits, tradeMoney, uniqueTrades } from './ejh-clearing.js'
import { summarizeCardAmountsUSD } from './ejh-card-funding.js'
import { returnPostedAt } from './ejh-trade-sort.js'

const received = t => ['2','5','16'].includes(t?.tradeType) && (t.tradeStatus === '2' || t.tradeType === '2' && t.tradeStatus === '1')
const currencyOK = v => /^[A-Z]{3}$/.test(v || '')

// Only completed, evidenced account returns establish usage. Card closure is a
// transfer, not a refund, and never determines whether this expense is known.
// Keep a separate entry per original payment; later reads replace the same
// calculation instead of appending another charge or changing issuer records.
export function summarizeUsageCosts(cases = [], cards = [], operations = [], trades = [], {includeUnlinked=true} = {}) {
 const byCard = new Map(), owners = new Map(), candidates = [], seenCases = new Set()
 const cardsByID = new Map(cards.map(c => [c.id,c])), opsByKey = new Map()
 for (const op of operations) for (const key of new Set([op.card_id && 'id:'+op.card_id,op.order_no && 'order:'+op.order_no,op.result?.orderNo && 'order:'+op.result.orderNo].filter(Boolean))) {
  if (!opsByKey.has(key)) opsByKey.set(key,[])
  opsByKey.get(key).push(op)
 }
 let waiting = 0
 for (const t of uniqueTrades(trades)) {
  if (!byCard.has(t.card_id)) byCard.set(t.card_id, [])
  byCard.get(t.card_id).push(t)
 }
 // Historical closed cards can outlive their account association. Issuer-posted
 // refunds remain evidence even when `confirmed` (local assignment) is false.
 // Never infer a return from closure alone or use another account's payment.
 const represented = new Set(), rowsByCard = new Map(), groupsByCard = new Map()
 for (const row of cases) {
  const card = row.ejh_card_id || row.card_id
  if (!rowsByCard.has(card)) rowsByCard.set(card,[])
  rowsByCard.get(card).push(row)
  if (row.charge) for (const r of casePaymentTrades(row,byCard.get(card)||[])) if (r.trade?.authCode) represented.add(card+':'+r.trade.authCode)
 }
 for (const group of authorizationGroups(trades)) {
  if (!groupsByCard.has(group.card_id)) groupsByCard.set(group.card_id,[])
  groupsByCard.get(group.card_id).push(group)
 }
 const workingCases = [...cases]
 for (const [card,groups] of groupsByCard) {
  const linked = rowsByCard.get(card)||[], unknown = linked.filter(r=>!r.charge)
  const unassigned = groups.filter(g=>!represented.has(card+':'+g.reference))
  const fallback = unknown.length===1 && unknown[0].state==='returned' && unassigned.length===1 ? unknown[0] : null
  for (const group of unassigned) {
   if (group.invalid || !group.authorization || !group.records.some(r=>received(r.trade))) continue
   if (linked.some(r=>r.payment_only||r.pro_dead)||cardsByID.get(card)?.payment_policy?.mode==='payment_only') continue
   if (!fallback && (!includeUnlinked || unknown.length || cardsByID.get(card)?.remote?.status!=='2')) continue
   const original = group.records.find(r=>r.trade?.tradeType==='1'&&tradeUnits(r.trade.cardAmount)===group.authorization)
   const returns = group.records.filter(r=>['2','5','16'].includes(r.trade?.tradeType))
   if (!original || !returns.length || returns.some(r=>!received(r.trade))) continue
   const synthetic = {...(fallback||{}),id:fallback?.id||'card-return:'+card+':'+group.reference,ejh_card_id:card,state:'returned',
    charge:{id:original.id,trade_no:original.trade.tradeNo,type:'1',status:original.trade.tradeStatus,card_amount:original.trade.cardAmount,card_currency:original.trade.cardCurrency},
    returns:returns.map(r=>({...r,payment_order_id:fallback?.id||'',confirmed:true,confirmed_amount:r.trade.cardAmount,confirmed_currency:r.trade.cardCurrency})),usage_from_issuer:true}
   if (fallback) workingCases[workingCases.indexOf(fallback)]=synthetic
   else workingCases.push(synthetic)
  }
 }
 for (const row of workingCases) {
  if (row.pro_dead || row.payment_only) continue
  if (row.state !== 'returned') { if (['opened','waiting'].includes(row.state)) waiting++; continue }
  if (row.id && seenCases.has(row.id)) continue
  if (row.id) seenCases.add(row.id)
  const card = row.ejh_card_id || row.card_id
  const related = casePaymentTrades(row, byCard.get(card) || [])
  const original = related.find(r => r.id === row.charge?.id || r.trade?.tradeNo === row.charge?.trade_no)
  const key = card + ':' + (original?.trade?.authCode ? 'auth:' + original.trade.authCode : 'trade:' + (row.charge?.trade_no || row.charge?.id || row.id))
  owners.set(key, (owners.get(key) || 0) + 1)
  candidates.push({row, card, related, key})
 }
 const entries = [], reasons = [], totals = new Map()
 let totalUSD = 0n, missing = 0, pending = 0, coveredCount = 0
 for (const {row, card, related, key} of candidates) {
  const returned = uniqueTrades((row.returns || []).filter(r => ['2','5','16'].includes(r.trade?.tradeType)))
  if (!returned.length || returned.some(r => !r.confirmed || !received(r.trade))) { pending++; continue }
  const dates = returned.map(returnPostedAt)
  const at = dates.every(Boolean) ? Math.max(...dates) : null
  const identity = {id:'usage:'+key,order_id:row.id,email:row.email||'',card_id:card,reference:row.charge?.trade_no||row.charge?.id||row.id,at,estimated:true,fromIssuer:!!row.usage_from_issuer}
  const fail = (reason,entryExists=false) => {
   missing++; reasons.push({id:row.id,email:row.email,reason})
   if (!entryExists) entries.push({...identity,amount:null,currency:row.charge?.card_currency||'',usd:null,incomplete:true,reason})
  }
  if (!row.charge) { fail('原付款金额待核实'); continue }
  if (owners.get(key) !== 1) { fail('原付款关联多个账号，使用消耗待核实'); continue }
  const auth = related.filter(r => r.trade?.tradeType === '1' && ['1','2'].includes(r.trade.tradeStatus)).sort((a,b) => Number(b.trade.cardAmount)-Number(a.trade.cardAmount))[0]?.trade
  const charge = row.charge, currency = auth?.cardCurrency || charge.card_currency
  const paid = tradeUnits(auth?.cardAmount ?? charge.card_amount)
  if (paid === null || !currencyOK(currency) || !['1','4'].includes(charge.type) || !['1','2'].includes(charge.status)) { fail('付款金额或币种待核实'); continue }
  if (returned.some(r => r.card_id && r.card_id !== card || r.payment_order_id && row.id && r.payment_order_id !== row.id || tradeUnits(r.confirmed_amount) === null || r.confirmed_currency !== currency)) { fail('回款金额、币种或归属待核实'); continue }
  const receipt = returned.reduce((s,r) => s + tradeUnits(r.confirmed_amount), 0n)
  if (receipt > paid) { fail('回款超过原付款，使用消耗待核实'); continue }
  const difference = paid - receipt
  if (!difference) continue
  // A later posted partial capture already contributes to clearing cost.
  // Deduct that portion here, so delayed settlement never charges usage twice.
  const settled = related.filter(r => ['4','19','5','16'].includes(r.trade?.tradeType) && r.trade.tradeStatus === '2')
  if (settled.some(r => tradeUnits(r.trade.cardAmount) === null || r.trade.cardCurrency !== currency)) { fail('清算金额或币种待核实'); continue }
  const hasClearing = settled.some(r => ['4','19'].includes(r.trade.tradeType))
  const netClearing = hasClearing ? settled.reduce((s,r) => s + (['5','16'].includes(r.trade.tradeType) ? -1n : 1n) * tradeUnits(r.trade.cardAmount), 0n) : 0n
  const covered = netClearing > 0n ? (netClearing < difference ? netClearing : difference) : 0n
  if (covered) coveredCount++
  const amount = difference - covered
  if (!amount) continue
  const cardRow = cardsByID.get(card)
  const cardOps = [...new Set([...(opsByKey.get('id:'+card)||[]),...(opsByKey.get('order:'+cardRow?.remote?.orderNo)||[])])]
  const converted = summarizeCardAmountsUSD([{card_id:card,amount:tradeMoney(amount),currency}], cardRow?[cardRow]:[], cardOps)
  const incomplete = !!converted.missing || converted.amount === null
  const entry = {...identity,amount:tradeMoney(amount,2),currency,usd:incomplete?null:converted.amount,incomplete,covered:tradeMoney(covered,2)}
  entries.push(entry)
  totals.set(currency,(totals.get(currency)||0n)+amount)
  if (incomplete) fail('缺少开卡 USD 折算依据',true)
  else totalUSD += tradeUnits(converted.amount)
 }
 return {entries,count:entries.length,missing,pending,waiting,coveredCount,reasons,estimated:entries.length>0,rows:[...totals].map(([currency,n])=>({currency,amount:tradeMoney(n,2)})),usd:{amount:missing?null:tradeMoney(totalUSD,2),knownAmount:tradeMoney(totalUSD,2),missing,estimated:entries.length>0}}
}
