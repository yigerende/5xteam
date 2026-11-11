const number = value => typeof value === 'number' && Number.isFinite(value)
const value = (n, suffix = '') => number(n) ? `${Number(n.toFixed(2))}${suffix}` : '未记录'
const minutes = n => value(n, ' 分钟')
const capNames = {batch:'每批上限',inflight:'在途余量',pool:'账号池余量',hour:'每小时数量余量',day:'每日数量余量',hour_budget:'每小时预算可补',day_budget:'每日预算可补'}

// Only use the decision's saved inputs. Live configuration and current task
// statuses must never be used to reconstruct a historical trigger.
export function decisionReport(d = {}) {
 const c=d.config||{}, s=d.snapshot||{}, tr=d.trace?.version===1?d.trace:null, limits=d.limits||{}
 const evaluation=!!(d.trace||s.id)
 const known=d.demand_known===true
 const age=(Date.parse(d.at)-Date.parse(s.at))/1000
 const snapshotStatus=!s.valid?'采集失败':Number.isFinite(age)&&(age>c.fresh_seconds||age < -5)?'快照过期或时间异常':'采集成功'
 const utilization=number(d.capacity)&&d.capacity>0&&number(d.used)?d.used*100/d.capacity:null
 const scaled=number(c.capacity_percent)&&number(d.reported_capacity)
 const trigger=scaled?Math.floor(d.reported_capacity*c.capacity_percent*c.trigger_percent/10000):null
 const pressure=d.waiting>0||(utilization!==null&&(scaled?d.used>0&&d.used>=trigger:utilization>=c.trigger_percent))
 const timeTriggered=tr?tr.time_triggered:known&&c.time_enabled&&number(d.supply_minutes)&&d.supply_minutes<=d.trigger_minutes&&d.lead_minutes<c.forecast_minutes&&d.target_minutes<=c.forecast_minutes
 const concurrencyTriggered=tr?tr.concurrency_triggered:known&&c.concurrency_enabled&&pressure&&d.confirmations>=c.confirmations
 function rule(key,name,enabled,need,triggered,evidence,extra='') {
  const status=!enabled?'已关闭':!known?'未评估':!number(need)?'未记录':key==='time'&&!number(d.supply_minutes)?(d.money?.state==='idle'?'近期无消耗':'暂不可估'):need>0?'产生补货缺口':triggered?(key==='time'&&tr&&!tr.money_sizing?'条件达到 · 计算依据不足':'条件达到 · 无新增净缺口'):'未产生缺口'
  return {key,name,need:value(need,' 个'),active:known&&need>0,status,evidence,extra}
 }
 const newCapacity=tr?.effective_new_account_capacity??tr?.new_account_capacity
 const capacityBasis=number(c.capacity_percent)&&number(d.reported_capacity)?`原始容量 ${value(d.reported_capacity)} × ${c.capacity_percent}% = ${value(d.capacity)}。`:''
 const sizing=tr?.money_sizing
 const rules=[
  rule('time','预计供给不足',c.time_enabled,d.time_need,timeTriggered,
   `预计支撑 ${number(d.supply_minutes)?minutes(d.supply_minutes):'暂不可估'}；实际触发水位 ≤ ${minutes(d.trigger_minutes)}；补后目标 ${minutes(d.target_minutes)}。`,
   `配置水位 ${minutes(c.trigger_minutes)}，预计就绪 ${minutes(d.lead_minutes)}，${c.early?'已开启按就绪耗时提前补货':'未开启按就绪耗时提前补货'}。`+(sizing?` 目标消耗 ${value(d.money?.rate_usd)} USD/分钟 × ${value(d.target_minutes)} 分钟 − 剩余 ${value(d.money?.remaining_usd)} USD − ${sizing.pending} 个在途 × 每号 ${value(sizing.new_budget_usd)} USD = ${value(sizing.gap_usd)} USD 缺口；按每号额度向上取整，并受最大在途 ${value(c.max_inflight)} 个限制。`:'')),
  rule('concurrency','持续并发压力',c.concurrency_enabled,d.concurrency_need,concurrencyTriggered,
   capacityBasis+`占用 ${value(d.used)} / 容量 ${value(d.capacity)}（${utilization===null?'无有效容量':value(utilization,'%')}），触发 ≥ ${scaled?value(trigger)+' 并发（'+value(c.trigger_percent,'%')+' 向下取整）':value(c.trigger_percent,'%')}；账号侧等待 ${value(d.waiting)}，有等待也参与触发；连续确认 ${value(d.confirmations)} / ${value(c.confirmations)} 次。`,
   `补后目标利用率 ${value(c.target_percent,'%')}。`+(tr&&known?` 需求槽位按（占用 ${value(d.used)} + 等待 ${value(d.waiting)}）÷ ${value(c.target_percent,'%')} 计算，扣除预计到货时有效容量 ${tr.future_capacity} 及 ${tr.timely_inflight} 个及时在途 × 每号 ${value(newCapacity)} 有效槽位，再按每号容量向上取整。`:'')),
  rule('exit','账号退出前补货',c.exit_enabled,d.exit_need,d.exit?.pressure,
   `手动提前 ${minutes(c.exit_minutes)}；临期 ${value(d.exit?.at_risk)} 个，当前容量 ${value(d.exit?.current_capacity)}，退出后容量 ${value(d.exit?.after_capacity)}，采用需求 ${value(d.exit?.demand)}。`,
   `沿用并发触发 ${value(c.trigger_percent,'%')}、目标 ${value(c.target_percent,'%')}；退出压力确认 ${value(d.exit?.confirmations)} / ${value(c.confirmations)} 次${d.exit?.emergency?'，预测存在容量不足，确认后采用紧急冷却':''}。`+(d.exit?.demand_held?' 当前占用受容量下降限制，暂保留退出前的近期需求。':'')+(d.exit?.transient_gap?' 存在短时衔接缺口，按新增任务到位时的容量计算，避免重复开通。':'')),
  rule('reserve','最低备用',c.reserve>0,d.reserve_need,d.reserve_need>0,
   `目标 ${value(c.reserve)} 个 − 当前可用 ${value(d.usable)} 个${tr?` − 预计及时就绪在途 ${tr.timely_inflight} 个`:''}。`),
  rule('empty','空分组启动',c.empty_group_count>0,d.empty_group_need,known&&!s.monitor_oauth_error&&(number(s.monitor_oauth_count)?s.monitor_oauth_count===0:Array.isArray(s.accounts)&&s.accounts.length===0),
   `${number(s.monitor_oauth_count)?'监控组 OAuth '+s.monitor_oauth_count+' 个账号':s.monitor_oauth_error||'历史快照未记录 OAuth 数量，原账号总数 '+(Array.isArray(s.accounts)?s.accounts.length:'未知')}；空组启动设置 ${value(c.empty_group_count)} 个${tr?`，扣除同组全部在途 ${tr.group_inflight} 个（包含结果未知）`:''}。`),
  rule('force','强制合并前接替',c.force_merge_enabled,d.force_merge_need,d.force_merge?.triggered,
   `参与账号 ${value(d.force_merge?.participants)} 个；平均剩余 ${number(d.force_merge?.average_minutes)?minutes(d.force_merge.average_minutes):'暂不可估'}，触发条件 < ${minutes(c.force_merge_minutes)}。`,
   `已就绪接替 ${value(d.force_merge?.ready)} 个，在途接替 ${value(d.force_merge?.pending)} 个；剩余需接替 ${value(d.force_merge_need)} 个。`),
 ]
 const active=rules.filter(r=>r.active).map(r=>r.name)
 const net=tr?tr.net_need:Math.max(0,...[d.time_need,d.concurrency_need,d.reserve_need,d.empty_group_need,d.force_merge_need,d.exit_need].filter(number))
 const outcome=d.started>0?`已预留 ${d.started} 个全自动 Pro 任务`:d.suggested>0?`本轮建议 ${d.suggested} 个，未新增预留`:'本轮未新增补货'
 const steps=[known?`各规则净缺口取最大值：${value(net)} 个，不累加。`:'监控数据不足，本轮未完成净缺口计算。']
 if(tr?.after_minimum!=null)steps.push(tr.strict_minimum?`按最小批量 ${value(c.min_batch)} 个处理后：${tr.after_minimum} 个。`:d.empty_group_need>0&&net===d.empty_group_need&&!d.time_need&&!d.concurrency_need&&!d.reserve_need&&!d.force_merge_need&&!d.exit_need?'仅空组启动，按启动数量执行，不抬高到最小批量。':'允许少补，按实际净缺口执行。')
 if(tr?.hard_limit!=null)steps.push(`本轮硬上限 ${Math.max(0,tr.hard_limit)} 个；最终建议 ${value(d.suggested)} 个${d.no_restock_active?'（每日禁补已覆盖建议）':''}。`)
 steps.push(`实际新增预留 ${value(d.started)} 个。预留后仍需经过登录、开通、推送和入组核验，未就绪前不计为可用账号。`)
 if(d.started>0&&d.started<d.suggested)steps.push('实际预留少于建议数量；执行时还会复核可领取账号和各项上限。具体限制以本条执行结果为准。')
 if(d.batch_wait?.message)steps.push(d.batch_wait.message)
 const protections=[
  {name:'运行方式',text:`${c.enabled?'总开关开启':'总开关关闭'} · ${c.mode==='auto'?'自动执行':c.mode==='observe'?'观察模式':'模式未记录'}${d.no_restock_active?' · 每日禁补生效':''}`},
  {name:'采集情况',text:`${snapshotStatus} · 并发采集${s.load_valid?'完整':'不可用'}${s.error?' · '+s.error:''}`},
  {name:'失败熔断',text:`连续失败 ${value(limits.failures)} / ${value(c.failure_limit)} 次`},
  {name:'数量与在途',text:`每批 ${value(c.min_batch)}～${value(c.max_batch)} 个 · 当时在途 ${value(d.inflight)} / ${value(c.max_inflight)} 个 · 小批量${c.allow_small?'允许少补':'严格最少数量'}`},
  {name:'累计数量',text:`小时 ${value(limits.hour_count)} / ${value(c.hour_limit)} 个 · 当天 ${value(limits.day_count)} / ${value(c.day_limit)} 个`},
  {name:'成本预留',text:`小时 ${value(limits.hour_cost)} / ${c.hour_budget>0?value(c.hour_budget):'不限'} · 当天 ${value(limits.day_cost)} / ${c.day_budget>0?value(c.day_budget):'不限'}（配置的记账单位）`},
 ]
 if(tr?.cooldown_seconds!=null)protections.push({name:'本轮冷却',text:`采用 ${tr.cooldown_seconds} 秒；${tr.cooldown_remaining_seconds>0?`当时还需 ${tr.cooldown_remaining_seconds} 秒`:'已满足'}`})
 return {evaluation,legacy:evaluation&&!tr,outcome,trigger:active.length?`产生净缺口的规则：${active.join('、')}`:known?'本轮没有已确认的新增净缺口':'本轮未完成需求判断',rules,steps,protections,caps:(tr?.caps||[]).map(cap=>({name:capNames[cap.key]||cap.key,remaining:Math.max(0,cap.remaining),binding:cap.remaining===tr.hard_limit})),evaluationReason:tr?.evaluation_reason||''}
}
