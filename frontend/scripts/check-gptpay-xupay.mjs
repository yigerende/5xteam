import assert from 'node:assert/strict'
import { catalogProPlans, paymentCountries, paymentRetryBlocked, supplierCredits, payProviderName, xupayStates, needsPayPoll, canCheckXUPay, payPollDelay, supplierAmountTitle } from '../src/gptpay.js'
assert.equal(payProviderName('xupay'), '序付 · XUPAY')
assert.deepEqual(catalogProPlans(null, 'xupay').map(p => p.code), ['pro5', 'pro20'])
assert(!paymentCountries('xupay').some(c => c.code === 'CL'))
assert(paymentCountries('xupay').some(c => c.code === 'SG'))
assert.equal(supplierCredits(12345, 'xupay'), '123.45 R')
const catalog = { products: [{ code: 'pro20', creditPrice: 123, enabled: true }], countries: [{ code: 'PH' }] }
assert.equal(catalogProPlans(catalog, 'xupay')[1].label, 'Pro 20x · 1.23 R')
assert.equal(catalogProPlans(catalog, 'xupay')[0].available, false)
assert(paymentRetryBlocked({ provider: 'xupay', status: 'success', remote: { paymentState: 'succeeded' } }))
assert(paymentRetryBlocked({ provider: 'xupay', status: 'manual_review', remote: {} }))
assert(!paymentRetryBlocked({ provider: 'xupay', status: 'failed', remote: { paymentState: 'failed' } }))
assert.match(xupayStates({ paymentState: 'succeeded', activationStatus: 'review' }), /已付款.*待核实/)
console.log('XUPAY UI contract passed: plans, countries, R units, paid fence and activation state')

const order = { provider: 'xupay', status: 'manual_review', remote: { id: 'original', paymentState: 'review', automaticRunning: true, canCheck: true } }
assert(needsPayPoll(order))
assert(!canCheckXUPay(order))
order.remote.automaticRunning = false
assert(!needsPayPoll(order))
assert(canCheckXUPay(order))
order.remote.canCheck = false
assert(!canCheckXUPay(order))
delete order.remote.canCheck
assert(canCheckXUPay(order))
order.check_pending = true
assert(needsPayPoll(order))
assert(!canCheckXUPay(order))
const now = Date.now(), next = new Date(now + 120000).toISOString()
const limited = { provider: 'xupay', status: 'submission_unknown', rate_limited: true, rate_limit_retries: 2, next_status_check_at: next }
assert(needsPayPoll(limited))
assert.equal(payPollDelay(limited, now), 120000)
assert(!needsPayPoll({ ...limited, rate_limit_retries: 3 }))
assert(!needsPayPoll({ ...limited, next_status_check_at: null }))
assert.equal(supplierAmountTitle({ paymentAmountSource: 'quoted' }, 'xupay'), '付款报价')
assert.equal(supplierAmountTitle({ paymentAmountSource: 'charged' }, 'xupay'), '已确认实收')
assert.equal(supplierAmountTitle({}, 'cmsnav'), '供应商应付')
assert(!needsPayPoll({ provider: 'xupay', status: 'success', remote: { id: 'original', paymentState: 'succeeded', cancellationStatus: 'failed' } }))

// Render the actual shared diagnostics template. Supplier text must remain text,
// and an exhausted retry must not advertise another scheduled attempt.
const { readFileSync } = await import('node:fs')
const vm = await import('node:vm')
const Vue = await import('vue')
const { renderToString } = await import('@vue/server-renderer')
const { compileScript, parse } = await import('@vue/compiler-sfc')
const { transformWithEsbuild } = await import('vite')
const utils = await import('../src/utils.js')
const source = readFileSync(new URL('../src/components/XUPayOrderDetails.vue', import.meta.url), 'utf8')
const { descriptor } = parse(source)
const compiled = compileScript(descriptor, { id: 'xupay-detail-check', inlineTemplate: true })
const { code } = await transformWithEsbuild(compiled.content, 'detail.js', { format: 'cjs', loader: 'js' })
const module = { exports: {} }
vm.runInNewContext(code, { module, exports: module.exports, require: id => id === 'vue' ? Vue : utils })
const html = await renderToString(Vue.createSSRApp(module.exports.default, { order: {
  ...limited, rate_limit_retries: 3, next_status_check_at: null,
  remote: { errorCode: 'amount_changed', message: '报价已变化', events: [{ stage: 'payment', message: '<script>alert(1)</script>' }] },
} }))
assert.match(html, /3\/3/)
assert.match(html, /自动重试已结束/)
assert.match(html, /amount_changed/)
assert.match(html, /报价已变化/)
assert.match(html, /&lt;script&gt;/)
assert(!html.includes('<script>'))
assert(!html.includes('下次重试'))
console.log('XUPAY follow-up passed: automatic states, check permission, retry schedule, amount labels and safe diagnostics rendering')
