import { cents, money, financeOverview, financeTime } from './ejh-finance.js'
import { summarizeCardBalancesUSD } from './ejh-card-funding.js'
import { summarizeInFlightPayments } from './ejh-account-amounts.js'
import { isClosedCard } from './card-status.js'

// A display tolerance only. Never write off the difference or deduct it as cost.
export const reconciliationToleranceUSD = '1.00'
export function reconciliationStatus(data, now = Date.now(), maxAgeSeconds = 600, overview = null) {
  const value = overview || financeOverview(data), issues = [...value.lossReasons]
  if (value.earnings.costIncomplete) issues.push('部分费用缺少金额或折算依据')
  if (data.sync?.error) issues.push('卡片或交易同步失败，保留上次数据')
  if (value.stale) issues.push(`${value.stale} 张卡余额查询失败`)
  const liveCards = (data.cards || []).filter(c => !isClosedCard(c))
  const timestamps = [data.balance?.at, data.sync?.last_success, ...liveCards.map(c => c.balance_at)].map(financeTime)
  const oldest = timestamps.some(t => t === null) ? null : Math.min(...timestamps)
  const ageLimit = Math.max(600, Number(maxAgeSeconds) || 600) * 1000
  const sources = [{id:'account',label:'EJH 账户余额',at:data.balance?.at},{id:'trades',label:'卡片与交易',at:data.sync?.last_success},...liveCards.map(c=>({id:c.id,label:'卡片 '+(c.remote?.cardNo?.slice(-4)||c.local?.name||c.id)+(c.normal_pending?'（正常待定）':''),at:c.balance_at}))]
  const freshness = sources.filter(s=>financeTime(s.at)===null||now-financeTime(s.at)>ageLimit).map(s=>({...s,at:financeTime(s.at)}))
  if (oldest === null) issues.push('部分查询时间未记录')
  else if (now - oldest > ageLimit) issues.push('部分余额或交易较旧，建议刷新核对')
  let state = 'unknown', label = '数据待补齐'
  const gap = cents(value.gap)
  if (gap !== null) {
    if (issues.length) { state = 'stale'; label = '数据待刷新' }
    else if (gap === 0n) { state = 'balanced'; label = '账面平衡' }
    else if ((gap < 0n ? -gap : gap) <= cents(reconciliationToleranceUSD)) { state = 'small'; label = '小额估算差异' }
    else { state = 'difference'; label = '存在待核对差额' }
  }
  return { value, state, label, oldest, freshness, issues: [...new Set(issues)], tolerance: reconciliationToleranceUSD }
}

export function reconciliationCards(data) {
  const operations = data.operations || [], trades = data.trades || [], cases = data.cases || []
  return (data.cards || []).filter(c => c.issuer_linked !== false && !isClosedCard(c)).map(card => {
    const cardUSD = summarizeCardBalancesUSD([card], operations)
    const linked = cases.filter(c => (c.ejh_card_id || c.card_id) === card.id)
    const inFlight = summarizeInFlightPayments(linked, [card], operations, trades.filter(t => t.card_id === card.id))
    const balance = cardUSD.missing ? null : cardUSD.amount ?? '0.00'
    const pending = inFlight.paymentUSD.missing ? null : inFlight.paymentUSD.amount ?? '0.00'
    const total = balance === null || pending === null ? null : money(cents(balance) + cents(pending))
    return { id: card.id, name: card.local?.name || card.remote?.cardNo || card.remote?.orderNo || card.id,
      balance, pending, total, original: card.balance_at ? card.balance : null, currency: card.remote?.cardCurrency,
      at: card.balance_at, error: card.balance_error || card.error || '',
      processing: card.remote?.status === '8' || card.local?.issuer_cancel_status === 'processing' }
  }).sort((a, b) => Number(!!b.error) - Number(!!a.error) || a.name.localeCompare(b.name))
}
