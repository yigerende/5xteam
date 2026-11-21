<script setup>
import {computed} from 'vue'
import {exitReport,exitMinutes,exitEvidence,exitSizing} from '../restockExit'
import {confirmationText} from '../restockDecision'
const props=defineProps({decision:{type:Object,default:()=>({})},config:{type:Object,default:()=>({})},unavailable:Boolean,compact:Boolean})
defineEmits(['details'])
const report=computed(()=>exitReport(props.decision,props.config,props.unavailable))
const n=v=>v??'—'
</script>
<template>
 <div class="exit-status" :class="{compact}" aria-label="账号退出前补货">
  <span class="title">账号退出前补货</span><strong>{{report.earliest}}</strong>
  <p>{{report.status}} · 提前 {{config.exit_minutes??5}} 分钟</p>
  <p>当前需求 {{n(report.observed)}}/{{n(report.current)}} · 退出后 {{n(report.demand)}}/{{n(report.after)}}</p>
  <p>临期 {{report.risk}} 个 · 待补 {{report.need||0}} 个</p>
  <small v-if="report.held">采用退出前近期需求，等待确认流量回落</small>
  <small v-else-if="report.gap">可能存在接替空档，详见到位时间</small>
  <small v-else>逐号预计耗完／截止时间＋实际并发</small>
  <button v-if="compact" type="button" @click="$emit('details')">查看接替依据</button>
  <template v-else>
   <p v-if="report.unknown" class="hint">{{report.unknown}} 个账号的额度预测数据不足；有效的截止时间和其他账号数据仍独立参与判断。</p>
   <div class="scroll"><table><thead><tr><th>账号</th><th>预计耗完</th><th>预计退出</th><th>退出依据</th><th>容量</th></tr></thead><tbody>
    <tr v-for="r in report.rows" :key="r.id"><td>{{r.name||'#'+r.id}}</td><td>{{exitMinutes(r.depletion_minutes)}}</td><td>{{r.warning_only?'接近阈值，时间未知':exitMinutes(r.exit_minutes)}}</td><td>{{r.reason}}<small>{{exitEvidence(r).basis}}</small><small v-if="exitEvidence(r).sample">样本时间 {{exitEvidence(r).sample}}</small><small v-if="exitEvidence(r).detail">{{exitEvidence(r).detail}}</small><small v-if="r.threshold!=null">额度 {{r.used_percent??'未知'}}% / 合并阈值 {{r.threshold}}%</small></td><td>{{r.capacity}}</td></tr>
    <tr v-if="!report.rows.length"><td colspan="5">暂无有效逐账号预测</td></tr>
   </tbody></table></div>
   <div v-if="report.points.length" class="scroll"><table><thead><tr><th>预计退出时点</th><th>剩余容量</th><th>及时在途容量</th><th>新号预计可用时容量</th><th>净缺口</th></tr></thead><tbody><tr v-for="(p,i) in report.points" :key="i"><td>{{exitMinutes(p.minutes)}}</td><td>{{p.capacity}}</td><td>{{p.pending_capacity}}（{{p.pending}} 个）</td><td>{{p.sizing_capacity}}<small>{{exitMinutes(p.sizing_minutes)}}后 · 另有 {{p.later_pending}} 个在途到位</small></td><td>{{p.need}} 个<small v-if="p.gap">存在短时容量缺口</small></td></tr></tbody></table></div>
   <p v-if="exitSizing(decision)" class="calculation">{{exitSizing(decision)}}</p>
   <p v-if="decision.exit?.confirmation" class="hint">{{confirmationText(decision.exit.confirmation,decision.config?.confirmations)}} 预测按相关账号的新消耗证据确认，重复缓存不累计。</p>
   <p v-else class="hint">历史退出压力确认 {{decision.exit?.confirmations??'未记录'}} / {{decision.config?.confirmations??'未记录'}} 次；旧记录未保存逐数量确认。</p>
   <p class="hint">按退出后的需求与容量判断，按新增任务预计到位时的缺口定数量。晚到但能先于新增任务补齐的在途，不重复开通。</p>
  </template>
 </div>
</template>
<style scoped>
.exit-status{font-size:12px;line-height:1.65}.title{color:var(--muted);font-size:12px}.exit-status strong{display:block;font-size:22px;margin:15px 0;font-weight:550}.exit-status p{margin:6px 0}.exit-status small,.hint{color:var(--muted);font-size:11px}.exit-status small{display:block}.exit-status button{display:block;margin-top:12px;border:1px solid var(--border);border-radius:6px;padding:6px 9px;background:transparent;color:var(--ink);font:inherit;cursor:pointer}.scroll{overflow:auto;margin:14px 0}table{width:100%;border-collapse:collapse;min-width:600px}th,td{text-align:left;border-bottom:1px solid var(--border);padding:9px;vertical-align:top}th{color:var(--muted);font-weight:500}td:first-child{overflow-wrap:anywhere;max-width:220px}button:focus-visible{outline:2px solid var(--teal);outline-offset:2px}
</style>
