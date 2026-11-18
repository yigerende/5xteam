<script setup>
import { ref } from 'vue'
import { Link2, LoaderCircle } from 'lucide-vue-next'
import { api } from '../api'

const props = defineProps({ account: { type: Object, required: true }, enabled: Boolean, disabled: Boolean })
const emit = defineEmits(['updated', 'busy'])
const busy = ref(false), error = ref(''), message = ref('')

async function link() {
  if (busy.value || props.disabled || !props.enabled) return
  busy.value = true
  error.value = message.value = ''
  emit('busy', true)
  try {
    const result = await api(`/api/pro-accounts/${encodeURIComponent(props.account.email)}/link-sub`, { method: 'POST', body: {} })
    message.value = '已关联，正在继续额度及后续流程'
    emit('updated', result)
  } catch (e) {
    error.value = e.message || '关联失败，请稍后重试'
  } finally {
    busy.value = false
    emit('busy', false)
  }
}
</script>

<template>
  <div v-if="enabled" class="sub-account-link">
    <button type="button" class="btn ghost compact" :disabled="busy || disabled" :aria-busy="busy" title="按邮箱查找已进入 Sub 的账号，关联后继续额度及后续流程" @click="link">
      <LoaderCircle v-if="busy" :size="12" class="spin" /><Link2 v-else :size="12" />{{ busy ? '关联中…' : '关联已有 Sub 账号' }}
    </button>
    <small v-if="error" class="link-error" role="alert">{{ error }}</small>
    <small v-else-if="message" role="status">{{ message }}</small>
  </div>
</template>

<style scoped>
.sub-account-link { display:flex;flex-direction:column;align-items:flex-start;gap:5px;margin-top:6px;max-width:240px }
.sub-account-link small { font-size:11px;line-height:1.5;white-space:normal;overflow-wrap:anywhere;color:var(--muted) }
.sub-account-link .link-error { color:var(--red) }
</style>
