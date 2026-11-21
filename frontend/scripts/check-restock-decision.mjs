import assert from 'node:assert/strict'
import {decisionReport,decisionSummary} from '../src/restockDecision.js'
import {exitEvidence,exitSizing} from '../src/restockExit.js'

const base={id:'audit-1',at:'2026-10-16T12:00:00+08:00',version:3,demand_known:true,started:2,suggested:2,reason:'本轮已预留',
 config:{enabled:true,mode:'auto',group_id:7,time_enabled:true,trigger_minutes:10,target_minutes:15,early:true,forecast_minutes:60,concurrency_enabled:true,trigger_percent:85,target_percent:75,confirmations:2,reserve:0,empty_group_count:2,force_merge_enabled:false,force_merge_minutes:15,min_batch:1,max_batch:2,allow_small:true,max_inflight:6,hour_limit:20,day_limit:100,failure_limit:3},
 snapshot:{id:'snapshot-1',at:'2026-10-16T12:00:00+08:00',group_id:7,valid:true,load_valid:true,accounts:[{id:1}]},
 usable:1,capacity:20,used:100,waiting:0,inflight:3,confirmations:2,lead_minutes:2,trigger_minutes:10,target_minutes:15,supply_minutes:5,time_need:0,concurrency_need:5,reserve_need:0,empty_group_need:0,force_merge_need:0,
 money:{rate_usd:2,remaining_usd:10},limits:{hour_count:0,day_count:0,hour_cost:0,day_cost:0,failures:0},
 trace:{version:1,time_triggered:true,concurrency_triggered:true,new_account_capacity:20,group_inflight:2,timely_inflight:1,future_capacity:20,net_need:5,after_minimum:5,hard_limit:2,caps:[{key:'batch',remaining:2},{key:'inflight',remaining:3}],cooldown_seconds:60,cooldown_remaining_seconds:0,evaluation_reason:'净缺口取最大值',money_sizing:{new_budget_usd:100,pending:1,gap_usd:-80}}}
const report=decisionReport(base)
assert.match(report.trigger,/持续并发压力/)
assert.doesNotMatch(report.trigger,/预计供给不足/)
assert.match(report.rules[0].status,/条件达到/)
assert.match(report.rules[0].extra,/− 1 个在途 × 每号 100 USD/)
assert.match(report.rules[1].extra,/有效容量 20 及 1 个及时在途/)
assert.match(report.steps[0],/最大值：5 个，不累加/)
assert.equal(report.caps.find(c=>c.name==='每批上限').binding,true)
assert.equal(report.caps.find(c=>c.name==='在途余量').binding,false)
assert.equal(report.evaluationReason,'净缺口取最大值')
// The displayed thresholds come from the saved configuration, even years later.
assert.match(decisionReport(JSON.parse(JSON.stringify(base))).rules[1].evidence,/≥ 85%/)
const legacy={...base,trace:undefined}
assert.equal(decisionReport(legacy).legacy,true)
assert.equal(decisionReport(legacy).caps.length,0)
assert.doesNotMatch(decisionReport(legacy).rules[1].extra,/20 槽位/)
assert.equal(decisionReport({id:'configuration',config:base.config,snapshot:{}}).evaluation,false)
const bad=decisionReport({...base,demand_known:false,started:0,suggested:0,trace:{version:1,net_need:0},snapshot:{...base.snapshot,valid:false,load_valid:false,error:'采集失败'}})
assert.equal(bad.rules[0].status,'未评估')
assert.doesNotMatch(bad.trigger,/产生净缺口的规则/)
const paused=decisionReport({...base,no_restock_active:true,started:0,suggested:0})
assert.equal(paused.outcome,'本轮未新增补货')
assert.match(paused.steps.join(' '),/每日禁补已覆盖建议/)
const empty=decisionReport({...base,concurrency_need:0,empty_group_need:2,snapshot:{...base.snapshot,accounts:[]},trace:{...base.trace,concurrency_triggered:false,net_need:2,after_minimum:2,group_inflight:0}})
assert.match(empty.trigger,/空分组启动/)
assert.match(empty.steps[1],/不抬高到最小批量/)
assert.match(empty.rules.find(r=>r.key==='empty').evidence,/历史快照未记录 OAuth 数量，原账号总数 0/)
const oauthEmpty=decisionReport({...base,empty_group_need:2,snapshot:{...base.snapshot,monitor_oauth_count:0}})
assert.match(oauthEmpty.rules.find(r=>r.key==='empty').evidence,/监控组 OAuth 0 个账号/)
assert.equal(oauthEmpty.rules.find(r=>r.key==='empty').status,'产生补货缺口')
const oauthPresent=decisionReport({...base,snapshot:{...base.snapshot,accounts:[],monitor_oauth_count:1}})
assert.doesNotMatch(oauthPresent.rules.find(r=>r.key==='empty').status,/条件达到/)
const oauthUnknown=decisionReport({...base,snapshot:{...base.snapshot,accounts:[],monitor_oauth_error:'账号类型不明确'}})
assert.match(oauthUnknown.rules.find(r=>r.key==='empty').evidence,/账号类型不明确/)
assert.doesNotMatch(oauthUnknown.rules.find(r=>r.key==='empty').status,/条件达到/)
assert.equal(decisionReport({}).evaluation,false)
console.log('Passed: historical trigger reasons, sizing, caps, legacy/failed data, events and daily pause')

