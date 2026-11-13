<script setup>
import { ref } from 'vue'
import { api } from '../api'

const props = defineProps({ account: { type: Object, required: true }, completed: Boolean })
const emit = defineEmits(['updated'])
const busy = ref(false), error = ref('')
async function retry() {
  if (busy.value) return
  busy.value = true
  error.value = ''
  try {
    await api(`/api/pro-accounts/${encodeURIComponent(props.account.email)}/siwc-marker/retry`, { method: 'POST' })
    emit('updated')
  } catch (e) { error.value = e.message }
  finally { busy.value = false }
}
</script>

<template>
  <div v-if="account.siwc_marker && (completed || account.siwc_marker.state === 'retry')" class="marker" role="status">
    <span v-if="account.siwc_marker.state === 'synced'">SIWC 完成标记已同步</span>
    <span v-else-if="account.siwc_marker.state === 'retry'" class="retry">{{ account.siwc_marker.error || 'SIWC 标记待重试' }}</span>
    <span v-else>SIWC 标记待同步</span>
    <button v-if="account.siwc_marker.state === 'retry'" type="button" :disabled="busy" title="只重新同步标记，不重跑全自动流程" @click.stop="retry">{{ busy ? '提交中…' : '重试标记' }}</button>
    <small v-if="error" role="alert">{{ error }}</small>
  </div>
</template>

<style scoped>
.marker { display:flex;flex-wrap:wrap;gap:4px 8px;margin-top:5px;font-size:11px;color:var(--muted);max-width:330px;white-space:normal;overflow-wrap:anywhere }
.retry,.marker small { color:var(--amber) }
.marker button { color:var(--green-strong);border:0;background:transparent;padding:0;font:inherit;text-decoration:underline;cursor:pointer }
.marker button:disabled { opacity:.5;cursor:wait }
</style>
