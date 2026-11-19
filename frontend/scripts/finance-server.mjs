// The server uses the exact same financial formulas as card/account details.
// Source evidence stays on the server; only the requested period is returned.
import { financeReport } from '../src/ejh-finance.js'
let input = ''
for await (const chunk of process.stdin) {
 input += chunk
 if (input.length > 128 * 1024 * 1024) throw new Error('Financial input is too large')
}
const {data, history, mode, date} = JSON.parse(input)
const report = financeReport(data, history, mode, date)
// Detailed source rows inside the cumulative overview are not page inputs.
const overview=report.overview
report.overview = Object.fromEntries(['consumption','refundSettlement','gap','netR','net','estimated','lossR','loss','lossReasons'].map(key=>[key,overview[key]]))
process.stdout.write(JSON.stringify(report))