const scaled={...base,capacity:48,reported_capacity:80,used:33,concurrency_need:1,config:{...base.config,capacity_percent:60,trigger_percent:70},trace:{...base.trace,new_account_capacity:40,effective_new_account_capacity:24,concurrency_trigger:33,future_capacity:48}}
const scaledRule=decisionReport(scaled).rules.find(r=>r.key==='concurrency')
assert.match(scaledRule.evidence,/原始容量 80 × 60% = 48/)
assert.match(scaledRule.evidence,/≥ 33 并发（70% 向下取整）/)
assert.match(scaledRule.extra,/每号 24 有效槽位/)
assert.doesNotMatch(scaledRule.extra,/每号 40 有效槽位/)
console.log('Passed: saved capacity percentage, rounded trigger and scaled new-account capacity in decision details')

const exitDecision={...base,started:2,suggested:2,concurrency_need:0,exit_need:2,reason:'净缺口取最大值',
 config:{...base.config,exit_enabled:true,exit_minutes:8,target_percent:60,confirmations:3},
 trace:{...base.trace,net_need:2,new_account_capacity:60,effective_new_account_capacity:60,strict_minimum:false},
 exit:{valid:true,need:2,at_risk:2,confirmations:3,demand:71,rows:[],points:[{need:2,capacity:0,sizing_capacity:0,pending:0,later_pending:0,target_percent:60}]}}
assert.match(decisionSummary(exitDecision),/退出前补货缺 2 个（取最大值）；已预留 2 个/)
assert.match(exitSizing(exitDecision),/需求 71 ÷ 目标利用率 60% − 到位时容量 0/)
assert.match(exitSizing(exitDecision),/每新号容量 60，向上取整 = 2 个/)
assert.match(decisionSummary({...exitDecision,started:0,suggested:0,reason:'冷却尚未结束'}),/本轮未补货。冷却尚未结束/)
assert.match(decisionSummary({...exitDecision,exit_need:1,trace:{...exitDecision.trace,strict_minimum:true,net_need:1,after_minimum:2}}),/最小批量将 1 个补到 2 个/)
assert.equal(decisionSummary({reason:'已保存配置'}),'已保存配置')
assert.match(exitEvidence({}).basis,/旧记录未保存/)
assert.equal(exitEvidence({evidence:{source:'deadline'}}).sample,'')
const percent=exitEvidence({evidence:{source:'quota_percent',sample_at:'2026-11-20T03:31:03Z',rate:16.4468,sample_seconds:10.944,window_seconds:604800}})
assert.equal(percent.basis,'额度百分比预测')
assert.match(percent.detail,/16.447 个百分点\/分钟；样本覆盖 10.944 秒/)
assert.match(percent.sample,/11:31:03/)
const money=exitEvidence({evidence:{source:'money',sample_at:'2026-11-20T03:31:03Z',rate:0,sample_seconds:180,window_seconds:604800}})
assert.match(money.detail,/近 3 分钟消耗 0 USD\/分钟/)
assert.equal(exitSizing({}),'')
const tiered={...exitDecision,config:{...exitDecision.config,capacity_tiers_enabled:true,capacity_tiers:[{min_capacity:0,trigger_percent:60,target_percent:50}]},trace:{...exitDecision.trace,concurrency_trigger_percent:60,concurrency_target_percent:50},exit:{...exitDecision.exit,points:[{...exitDecision.exit.points[0],target_percent:50}]}}
assert.match(exitSizing(tiered),/目标利用率 50%/)
assert.match(decisionReport(tiered).rules.find(r=>r.key==='concurrency').extra,/补后目标利用率 50%/)
console.log('Passed: readable outcomes, actual quantity, exit formula, per-account sources and legacy evidence')

