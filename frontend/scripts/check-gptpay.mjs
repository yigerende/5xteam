import assert from 'node:assert/strict'
import { parseCardText, needsPayPoll, proPlanName } from '../src/gptpay.js'
for (const date of ['202812','2028-12','2028/12','12/28','12/2028','2812']) {
  assert.deepEqual(parseCardText(`4242 4242 4242 4242----${date}----012`), { number: '4242424242424242', cvv: '012', exp_year: 2028, exp_month: 12 })
}
assert.throws(() => parseCardText('4242424242424242----202813----123'))
assert.throws(() => parseCardText('4242424242424242----oops----123'))
assert.throws(() => parseCardText('bad'))
assert.equal(proPlanName('pro5'), 'Pro 5x')
assert.equal(proPlanName('pro20'), 'Pro 20x')
assert.equal(needsPayPoll({status:'success', remote:{id:'test', cancellationStatus:'pending'}}), true)
assert.equal(needsPayPoll({status:'success', remote:{id:'test', cancellationStatus:'success'}}), false)
assert.equal(needsPayPoll({status:'success', remote:{id:'test', cancellationStatus:'failed'}}), false)
assert.equal(needsPayPoll({status:'processing', remote:{id:'test'}}), true)
assert.equal(needsPayPoll({status:'submission_unknown', remote:{}}), false)
console.log('GPTPay card parsing, plan labels and polling conditions passed')
