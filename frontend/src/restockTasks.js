const terminal = new Set(['ready', 'failed', 'cancelled'])
export const restockReplacementDelivered = task => !!task.fulfilled_by_task_id && !!task.fulfilled_by_email && !!taskTime(task.fulfilled_at)
export const restockTaskActive = task => !restockReplacementDelivered(task) && !terminal.has(task.status)
export const taskTime = value => value && !String(value).startsWith('0001') && Number.isFinite(Date.parse(value)) ? value : null
const epoch = value => taskTime(value) ? Date.parse(value) : 0
const stages = {queued:'排队',card:'卡片准备 / 同步',login:'登录',recharge:'开通 / 付款核验',oauth:'获取新授权',push:'推送 Sub2',quota:'额度巡检',merge:'合并 / 移出',manual:'手动流程'}
const statuses = {reserved:'已预留 / 排队',submitting:'启动确认中',waiting_card:'等待原卡 · 自动续跑',running:'全自动执行中',unknown:'原订单待确认',verifying:'已推送待核验',failed:'确认失败',attention:'需要核验',cancelled:'未提交已取消',external:'其他来源在途'}
const milestones = {running:'开始执行',completed:'完成',failed:'失败',interrupted:'中断',waiting:'等待',queued:'排队'}

export function restockTaskProgress(task) {
 const active=restockTaskActive(task),replaced=restockReplacementDelivered(task),times=task.stages||{}
 const started=Object.entries(times).filter(([key,at])=>/^(login|recharge|oauth|push):running$/.test(key)&&taskTime(at)).map(([,at])=>at).sort((a,b)=>epoch(a)-epoch(b))[0]||null
 const submitted=taskTime(task.submitted_at),success=replaced?taskTime(task.fulfilled_at):task.status==='ready'?taskTime(task.ready_at):null
 const pushed=taskTime(times['push:completed'])
 const waitingPreparation=task.status==='reserved'&&task.stage==='card'&&taskTime(task.next_card_check_at)
 const status=replaced?'换号补货成功':task.stopped_at?'已手动停止':task.status==='ready'?(success?'补货成功':'已交付'):waitingPreparation?'等待卡片准备':statuses[task.status]||task.status
 const stage=replaced?'替换账号已推送并核验':task.stage==='queued'&&task.status!=='reserved'?(task.stopped_at?'已停止':task.status==='failed'?'准备失败':task.status==='submitting'?'准备卡片':'准备异常 · 待核验'):task.status==='ready'?(success?'推送后核验完成':'已进入后续流程'):stages[task.stage]||task.stage||'尚未执行'
 const message=replaced?`由 ${task.fulfilled_by_email} 完成补货，不再占用在途；原订单核实记录及卡片占用保留，见开通订单。`:task.status==='ready'?(success?'已完成推送并核验；此处记录补货结果，当前可用状态见监控。':task.message||'已交付，不再占用补货在途；历史核验时间未记录。'):task.message||'持续跟踪'
 return {active,started,submitted,success,pushed,status,stage,message,eta:active?taskTime(task.eta):null,
  startNote:started?'':submitted?'已提交，执行时间未记录':task.status==='reserved'||task.status==='cancelled'?'尚未执行':'未记录',
  successNote:success?'':task.status==='ready'?'历史核验时间未记录':active?'尚未成功':'未成功',
  sortAt:epoch(task.created_at)||epoch(started)||epoch(submitted)||epoch(task.updated_at),
 }
}

export function restockTaskRows(tasks, filter='all', query='') {
 const q=query.trim().toLowerCase()
 return tasks.filter(t=>(filter==='all'||filter==='active'&&restockTaskActive(t)||filter==='ready'&&(t.status==='ready'||restockReplacementDelivered(t))||filter==='ended'&&!restockReplacementDelivered(t)&&['failed','cancelled'].includes(t.status))&&
  (!q||[t.email,t.order_id,t.id,t.fulfilled_by_email].some(v=>String(v||'').toLowerCase().includes(q))))
  .map(task=>({...task,progress:restockTaskProgress(task)}))
  .sort((a,b)=>Number(b.progress.active)-Number(a.progress.active)||b.progress.sortAt-a.progress.sortAt||String(a.id).localeCompare(String(b.id)))
}

export function restockTaskTimeline(task) {
 return Object.entries(task.stages||{}).filter(([,at])=>taskTime(at)).map(([key,at])=>{
  const [stage,state]=key.split(':')
  return {key,at,label:(stages[stage]||stage)+(state?' · '+(milestones[state]||state):' · 首次观察')}
 }).sort((a,b)=>epoch(a.at)-epoch(b.at)||a.key.localeCompare(b.key))
}
