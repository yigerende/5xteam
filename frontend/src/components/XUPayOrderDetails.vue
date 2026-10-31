<script setup>
import { formatTime } from '../utils'
defineProps({ order: { type: Object, required: true } })
</script>

<template>
  <div v-if="order.provider === 'xupay'" class="xupay-details">
    <p v-if="order.remote?.automaticRunning">序付自动处理中，正在跟踪原订单</p>
    <p v-if="order.check_pending">核实请求结果待确认，正在查询原订单</p>
    <p v-if="order.rate_limited">接口限流 · 已自动重试 {{ order.rate_limit_retries || 0 }}/3 次<span v-if="!order.next_status_check_at">，自动重试已结束</span></p>
    <p v-if="order.next_status_check_at">下次{{ order.rate_limited ? '重试' : '查询' }}：{{ formatTime(order.next_status_check_at) }}</p>
    <details v-if="order.remote?.message || order.remote?.errorCode || order.remote?.events?.length">
      <summary>查看序付流程记录</summary>
      <p v-if="order.remote.errorCode">错误码：{{ order.remote.errorCode }}</p>
      <p v-if="order.remote.message">{{ order.remote.message }}</p>
      <ol v-if="order.remote.events?.length">
        <li v-for="(event, index) in order.remote.events" :key="index">
          <span>{{ event.createdAt ? formatTime(event.createdAt) : '' }} {{ event.stage }}</span>
          <p>{{ event.message }}</p>
        </li>
      </ol>
    </details>
  </div>
</template>

<style scoped>
.xupay-details{font-size:12px;line-height:1.7;max-width:480px;overflow-wrap:anywhere;white-space:normal;color:var(--muted)}.xupay-details p{margin:5px 0;white-space:pre-wrap}.xupay-details summary{cursor:pointer;color:var(--text)}.xupay-details ol{padding-left:20px;max-height:260px;overflow:auto}.xupay-details li{margin:10px 0}.xupay-details li>span{font-size:11px}
</style>
