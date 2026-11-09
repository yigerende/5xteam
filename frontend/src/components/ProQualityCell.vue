<script setup>
import { computed } from 'vue'
import { LoaderCircle } from 'lucide-vue-next'
import StatusPill from './StatusPill.vue'
import { formatTime } from '../utils'
const props = defineProps({ account: { type: Object, required: true } })
const q = computed(() => props.account.pro_quality || {})
const labels = { normal: '正常', variant: '正常变体', degraded: '降智', suspect: '疑似异常', unknown: '待确认', stale: '已过期', error: '查询失败' }
const actions = { merge: '合并', groups: '切组', restore: '恢复分组' }
const statuses = { pending: '待处理', running: '处理中', completed: '已完成', failed: '失败', cancelled: '已取消' }
const label = computed(() => q.value.status === 'degraded' && q.value.error_degraded ? '失败达限' : labels[q.value.status] || (props.account.push_status !== 'completed' ? '未推送' : props.account.push_provider !== 'sub2' ? '不支持' : '未查询'))
const tone = computed(() => ({ normal: 'success', degraded: 'danger', error: 'danger', suspect: 'pending' }[q.value.status] || 'neutral'))
</script>
<template>
  <div class="pro-quality-cell">
    <StatusPill :tone="tone">{{ label }}</StatusPill>
    <small v-if="q.error_failures" class="table-note">连续失败 {{ q.error_failures }}/{{ q.error_failure_limit || 2 }} 次</small>
    <small v-if="q.action && q.action_status" class="quality-action" :class="{ 'danger-text': q.action_status === 'failed' }"><LoaderCircle v-if="['pending','running'].includes(q.action_status)" class="spin" :size="11" />{{ actions[q.action] || q.action }}{{ statuses[q.action_status] || q.action_status }}</small>
    <small v-if="q.routed" class="table-note">当前在降智分组</small>
    <details v-if="q.fetched_at || q.error"><summary>检测详情</summary><div class="quality-detail">
      <span>答题：{{ labels[q.question_status] || '无结果' }}</span>
      <span>模型：{{ labels[q.model_audit?.status] || '无结果' }}</span>
      <span v-if="q.source_question_at">答题时间：{{ formatTime(q.source_question_at) }}</span>
      <span v-if="q.source_model_at">模型时间：{{ formatTime(q.source_model_at) }}</span>
      <span v-if="q.fetched_at">查询时间：{{ formatTime(q.fetched_at) }}</span>
      <span v-if="q.connection_check">连接复核：{{ q.connection_check.status === 'normal' ? '正常' : q.connection_check.status === 'unconfirmed' ? '未确认降智' : '失败' }}<template v-if="q.connection_check.http_status"> · HTTP {{ q.connection_check.http_status }}</template> · {{ formatTime(q.connection_check.checked_at) }}</span>
      <span v-if="q.model_audit?.response_model">返回模型：{{ q.model_audit.response_model }}</span>
      <span v-if="q.reason">{{ q.reason }}</span><span v-if="q.error" class="danger-text">{{ q.error }}</span><span v-if="q.action_error" class="danger-text">{{ q.action_error }}</span>
    </div></details>
  </div>
</template>
<style scoped>
.pro-quality-cell{min-width:96px;max-width:240px}.quality-action{display:flex;gap:4px;align-items:center;margin-top:5px;font-size:11px}.pro-quality-cell details{margin-top:5px;font-size:11px;color:var(--muted)}.pro-quality-cell summary{cursor:pointer}.quality-detail{display:grid;gap:5px;margin-top:8px;overflow-wrap:anywhere;white-space:normal;min-width:180px}
</style>