// Render the real decision and exit templates with the recorded calculation.
const {readFileSync}=await import('node:fs'), vm=await import('node:vm')
const Vue=await import('vue'), {renderToString}=await import('@vue/server-renderer')
const {parse,compileScript}=await import('@vue/compiler-sfc'), {transformWithEsbuild}=await import('vite')
const components={}
for(const name of ['ProExitStatus','ProRestockDecisionDetails']) {
 const file=new URL(`../src/components/${name}.vue`,import.meta.url),source=readFileSync(file,'utf8'),modules={vue:Vue}
 for(const [,id] of source.matchAll(/^import .* from ['"]([^'"]+)['"]/gm)) {
  if(id==='vue')continue
  modules[id]=id.endsWith('.vue')?{default:components[id.split('/').pop().replace('.vue','')]||{render:()=>null},__esModule:true}:await import(new URL(id+'.js',file))
 }
 const script=compileScript(parse(source).descriptor,{id:name,inlineTemplate:true})
 const {code}=await transformWithEsbuild(script.content,name+'.js',{format:'cjs',loader:'js'})
 const mod={exports:{}}
 vm.runInNewContext(code,{module:mod,exports:mod.exports,require:id=>modules[id],console})
 components[name]=mod.exports.default
}
exitDecision.exit.rows=[{id:1,name:'fixture@example.com',capacity:60,exit_minutes:4.44,reason:'达到额度合并阈值',risk:true,evidence:{source:'quota_percent',sample_at:'2026-11-20T03:31:03Z',rate:16.4468,sample_seconds:10.944,window_seconds:604800}}]
const html=await renderToString(Vue.createSSRApp(components.ProRestockDecisionDetails,{decision:exitDecision}))
for(const text of ['退出前补货缺 2 个','已预留 2 个','fixture@example.com','额度百分比预测','样本时间','10.944 秒','需求 71','每新号容量 60','向上取整 = 2 个','旧记录未保存逐数量确认'])assert.ok(html.includes(text),text)
assert.doesNotMatch(html,/undefined|NaN/)
console.log('Passed: actual Vue decision and exit templates render the complete saved explanation')
const confirmed={raw_need:2,confirmed_need:1,levels:[{quantity:1,count:3},{quantity:2,count:1}]}
const current={...exitDecision,exit_need:1,started:1,suggested:1,trace:{...exitDecision.trace,version:2,net_need:1},exit:{...exitDecision.exit,confirmation:confirmed}}
const newHtml=await renderToString(Vue.createSSRApp(components.ProRestockDecisionDetails,{decision:current}))
for(const text of ['计算缺 2 个，已确认 1 个','缺至少1个：3/3次','缺至少2个：1/3次','重复缓存不累计'])assert.ok(newHtml.includes(text),text)
assert.doesNotMatch(newHtml,/undefined|NaN|旧记录未保存逐数量确认/)
console.log('Passed: actual Vue per-quantity confirmation, partial approval and cache explanation')
