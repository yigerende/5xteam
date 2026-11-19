import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { computed, ref } from 'vue'
import { financeOverview, financeReport, cents, money } from '../src/ejh-finance.js'

const data = {
  recharge_ledger: { amount: '1000.00' },
  cards: [{ id: 'c', issuer_linked: true, balance: '0.00', balance_at: '2026-10-22T12:00:00+08:00', remote: { orderNo: 'o', cardCurrency: 'USD', status: '1', startTime: '2026-10-22T10:00:00+08:00' } }],
  trades: [{ id: 't', card_id: 'c', trade: { tradeNo: 't', tradeType: '4', tradeStatus: '2', cardAmount: '200.00', cardCurrency: 'USD', billAmount: '200.00', billCurrency: 'USD', createdAt: '2026-10-22T11:00:00+08:00' } }],
  balance: { data: { abroadList: [{ currency: 'USD', balance: '789.50' }] } },
  cases: [{ id: 'a', email: 'test@example.com', state: 'returned', consumption_total: 1000 }],
}
const original = structuredClone(data)
const base = financeOverview(data)
assert.equal(base.gap, '0.00')
const adjusted = { ...data, recharge_ledger: { ...data.recharge_ledger, external_transfer_amount: '100.00', external_transfer_note: '外部消费' } }
const result = financeOverview(adjusted)
assert.equal(result.rawClearingUSD, '200.00')
assert.equal(result.platformClearingUSD, '100.00')
assert.equal(result.externalTransfer, '100.00')
assert.equal(result.earnings.costUSD, '100.50')
assert.equal(result.earnings.clearingR, '670.0000')
assert.equal(Number(result.earnings.profitR) - Number(base.earnings.profitR), 670)
assert.equal(result.depositFee, base.depositFee)
assert.equal(result.gross, base.gross)
assert.equal(result.loss, money(cents(base.loss) - 10000n))
assert.equal(result.gap, base.gap, 'transfer must not create a second cash gap after reducing platform costs')
assert.equal(result.gap, money(cents(result.net) - cents(result.assets) - cents(result.earnings.costUSD) - cents(result.externalTransfer)))
assert.deepEqual(data, original, 'issuer trades and balances must remain unchanged')
assert.deepEqual(financeOverview({ ...data, recharge_ledger: { ...data.recharge_ledger, external_transfer_amount: '0.00' } }), base)
const unknown = financeOverview({ ...adjusted, trades: [{ ...data.trades[0], trade: { ...data.trades[0].trade, billAmount: 'bad', cardAmount: 'bad' } }] })
assert.equal(unknown.earnings.costUSD, null, 'an exclusion cannot make missing issuer costs known')
assert.equal(unknown.gap, null)
assert.equal(financeOverview({ ...adjusted, recharge_ledger: { ...adjusted.recharge_ledger, external_transfer_amount: 'bad' } }).gap, null)
assert.equal(financeOverview({ ...adjusted, recharge_ledger: { ...adjusted.recharge_ledger, external_transfer_amount: '300.00' } }).platformClearingUSD, '-100.00', 'never silently clamp a user entry')

const baseline = { id: 'transfer1', before: '0.00', after: '120.00', delta: '120.00', baseline: true, at: '2026-10-21T10:00:00+08:00', note: '历史外部消费' }
const correction = { id: 'transfer2', before: '120.00', after: '100.00', delta: '-20.00', baseline: false, at: '2026-10-22T10:00:00+08:00', note: '修正累计值' }
const history = { external_transfers: [baseline, correction, correction] }
for (const h of [history, {}]) {
  const report = financeReport(adjusted, h, 'all', '2026-10-22')
  assert.equal(report.summary.clearingUSD, '100.00')
  assert.equal(report.summary.externalTransferUSD, '100.00')
  assert.deepEqual(report.summary.earnings, result.earnings)
  assert.equal(report.rows.reduce((sum, row) => sum + cents(row.clearingUSD), 0n), 10000n)
  assert.equal(report.events.filter(e => e.externalTransfer).reduce((sum, e) => sum + cents(e.usd), 0n), -10000n)
}
for (const mode of ['day', 'week', 'month']) {
  const report = financeReport(adjusted, history, mode, '2026-10-22')
  assert.equal(report.summary.clearingUSD, '220.00', 'later correction restores 20 USD to dated costs; unknown baseline is not assigned to today')
  assert.equal(report.summary.externalTransferUSD, '-20.00')
  assert.equal(report.undatedTransfers, 1)
  assert.equal(report.summary.earnings.profitR, null, 'unknown historical exclusion dates must not imply complete period profit')
}
const cleared = financeReport(data, { external_transfers: [baseline, correction, { id: 'clear', delta: '-100.00', at: '2026-10-23T10:00:00+08:00' }] }, 'all')
assert.equal(cleared.summary.clearingUSD, '200.00')
assert.deepEqual(cleared.summary.earnings, base.earnings)

const code = readFileSync(new URL('../src/components/EJHExternalTransfer.vue', import.meta.url), 'utf8').split('<script setup>')[1].split('</script>')[0].replace(/^import .*$/gm, '')
const props = { ledger: { amount: '1000.00', external_transfer_amount: '100.00', external_transfer_note: '外部消费' }, disabled: false }
const calls = [], emits = []
let fail = false, release
const context = vm.createContext({ computed, ref, nextTick: async () => {}, defineProps: () => props, defineEmits: () => (name, value) => { emits.push({ name, value }); props.ledger = value }, async api(url, options) {
  calls.push({ url, options })
  if (fail) throw Error('保存失败')
  if (release) await new Promise(resolve => { release.resolve = resolve })
  return { ...props.ledger, external_transfer_amount: options.body.amount || '0.00', external_transfer_note: options.body.note }
} })
vm.runInContext(code + ';globalThis.form={edit,save,editing,saving,draft,note,error,valid}', context)
const f = context.form
await f.edit()
assert.equal(f.draft.value, '100.00')
f.draft.value = '123.45'; f.note.value = ' 修正 '
props.ledger = { ...props.ledger, external_transfer_amount: '90.00' }
assert.equal(f.draft.value, '123.45', 'background reads must not overwrite unsaved edits')
await f.save()
assert.deepEqual(JSON.parse(JSON.stringify(calls[0])), { url: '/api/ejh/external-transfer', options: { method: 'PUT', body: { amount: '123.45', note: '修正' } } })
assert.equal(emits[0].name, 'saved')
assert.equal(emits[0].value.amount, '1000.00')
await f.edit()
for (const value of ['-1', '1.001', '1e3', '1000000000000']) { f.draft.value = value; assert.equal(f.valid.value, false); await f.save() }
assert.equal(calls.length, 1)
f.draft.value = '10'; fail = true; await f.save()
assert.equal(f.editing.value, true); assert.equal(f.saving.value, false); assert.equal(f.error.value, '保存失败')
fail = false; release = {}; const pending = f.save(); await f.save()
assert.equal(calls.length, 3, 'double click cannot issue concurrent saves')
release.resolve(); await pending; release = null
await f.edit(); f.draft.value = ''; await f.save()
assert.equal(props.ledger.external_transfer_amount, '0.00')
props.disabled = true; const before = calls.length; await f.save(); assert.equal(calls.length, before)
console.log('Passed: external transfers, unchanged issuer data/recharge fees, consistent cost/profit/reconciliation, historical and dated corrections, duplicates, clear/reset, API form validation and failure handling')
