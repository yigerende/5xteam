import { uniqueTrades, tradeUnits, tradeMoney } from './ejh-clearing.js'

// MASTER_B1_5 clearing refunds remain with the issuer until card closure;
// they do not restore the spendable card balance. This is a receivable, not
// another receipt or another profit entry. Other card products are not inferred.
export function pendingRefundSettlement(cards = [], trades = []) {
 const byCard = new Map(cards.map(card => [card.id, card])), rows = []
 let total = 0n, missing = 0
 for (const record of uniqueTrades(trades)) {
  const t = record.trade || {}, card = byCard.get(record.card_id)
  if (!['5', '16'].includes(t.tradeType) || t.tradeStatus !== '2' ||
      (t.cardType || card?.remote?.cardType) !== 'MASTER_B1_5' || card?.remote?.status === '2') continue
  const refund = tradeUnits(t.refundBuyPrice)
  const valid = !!card && t.billCurrency === 'USD' && refund !== null
  if (!valid) missing++; else total += refund
  rows.push({ id: record.id, card_id: record.card_id, card: card?.local?.last4 || card?.remote?.cardNo || record.card_id,
   usd: valid ? tradeMoney(refund, 2) : null, at: t.createdAt || t.tradeTime, missing: !valid })
 }
 return { rows, count: rows.length, missing, amount: missing ? null : tradeMoney(total, 2), knownAmount: tradeMoney(total, 2) }
}
