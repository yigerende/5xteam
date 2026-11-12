<script setup>
import { computed } from 'vue'
import { accountDisposition } from '../pro-disposition'
import { LoaderCircle } from 'lucide-vue-next'
const props = defineProps({ state: { type: Object, default: () => ({}) }, editable: Boolean, disabled: Boolean, deleteMode: Boolean, merchantMode:Boolean, mergeV2Mode:Boolean, disposition:String })
const emit = defineEmits(['change'])
const mode = computed(() => props.disposition || accountDisposition({pro_auto:props.state}, {auto_delete_enabled:props.deleteMode,auto_merchant_enabled:props.merchantMode,auto_merge_v2_enabled:props.mergeV2Mode}))
const steps = computed(() => [['login','登录'],['recharge','开通'],['oauth',props.state.oauth_source === 'siwc_sub2api' ? 'SIWC 授权（s2接口）' : props.state.oauth_source === 'siwc_local' ? 'SIWC 授权（本地）' : 'RT/AT'],['push','推送'],['quota','额度'],[mode.value,{merge:'合并',deactivate:'删除',merchant:'API 商户',merge_v2:'合并2.0'}[mode.value]]])
const names = {not_started:'未开始',pending:'待处理',running:'执行中',waiting:'等待额度',completed:'已完成',skipped:'已跳过',failed:'失败',interrupted:'已中断',unknown:'待确认'}
function change(key,label,event) { const status=event.target.value;event.target.value=props.state.steps?.[key]||'not_started';emit('change',{key,label,status}) }
</script>
<template><div class="pro-auto-stages" :class="{editable}"><span v-for="[key,label] in steps" :key="key" :data-stage="key" :class="state.steps?.[key] || 'pending'" :title="label + '：' + (state.errors?.[key] || (key==='merchant' && state.steps?.[key]==='waiting' ? '商户处理中' : key==='deactivate' && state.steps?.[key]==='waiting' ? '等待确认' : names[state.steps?.[key]]) || '未开始') + (state.manual?.[key] ? '（手动修正）' : '')"><LoaderCircle v-if="state.steps?.[key] === 'running'" :size="11" class="spin" />{{ label }}<select v-if="editable && key!=='merchant' && key!=='merge_v2'" :value="state.steps?.[key]||'not_started'" :aria-label="'全自动'+label+'阶段状态'" :disabled="disabled" @change="change(key,label,$event)"><option value="not_started">未开始</option><option value="pending">待处理</option><option v-if="state.steps?.[key]==='running'" value="running" disabled>执行中</option><option v-if="key==='quota'||key==='deactivate'" value="waiting">{{ key==='deactivate'?'等待确认':'等待额度' }}</option><option value="completed">已完成</option><option value="failed">失败</option><option value="skipped">已跳过</option><option value="interrupted">已中断</option><option value="unknown">待确认</option></select><small v-else>{{ key === 'merchant' && state.steps?.[key] === 'waiting' ? '商户处理中' : key === 'deactivate' && state.steps?.[key] === 'waiting' ? '等待确认' : names[state.steps?.[key]] || '未开始' }}</small><i v-if="state.manual?.[key]" class="manual-mark" aria-label="手动修正">手</i></span></div></template>
<style scoped>
.pro-auto-stages { display:grid;grid-template-columns:repeat(3,minmax(0,1fr));gap:4px;min-width:260px; }
.pro-auto-stages>span {display:flex;align-items:center;gap:4px;padding:4px 6px;font-size:11px;border:1px solid var(--line);border-radius:5px;color:var(--muted);white-space:normal;flex-wrap:wrap;}
small {font-size:10px;}
.editable{min-width:310px}.editable select{width:76px;min-width:0;border:0;background:transparent;color:inherit;padding:0;font:inherit;cursor:pointer}.editable select:disabled{cursor:default}.editable select:focus-visible{outline:2px solid var(--green);outline-offset:2px}.manual-mark{font-size:9px;font-style:normal;border:1px solid currentColor;border-radius:3px;padding:0 2px}
.completed {color:var(--green-strong)!important;background:var(--green-bg);border-color:var(--green)!important;}
.running,.waiting,.unknown {color:var(--amber)!important;background:var(--amber-bg);}
.failed,.interrupted {color:var(--red)!important;background:var(--red-bg);}
.spin {animation:pro-stage-spin 1s linear infinite;}
@keyframes pro-stage-spin {to {transform:rotate(360deg);}}
</style>
