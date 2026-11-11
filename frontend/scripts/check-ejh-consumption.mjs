import assert from 'node:assert/strict'
import { accountConsumption, consumptionText, cardConsumption, accountConsumptionText, accountConsumptionForProfit, summarizeConsumption } from '../src/ejh-consumption.js'

const account = { email: 'One@example.com', consumption_total: 67.0766, cost_downstream_snapshot: 335.3832 }
assert.equal(consumptionText(accountConsumption(account)), '67.0766 R')
assert.equal(consumptionText(accountConsumption({ consumption_total: 0 })), '0.0000 R')
for (const value of [undefined, null, '', -1, NaN, Infinity]) {
  assert.equal(consumptionText(accountConsumption({ consumption_total: value })), '未查询')
}
assert.deepEqual(cardConsumption([
  account,
  { ...account, id: 'another-order', email: ' one@EXAMPLE.com ' },
  { email: 'two@example.com', consumption_total: 2.1234 },
  { email: 'zero@example.com', consumption_total: 0 },
  { email: 'unknown@example.com' },
]), { text: '69.2000 R', total:69.2, missing: 1, notPushed: 0 })
assert.deepEqual(cardConsumption([]), { text: '0.0000 R', total:0, missing: 0, notPushed: 0 })
assert.deepEqual(cardConsumption([{ email: 'unknown@example.com' }]), { text: '未查询', total:null, missing: 1, notPushed: 0 })
assert.deepEqual(cardConsumption([{ email: 'One@example.com' }, account]), { text: '67.0766 R', total:67.0766, missing: 0, notPushed: 0 })
console.log('Passed: user consumption, R display, account deduplication, card totals, known zero and missing data')

const notPushed = { email:'never@example.com', consumption_state:'not_pushed' }
assert.equal(accountConsumption(notPushed), null)
assert.equal(accountConsumptionText(notPushed), '未推送，无消费数据')
assert.equal(accountConsumptionForProfit(notPushed), 0)
assert.equal(accountConsumptionText({...notPushed,consumption_total:0}), '0.0000 R')
assert.equal(accountConsumptionText({...notPushed,consumption_total:12}), '12.0000 R')
assert.deepEqual(cardConsumption([notPushed,notPushed]),{text:'0.0000 R',total:0,missing:0,notPushed:1})
assert.deepEqual(summarizeConsumption([notPushed,account]),{total:67.0766,missing:0,count:2,notPushed:1})
for(const rows of [[notPushed,{email:notPushed.email}],[{email:notPushed.email},notPushed]]) {
 assert.deepEqual(summarizeConsumption(rows),{total:null,missing:1,count:1,notPushed:0})
}
for(const total of [0,12]) {
 const history=[{id:'history',email:' NEVER@example.com ',total}]
 assert.deepEqual(summarizeConsumption([notPushed], [...history,...history]),{total,missing:0,count:1,notPushed:0})
}
console.log('Passed: no push source distinguished from unknown and queried zero; history and applicable orders take precedence')
