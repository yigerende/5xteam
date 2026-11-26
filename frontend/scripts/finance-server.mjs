// The server uses the exact same financial formulas as card/account details.
// Source evidence stays on the server; only the requested period is returned.
import { financeReport, financeOverview, cents, money } from '../src/ejh-finance.js'
let input = ''
for await (const chunk of process.stdin) {
 input += chunk
 if (input.length > 128 * 1024 * 1024) throw new Error('Financial input is too large')
}
const {data, history, mode, date, purpose} = JSON.parse(input)
if(purpose==='reconciliation') {
 const v=financeOverview(data)
 const snapshot={formula:'ejh-reconciliation-v1',net:v.net,assets:v.assets,cost:v.earnings.costUSD,refund_fees:v.refundFees.amount,merchant_fee:v.merchantFee,external_transfer:v.externalTransfer,payment_only:money(BigInt(v.paymentOnly.amount)),account:v.accountUSD,card_balance:v.cardUSD.amount,in_flight:v.inFlight.paymentUSD.amount,refund_settlement:v.refundSettlement.amount,usage:v.usage.usd.amount,balance_at:data.balance?.at,sync_at:data.sync?.last_success,balances:(data.cards||[]).filter(c=>c.remote?.status!=='2').map(c=>({id:c.id,at:c.balance_at,error:c.balance_error||''})),issues:v.lossReasons,estimated:v.estimated||v.earnings.estimated}
 process.stdout.write(JSON.stringify({raw_gap:v.rawGap,total_before:v.adjustment,gap_before:v.gap,suggested_amount:v.gap===null?null:money(-cents(v.gap)),snapshot}))
} else {
const report = financeReport(data, history, mode, date)
// Detailed source rows inside the cumulative overview are not page inputs.
const overview=report.overview
report.overview = Object.fromEntries(['consumption','refundSettlement','rawGap','adjustment','gap','netR','net','estimated','lossR','loss','lossReasons'].map(key=>[key,overview[key]]))
process.stdout.write(JSON.stringify(report))

}
