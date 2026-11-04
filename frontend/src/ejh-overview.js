import { applyPaymentPolicies, paymentOnlySummary } from './payment-only.js'
import { applyRefundFeeOverrides } from './ejh-refund-cost.js'
import { financeOverview } from './ejh-finance.js'
import { reconciliationStatus } from './ejh-reconciliation-view.js'
import { summarizeCardBalances } from './ejh.js'
import { summarizeCardBalancesUSD } from './ejh-card-funding.js'
import { summarizeInFlightPayments } from './ejh-account-amounts.js'

// The same full-history formulas as the existing overview, evaluated once on
// plain data off the UI thread. No balances or accounting states are rewritten.
export function calculateEJHOverview(source, now = Date.now()) {
 const data = applyRefundFeeOverrides(applyPaymentPolicies(source))
 const finance = financeOverview(data)
 return {
  finance,
  reconciliation: reconciliationStatus(data, now, 600, finance),
  balances: summarizeCardBalances(data.cards),
  usd: summarizeCardBalancesUSD(data.cards, data.operations),
  inFlight: summarizeInFlightPayments(data.cases, data.cards, data.operations, data.trades),
  paymentOnly: paymentOnlySummary(data, 'ejh'),
 }
}
