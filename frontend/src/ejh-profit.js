// Fixed accounting rate: 1 USD = 6.7 R; opening USDT is valued at 1 USD.
// Use integer arithmetic and retain four decimals, matching consumption R.
function units(value) {
  if (typeof value === 'number') {
    if (!Number.isFinite(value) || Math.abs(value) >= 1e18) return null
    value = value.toFixed(6)
  }
  const text = String(value ?? '')
  if (!/^-?\d{1,18}(\.\d{1,6})?$/.test(text)) return null
  const [whole, fraction = ''] = text.replace('-', '').split('.')
  return (BigInt(whole) * 1000000n + BigInt(fraction.padEnd(6, '0'))) * (text.startsWith('-') ? -1n : 1n)
}
const rounded = (value, divisor) => ((value < 0n ? -value : value) + divisor / 2n) / divisor * (value < 0n ? -1n : 1n)
function format(value) {
  if (value === null) return null
  const n = value < 0n ? -value : value
  return `${value < 0n ? '-' : ''}${n / 10000n}.${String(n % 10000n).padStart(4, '0')}`
}
export function usdToR(value) {
  const n = units(value)
  return format(n === null ? null : rounded(n * 67n, 1000n))
}
function calculateProfit({ openingUSD, reversalUSD, rechargeUSD, clearingUSD = 0, refundUSD = 0, merchantUSD = 0, deadUSD = 0, consumption, costMissing = 0, consumptionMissing = 0, refundMissing = 0, rechargeMissing = 0, estimated = false }, deductRefund) {
  const usdCosts = [openingUSD, reversalUSD, clearingUSD, merchantUSD, deadUSD].map(units)
  if (deductRefund) usdCosts.push(units(refundUSD))
  const costs = usdCosts.map(n => {
    return n === null ? null : rounded(n * 67n, 1000n)
  })
  const partial = !!costMissing || deductRefund && !!refundMissing || costs.some(n => n === null)
  const known = costs.filter(n => n !== null)
  const cost = known.reduce((sum, n) => sum + n, 0n)
  const usd = usdCosts.filter(n => n !== null).reduce((sum,n) => sum+n,0n)
  const usdCents = rounded(usd,10000n)
  const usdText = `${usdCents<0n?'-':''}${(usdCents<0n?-usdCents:usdCents)/100n}.${String((usdCents<0n?-usdCents:usdCents)%100n).padStart(2,'0')}`
  const rawRevenue = units(consumption)
  const revenue = rawRevenue === null ? null : rounded(rawRevenue, 100n)
  const incomplete = partial || !!consumptionMissing || revenue === null
  return {
    openingR: format(costs[0]), reversalR: format(costs[1]), rechargeR: rechargeMissing ? null : usdToR(rechargeUSD), clearingR: format(costs[2]), refundR: refundMissing ? null : usdToR(refundUSD), merchantR: format(costs[3]), deadR: format(costs[4]),
    costR: partial ? null : format(cost), knownCostR: known.length ? format(cost) : null,
    costUSD: partial ? null : usdText, knownCostUSD: known.length ? usdText : null,
    consumptionR: format(revenue), profitR: incomplete ? null : format(revenue - cost),
    costIncomplete: partial, incomplete, estimated: !!estimated,
  }
}

// Period reports keep refund/recharge fees as separate disclosures.
export function financeProfit(input) { return calculateProfit(input, false) }

// Account/card balances deduct their own refund fees, regardless of the day
// they arrive. Recharge fees are shared and remain outside these balances.
export function balanceProfit(input) { return calculateProfit(input, true) }
