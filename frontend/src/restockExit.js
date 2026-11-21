const finite=n=>typeof n==='number'&&Number.isFinite(n)&&n>=0
export const exitMinutes=n=>finite(n)?n.toFixed(1)+' 分钟':'暂不可估'
const numberLabel=n=>finite(n)?Number(n.toFixed(3)).toLocaleString('zh-CN',{maximumFractionDigits:3}):'未记录'
export function exitEvidence(row={}) {
 const e=row.evidence
 if(!e)return {basis:'旧记录未保存预测来源',sample:'',detail:''}
 const names={money:'费用消耗预测',quota_percent:'额度百分比预测',deadline:'推送等待时限',leaving:'已进入移出流程'}
 const at=Date.parse(e.sample_at)
 const sample=Number.isFinite(at)&&at>0?new Date(at).toLocaleString('zh-CN',{hour12:false,timeZone:'Asia/Shanghai'}):''
 const window=e.window_seconds===604800?'7 天窗口':e.window_seconds===18000?'5 小时窗口':'额度窗口'
 let detail=''
 if(e.source==='money')detail=`${window}；近 ${numberLabel(e.sample_seconds/60)} 分钟消耗 ${numberLabel(e.rate)} USD/分钟`
 if(e.source==='quota_percent')detail=`${window}；采样速率 ${numberLabel(e.rate)} 个百分点/分钟；样本覆盖 ${numberLabel(e.sample_seconds)} 秒`
 return {basis:names[e.source]||'未记录',sample,detail}
}
export function exitSizing(decision={}) {
 const f=decision.exit,c=decision.config||{},tr=decision.trace||{}
 const point=f?.points?.find(p=>p.need===f.need&&p.need>0)
 const newCapacity=tr.effective_new_account_capacity??tr.new_account_capacity
 if(!point||!finite(newCapacity)||newCapacity<=0||!finite(f.demand)||!finite(point.sizing_capacity))return ''
 const tier=c.capacity_tiers_enabled?[...(c.capacity_tiers||[])].filter(t=>t.min_capacity<=point.capacity).sort((a,b)=>b.min_capacity-a.min_capacity)[0]:null
 const target=point.target_percent||tier?.target_percent||c.target_percent
 if(!finite(target)||target<=0)return ''
 return `（需求 ${f.demand} ÷ 目标利用率 ${target}% − 到位时容量 ${numberLabel(point.sizing_capacity)}）÷ 每新号容量 ${numberLabel(newCapacity)}，向上取整 = ${point.need} 个。到位时容量已包含 ${numberLabel(point.pending+point.later_pending)} 个可及时到位的在途账号。`
}
export function exitReport(decision={},config=decision.config||{},unavailable=false){
 const f=decision.exit
 const sameVersion=decision.version==null||config.version==null||decision.version===config.version
 const sameGroup=decision.snapshot?.group_id==null||config.group_id==null||decision.snapshot.group_id===config.group_id
 const valid=!unavailable&&sameVersion&&sameGroup&&!!f?.valid&&!!decision.demand_known
 const active=config.exit_enabled
 const status=!active?'规则未开启':!valid?'等待有效监控数据':!config.enabled?'总开关关闭':config.mode!=='auto'?'观察模式':decision.no_restock_active?'每日禁补中':decision.exit_need>0?'存在补货缺口':f.pressure&&f.need===0?'在途已覆盖后续缺口':f.pressure&&!f.confirmed?'等待连续确认':'容量充足／未到提前时间'
 return {valid,status,earliest:valid?exitMinutes(f.earliest_minutes):'暂不可估',rows:valid?(f.rows||[]):[],points:valid?(f.points||[]):[],observed:valid?f.observed_demand:null,demand:valid?f.demand:null,current:valid?f.current_capacity:null,after:valid?f.after_capacity:null,need:valid?decision.exit_need:0,risk:valid?f.at_risk:0,held:valid&&f.demand_held,gap:valid&&f.transient_gap,unknown:valid?f.unknown:0}
}
export function accountExitLabels(decision,row,unavailable=false){
 if(unavailable)return {depletion:'数据过期',exit:'数据过期',reason:''}
 const r=decision.exit?.valid?decision.exit.rows?.find(a=>a.id===row.id):null
 const depletion=r?exitMinutes(r.depletion_minutes):!row.reason&&finite(row.remaining_usd)&&finite(row.rate_usd)&&row.rate_usd>0?exitMinutes(row.remaining_usd/row.rate_usd):row.rate_usd===0?'暂无消耗':'暂不可估'
 return {depletion,exit:r?.warning_only?'额度接近阈值':exitMinutes(r?.exit_minutes),reason:r?.reason||'尚无退出预测'}
}
