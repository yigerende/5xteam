import assert from 'node:assert/strict'
import { financeOverview, financeReport } from '../src/ejh-finance.js'
import { applyEJHOverviewResponse } from '../src/ejh-overview-state.js'
const data={recharge_ledger:{amount:'100.00'},balance:{data:{abroadList:[{currency:'USD',balance:'231.07'}]}}}
const before=financeOverview(data)
assert.equal(before.gap,'-132.07')
const adjusted={...data,reconciliation_adjustments:{amount:'132.07',count:1}}
const after=financeOverview(adjusted)
assert.equal(after.gap,'0.00');assert.equal(after.rawGap,'-132.07')
for(const key of Object.keys(before)){if(['gap','unexplained','adjustment'].includes(key))continue;assert.deepEqual(after[key],before[key],key+' must not change')}
assert.equal(financeOverview({...data,reconciliation_adjustments:{amount:'-0.01'}}).gap,'-132.08')
assert.equal(financeOverview({reconciliation_adjustments:{amount:'132.07'}}).gap,null)
assert.equal(financeOverview({...data,reconciliation_adjustments:{amount:'invalid'}}).gap,null)
const report=financeReport(adjusted,{},'all','2026-11-26'),original=financeReport(data,{},'all','2026-11-26')
assert.deepEqual(report.earnings,original.earnings);assert.deepEqual(report.events,original.events)
const patch=applyEJHOverviewResponse({...data,revision:'1'},{mode:'delta',base_revision:'1',revision:'2',changes:{reconciliation_adjustments:{value:{amount:'132.07',count:1}}}})
assert.equal(financeOverview(patch).gap,'0.00')
console.log('Manual adjustment: signed cents, original evidence, profit/cost isolation and delta passed')
