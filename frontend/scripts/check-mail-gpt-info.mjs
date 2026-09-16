import assert from 'node:assert/strict'
import { gptPlanLabel, gptCreatedTime, gptInfoTitle, gptInfoStatus } from '../src/mailGPTInfo.js'
assert.equal(gptCreatedTime('2023-11-14T22:13:20Z'), '2023-11-15 06:13:20')
assert.equal(gptCreatedTime('2026-01-01T16:00:00Z'), '2026-01-02 00:00:00')
for (const value of [null, '', 'invalid']) assert.equal(gptCreatedTime(value), '未获取')
assert.equal(gptPlanLabel('self_serve_business_prolite'), 'Team 5x')
assert.equal(gptPlanLabel('PRO'), 'Pro')
assert.equal(gptPlanLabel(''), '未获取')
assert.match(gptInfoTitle({ gpt_info_check: { plan_source: 'jwt', error: 'HTTP 401' } }), /非实时/)
assert.match(gptInfoTitle({ gpt_info_check: { error: 'HTTP 401' } }), /HTTP 401/)
assert.equal(gptInfoStatus('partial'), '部分更新')
console.log('Passed: Beijing date and midnight, invalid dates, plans, cached source, errors and status labels')
