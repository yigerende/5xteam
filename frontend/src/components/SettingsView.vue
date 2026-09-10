<script setup>
import { reactive, ref, watch } from 'vue'
import { Save, ShieldCheck } from 'lucide-vue-next'
import { api } from '../api'
import { maskProxyURL } from '../utils'
import MessageBar from './MessageBar.vue'
import StatusPill from './StatusPill.vue'

const props = defineProps({ settings: { type: Object, default: null }, proxies: { type: Array, default: () => [] } })
const emit = defineEmits(['saved'])
const form = reactive({})
const message = reactive({ text: '', type: '' })
const busy = ref(false)
watch(() => props.settings, (value) => { if (value) Object.assign(form, value, { concurrency: 1 }) }, { immediate: true, deep: true })
async function save() {
  busy.value = true
  try {
    const payload = {
      base_url: String(form.base_url || '').trim(), accepted_tos_version: String(form.accepted_tos_version || '').trim(), role: form.role,
      concurrency: 1, request_timeout_seconds: Number(form.request_timeout_seconds), network_retry_count: Number(form.network_retry_count),
      network_retry_interval_seconds: Number(form.network_retry_interval_seconds), invite_delay_seconds: Number(form.invite_delay_seconds),
      accept_delay_seconds: Number(form.accept_delay_seconds), transfer_delay_seconds: Number(form.transfer_delay_seconds),
      account_interval_seconds: Number(form.account_interval_seconds), proxy_url: form.proxy_url || '',
	  oauth_proxy_mode: form.oauth_proxy_mode || 'global',
      sms_provider: form.sms_provider || '', allow_sms: Boolean(form.allow_sms),
      auto_cleanup: Boolean(form.auto_cleanup), stop_on_first_failure: Boolean(form.stop_on_first_failure),
    }
    const saved = await api('/api/settings', { method: 'PUT', body: payload })
    Object.assign(form, saved, { concurrency: 1 }); emit('saved', saved)
    Object.assign(message, { text: '接口与执行设置已保存', type: 'success' })
  } catch (error) { Object.assign(message, { text: error.message, type: 'error' }) }
  finally { busy.value = false }
}
</script>

<template>
  <section class="view-stack">
    <header class="page-heading"><div><span class="overline">RUNTIME CONFIG</span><h1>接口设置</h1><p>调整请求端点、容错策略与任务执行节奏</p></div><div class="heading-actions"><StatusPill tone="success"><ShieldCheck :size="13" />本机配置</StatusPill><button class="btn primary" type="button" :disabled="busy || !settings" @click="save"><Save :size="15" />保存设置</button></div></header>
    <form v-if="settings" class="settings-layout" @submit.prevent="save">
      <section class="settings-section"><div class="section-heading"><span>CONNECTION</span><h2>接口与网络</h2><p>所有 OpenAI / ChatGPT 请求强制使用已配置的代理出口</p></div><div class="settings-fields"><label class="field wide"><span>API 基址</span><input v-model="form.base_url" spellcheck="false" /></label><label class="field"><span>TOS 版本</span><input v-model="form.accepted_tos_version" /></label><label class="field"><span>成员角色</span><select v-model="form.role"><option value="standard-user">standard-user</option><option value="admin">admin</option></select></label><label class="field wide"><span>全局代理</span><select v-model="form.proxy_url"><option value="">服务器直连</option><option v-for="proxy in proxies" :key="proxy.id" :value="proxy.url">{{ proxy.name }} · {{ maskProxyURL(proxy.url) }}</option></select></label><label class="field"><span>OAuth 代理策略</span><select v-model="form.oauth_proxy_mode"><option value="global">固定使用全局代理</option><option value="least_used">选择当前任务最少的代理</option></select><small>单个 OAuth / 重登任务从开始到换取 RT/AT 始终固定同一代理</small></label><label class="field"><span>实时接码平台</span><select v-model="form.sms_provider"><option value="">已导入号码池（自动选择）</option><option value="hero_sms">hero-sms</option><option value="nextpro">nextpro</option><option value="congou">congou</option><option value="chatai">chatai</option></select><small>仅在 Codex OAuth 返回 add_phone 时使用；平台密钥和卡池在“接码管理”配置</small></label><label class="toggle-row"><div><strong>允许 Team OAuth 自动接码</strong><small>普通 OAuth 不申请号码，只有 OpenAI 明确要求 add_phone 才触发</small></div><input v-model="form.allow_sms" type="checkbox" /><i></i></label></div></section>
      <section class="settings-section"><div class="section-heading"><span>RESILIENCE</span><h2>请求与重试</h2><p>控制超时和临时网络错误的恢复策略</p></div><div class="settings-fields"><label class="field"><span>串行并发数</span><input :value="1" type="number" disabled /><small>当前流程固定串行执行</small></label><label class="field"><span>请求超时（秒）</span><input v-model.number="form.request_timeout_seconds" type="number" min="5" max="300" /></label><label class="field"><span>网络重试次数</span><input v-model.number="form.network_retry_count" type="number" min="0" max="10" /></label><label class="field"><span>重试间隔（秒）</span><input v-model.number="form.network_retry_interval_seconds" type="number" min="0" max="120" /></label></div></section>
      <section class="settings-section"><div class="section-heading"><span>PACING</span><h2>执行节奏</h2><p>在关键步骤之间留出稳定等待时间</p></div><div class="settings-fields"><label class="field"><span>邀请后等待（秒）</span><input v-model.number="form.invite_delay_seconds" type="number" min="0" max="120" /></label><label class="field"><span>接受后等待（秒）</span><input v-model.number="form.accept_delay_seconds" type="number" min="0" max="120" /></label><label class="field"><span>合并后等待（秒）</span><input v-model.number="form.transfer_delay_seconds" type="number" min="0" max="120" /></label><label class="field"><span>账号间隔（秒）</span><input v-model.number="form.account_interval_seconds" type="number" min="0" max="120" /></label></div></section>
      <section class="settings-section"><div class="section-heading"><span>BEHAVIOR</span><h2>任务行为</h2><p>决定完成后的清理与异常处理方式</p></div><div class="toggle-list"><label class="toggle-row"><div><strong>自动清理邀请</strong><small>任务结束后清理遗留的待处理邀请</small></div><input v-model="form.auto_cleanup" type="checkbox" /><i></i></label><label class="toggle-row"><div><strong>遇到首个失败即停止</strong><small>一个账号失败后不再继续处理后续账号</small></div><input v-model="form.stop_on_first_failure" type="checkbox" /><i></i></label></div></section>
      <MessageBar :message="message" /><div class="settings-submit"><button class="btn primary" type="submit" :disabled="busy"><Save :size="15" />保存全部设置</button></div>
    </form>
    <section v-else class="panel loading-panel">正在读取接口设置...</section>
  </section>
</template>
