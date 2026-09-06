<script setup>
import { reactive, ref } from 'vue'
import { LogIn, ShieldCheck } from 'lucide-vue-next'
import { api } from '../api'

const emit = defineEmits(['authenticated'])
const form = reactive({ username: 'admin', password: 'admin' })
const busy = ref(false)
const error = ref('')
async function submit() {
  busy.value = true; error.value = ''
  try { await api('/api/auth/login', { method: 'POST', body: form }); emit('authenticated') }
  catch (e) { error.value = e.message }
  finally { busy.value = false }
}
</script>
<template>
  <main class="login-shell"><section class="login-panel"><div class="login-mark"><ShieldCheck :size="22" /></div><span class="overline">SPACE CONSOLE</span><h1>登录管理台</h1><p>登录后管理 Team 轮转、邮箱和空间任务</p><form @submit.prevent="submit"><label class="field"><span>账号</span><input v-model.trim="form.username" autocomplete="username" required /></label><label class="field"><span>密码</span><input v-model="form.password" type="password" autocomplete="current-password" required /></label><div v-if="error" class="login-error">{{ error }}</div><button class="btn primary login-submit" type="submit" :disabled="busy"><LogIn :size="16" />{{ busy ? '正在登录…' : '登录' }}</button></form></section></main>
</template>
