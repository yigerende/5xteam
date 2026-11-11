import assert from 'node:assert/strict'
import { restockTaskRows, restockTaskProgress, restockTaskTimeline, restockTaskActive } from '../src/restockTasks.js'
const old={id:'old',email:'old@example.test',status:'ready',stage:'quota',created_at:'2026-10-19T10:00:00+08:00',submitted_at:'2026-10-19T10:01:00+08:00',ready_at:'2026-10-19T10:08:00+08:00',eta:'2026-10-19T10:09:00+08:00',stages:{'login:running':'2026-10-19T10:02:00+08:00','push:completed':'2026-10-19T10:07:00+08:00','login:completed':'2026-10-19T10:03:00+08:00'}}
const latest={...old,id:'latest',created_at:'2026-10-20T10:00:00+08:00'}
const pending={id:'pending',email:'pending@example.test',status:'reserved',created_at:'2026-10-20T09:00:00+08:00'}
const failure={id:'failed',order_id:'pro-auto-abcd',status:'failed',created_at:'2026-10-20T11:00:00+08:00',updated_at:'2026-10-20T11:05:00+08:00'}
const input=[old,latest,pending,failure],original=structuredClone(input)
assert.deepEqual(restockTaskRows(input).map(t=>t.id),['pending','failed','latest','old'])
assert.deepEqual(restockTaskRows(input,'ready').map(t=>t.id),['latest','old'])
assert.deepEqual(restockTaskRows(input,'active').map(t=>t.id),['pending'])
assert.deepEqual(restockTaskRows(input,'ended','ABCD').map(t=>t.id),['failed'])
assert.equal(restockTaskProgress(old).started,'2026-10-19T10:02:00+08:00')
assert.equal(restockTaskProgress(old).success,old.ready_at)
assert.equal(restockTaskProgress(old).eta,null)
assert.equal(restockTaskProgress(old).stage,'推送后核验完成','historical quota stage must not appear as current work')
assert.equal(restockTaskProgress(pending).started,null,'reservation time is not execution time')
assert.equal(restockTaskProgress(failure).success,null,'updated time is not success time')
const cardWait={...pending,status:'waiting_card',stage:'card',card_id:'original-card',message:'原卡资料完成后自动续跑'}
assert.equal(restockTaskProgress(cardWait).status,'等待原卡 · 自动续跑')
assert.equal(restockTaskProgress(cardWait).stage,'卡片准备 / 同步')
assert.equal(restockTaskProgress(cardWait).success,null)
assert.equal(restockTaskRows([cardWait],'active').length,1,'card import still occupies the original batch')
const legacy={...old,ready_at:null,stages:{'push:completed':old.stages['push:completed']},stage:'merge'}
assert.equal(restockTaskProgress(legacy).status,'已交付')
assert.equal(restockTaskProgress(legacy).success,null,'do not invent readiness from creation, updates, or push time')
assert.equal(restockTaskProgress(legacy).started,null,'submission alone does not prove execution')
assert.equal(restockTaskProgress({status:'verifying',stages:{'push:completed':old.ready_at}}).success,null)
assert.deepEqual(restockTaskTimeline(old).map(t=>t.label),['登录 · 开始执行','登录 · 完成','推送 Sub2 · 完成'])
assert.equal(restockTaskTimeline({stages:{login:'0001-01-01T00:00:00Z',push:'invalid'}}).length,0)
assert.deepEqual(input,original,'presenting history must not mutate task state or scheduling inputs')
console.log('Passed: restock task sorting/filtering, execution and verified-success times, historical stages and missing milestones')

assert.equal(restockTaskProgress({status:'cancelled',stopped_at:'2026-10-21T06:00:00Z',submitted_at:'2026-10-21T05:00:00Z'}).status,'已手动停止')

assert.equal(restockTaskProgress({status:'attention',stage:'queued'}).stage,'准备异常 · 待核验')
assert.equal(restockTaskProgress({status:'submitting',stage:'queued'}).stage,'准备卡片')
assert.equal(restockTaskProgress({status:'failed',stage:'queued'}).stage,'准备失败')
assert.equal(restockTaskProgress({status:'cancelled',stage:'queued',stopped_at:'2026-10-28T09:00:00Z'}).stage,'已停止')
assert.equal(restockTaskProgress({status:'reserved',stage:'queued'}).stage,'排队')

const replaced={...failure,status:'unknown',card_id:'held-card',fulfilled_by_task_id:'replacement',fulfilled_by_email:'replacement@example.test',fulfilled_at:old.ready_at}
const replacementProgress=restockTaskProgress(replaced)
assert.equal(restockTaskActive(replaced),false)
assert.equal(replacementProgress.status,'换号补货成功')
assert.equal(replacementProgress.success,old.ready_at)
assert.equal(replacementProgress.eta,null)
assert.match(replacementProgress.message,/replacement@example.test/)
assert.match(replacementProgress.message,/原订单核实记录及卡片占用保留/)
assert.equal(restockTaskRows([replaced],'active').length,0)
assert.equal(restockTaskRows([replaced],'ready','replacement@example.test').length,1)
assert.equal(restockTaskRows([replaced],'ended').length,0)
for(const field of ['fulfilled_by_task_id','fulfilled_by_email','fulfilled_at']) {
 assert.equal(restockTaskActive({...replaced,[field]:null}),true,'partial evidence must retain inflight')
}
assert.equal(restockTaskActive({...replaced,fulfilled_at:'0001-01-01T00:00:00Z'}),true)
assert.equal(replaced.status,'unknown','display must not rewrite original financial status')
