<script setup>
import { computed, onMounted, reactive, ref, watch } from 'vue'
import {
  ArrowLeftRight, Bot, ChevronDown, Crown, History, Inbox, LogIn, Menu, Moon, Network, Settings,
  Smartphone, Sun, UserRoundCog, UsersRound, Waypoints, X, LogOut, KeyRound,
} from 'lucide-vue-next'
import { api } from './api'
import AdminAccountsView from './components/AdminAccountsView.vue'
import HistoryView from './components/HistoryView.vue'
import FreePipelineView from './components/FreePipelineView.vue'
import MailManagementView from './components/MailManagementView.vue'
import SmsManagementView from './components/SmsManagementView.vue'
import OpenAIAccountsView from './components/OpenAIAccountsView.vue'
import ProManagementView from './components/ProManagementView.vue'
import ProxiesView from './components/ProxiesView.vue'
import SettingsView from './components/SettingsView.vue'
import StatusPill from './components/StatusPill.vue'
import WorkflowView from './components/WorkflowView.vue'
import LoginView from './components/LoginView.vue'

const nav = [
  { id: 'pro', label: 'Pro 管理', icon: Crown, group: '账号' },
  { id: 'space-merge', label: '空间合并', icon: ArrowLeftRight, group: '空间合并' },
  { id: 'mail', label: '邮件管理', icon: Inbox, group: '账号流水线' },
  { id: 'free', label: 'Team 轮转', icon: Waypoints, group: '账号流水线' },
  { id: 'admins', label: '母号管理', icon: UserRoundCog, group: '账号' },
  { id: 'sms', label: '接码管理', icon: Smartphone, group: '账号' },
  { id: 'history', label: '执行历史', icon: History, group: '数据' },
  { id: 'openai', label: 'OpenAI 账号', icon: Bot, group: '账号' },
  { id: 'proxies', label: '代理管理', icon: Network, group: '系统' },
  { id: 'settings', label: '接口设置', icon: Settings, group: '系统' },
]
const current = ref(localStorage.getItem('space-console-tab') || 'full')
const mobileOpen = ref(false)
const theme = ref(localStorage.getItem('space-console-theme-v2') || 'light')
const settings = ref(null)
const proxies = ref([])
const adminAccounts = ref([])
const openAIAccounts = ref([])
const history = ref([])
const freeAccounts = ref([])
const proAccounts = ref([])
const referenceLoaded = reactive({ admins: false, proxies: false, pro: false })
const teamEntryEmail = ref('')
const loadingError = ref('')
const health = ref(true)
const jobs = reactive({ full: false, enter: false, transfer: false, kick: false })
const anyActive = computed(() => Object.values(jobs).some(Boolean))
const spaceMergeItems = [
  { id: 'full', label: '任务台', icon: Bot },
  { id: 'enter', label: '进入空间', icon: LogIn },
  { id: 'transfer', label: '合并空间', icon: ArrowLeftRight },
  { id: 'kick', label: '移出空间', icon: UsersRound },
]
const spaceMergeIDs = new Set(spaceMergeItems.map((item) => item.id))
const spaceMergeActive = computed(() => spaceMergeIDs.has(current.value))
const spaceMergeVisited = ref(spaceMergeActive.value)
const activeNav = computed(() => {
  if (spaceMergeActive.value) return { ...nav[0], label: spaceMergeItems.find((item) => item.id === current.value)?.label || '任务台' }
  return nav.find((item) => item.id === current.value) || nav[0]
})
const runtimeHost = window.location.host
const authenticated = ref(false)
const authReady = ref(false)
const passwordDialog = ref(false)
const passwordForm = reactive({ current_password: '', new_password: '', confirm_password: '' })
const passwordMessage = ref('')
const passwordBusy = ref(false)

watch(theme, (value) => {
  document.documentElement.dataset.theme = value
  localStorage.setItem('space-console-theme-v2', value)
}, { immediate: true })
watch(current, (value) => {
  localStorage.setItem('space-console-tab', value)
  if (spaceMergeIDs.has(value)) spaceMergeVisited.value = true
  ensureTabData(value).catch((error) => { loadingError.value = error.message })
})

function selectTab(id) {
  current.value = id
  mobileOpen.value = false
}
function isNavActive(item) { return item.id === 'space-merge' ? spaceMergeActive.value : current.value === item.id }
async function reloadAdmins() { try { adminAccounts.value = await api('/api/admin-accounts'); referenceLoaded.admins = true } catch (error) { loadingError.value = error.message } }
function reloadOpenAI() {}
async function reloadProxies() { try { proxies.value = await api('/api/proxies'); referenceLoaded.proxies = true } catch (error) { loadingError.value = error.message } }
function reloadHistory() {}
function reloadFreeAccounts() {}
async function reloadProAccounts() { try { proAccounts.value = await api('/api/pro-accounts'); referenceLoaded.pro = true } catch (error) { loadingError.value = error.message } }
async function syncProAccounts() { if (referenceLoaded.pro) await reloadProAccounts() }
async function ensureTabData(id) {
  const requests = []
  if ((spaceMergeIDs.has(id) || ['free', 'admins', 'pro'].includes(id)) && !referenceLoaded.admins) requests.push(reloadAdmins())
  if (id === 'admins' && !referenceLoaded.proxies) requests.push(reloadProxies())
  if (spaceMergeIDs.has(id) && !referenceLoaded.pro) requests.push(reloadProAccounts())
  if (['proxies', 'settings'].includes(id) && !referenceLoaded.proxies) requests.push(reloadProxies())
  await Promise.all(requests)
}
async function selectProxy(url) {
  if (!settings.value) return
  try {
    const saved = await api('/api/settings', { method: 'PUT', body: { ...settings.value, concurrency: 1, proxy_url: url } })
    settings.value = saved
  } catch (error) { loadingError.value = error.message }
}
function updateJob(mode, value) { jobs[mode] = value }
async function openTeamFromMail(profile) {
  teamEntryEmail.value = profile?.email || ''
  selectTab('free')
}
async function load() {
  try {
    settings.value = await api('/api/settings')
    await ensureTabData(current.value)
  } catch (error) { loadingError.value = error.message; health.value = false }
}
async function checkAuth() {
  try { await api('/api/auth/me'); authenticated.value = true; await load() } catch { authenticated.value = false }
  finally { authReady.value = true }
}
function onAuthenticated() { authenticated.value = true; load() }
async function logout() { await api('/api/auth/logout', { method: 'POST' }); authenticated.value = false }
async function changePassword() {
  passwordBusy.value = true; passwordMessage.value = ''
  try { await api('/api/auth/change-password', { method: 'POST', body: passwordForm }); passwordDialog.value = false; authenticated.value = false }
  catch (error) { passwordMessage.value = error.message }
  finally { passwordBusy.value = false }
}
onMounted(checkAuth)
</script>

<template>
  <LoginView v-if="authReady && !authenticated" @authenticated="onAuthenticated" />
  <div v-else-if="authenticated" class="app-shell">
    <header class="topbar">
      <div class="topbar-inner">
        <button class="brand" type="button" aria-label="返回任务台" @click="selectTab('full')"><span class="brand-mark"><ArrowLeftRight :size="18" /></span><span><strong>Space Console</strong><small>Team Automation</small></span></button>
        <nav class="desktop-nav" aria-label="主导航"><button v-for="item in nav" :key="item.id" type="button" :class="{ active: isNavActive(item) }" @click="selectTab(item.id === 'space-merge' ? 'full' : item.id)"><component :is="item.icon" :size="15" />{{ item.label }}</button></nav>
      </div>
      <div class="context-bar"><div class="context-inner"><span><component :is="activeNav.icon" :size="14" />{{ activeNav.group }}</span><ChevronDown :size="13" /><strong>{{ activeNav.label }}</strong><i></i><small>{{ anyActive ? '有任务正在执行' : `运行地址 ${runtimeHost}` }}</small></div></div>
      <nav v-if="mobileOpen" class="mobile-nav" aria-label="移动端导航"><button v-for="item in nav" :key="item.id" type="button" :class="{ active: isNavActive(item) }" @click="selectTab(item.id === 'space-merge' ? 'full' : item.id)"><component :is="item.icon" :size="17" /><span>{{ item.label }}</span></button></nav>
    </header>

    <div class="topbar-actions" aria-label="账户与系统操作"><StatusPill :tone="health ? 'success' : 'danger'">{{ health ? '服务正常' : '连接异常' }}</StatusPill><button class="icon-button" type="button" title="修改密码" @click="passwordDialog = true"><KeyRound :size="17" /></button><button class="icon-button" type="button" title="退出登录" @click="logout"><LogOut :size="17" /></button><button class="icon-button theme-button" type="button" :title="theme === 'dark' ? '切换浅色主题' : '切换深色主题'" @click="theme = theme === 'dark' ? 'light' : 'dark'"><Sun v-if="theme === 'dark'" :size="17" /><Moon v-else :size="17" /></button><button class="icon-button mobile-menu" type="button" :aria-label="mobileOpen ? '关闭导航' : '打开导航'" @click="mobileOpen = !mobileOpen"><X v-if="mobileOpen" :size="18" /><Menu v-else :size="17" /></button></div>

    <main class="app-main">
      <div v-if="loadingError" class="global-alert"><strong>数据加载失败</strong><span>{{ loadingError }}</span><button type="button" @click="loadingError = ''; load()">重试</button></div>
      <ProManagementView v-if="current === 'pro'" :accounts="proAccounts" :admin-accounts="adminAccounts" @reload="syncProAccounts" />
      <template v-if="spaceMergeVisited">
        <WorkflowView v-show="current === 'full'" mode="full" :admin-accounts="adminAccounts" :pro-accounts="proAccounts" :any-active="anyActive" @job-state="updateJob" @history-changed="reloadHistory" @navigate="selectTab" />
        <WorkflowView v-show="current === 'enter'" mode="enter" :admin-accounts="adminAccounts" :pro-accounts="proAccounts" :any-active="anyActive" @job-state="updateJob" @history-changed="reloadHistory" @navigate="selectTab" />
        <WorkflowView v-show="current === 'transfer'" mode="transfer" :admin-accounts="adminAccounts" :pro-accounts="proAccounts" :any-active="anyActive" @job-state="updateJob" @history-changed="reloadHistory" @navigate="selectTab" />
        <WorkflowView v-show="current === 'kick'" mode="kick" :admin-accounts="adminAccounts" :pro-accounts="proAccounts" :any-active="anyActive" @job-state="updateJob" @history-changed="reloadHistory" @navigate="selectTab" />
      </template>
      <FreePipelineView v-if="current === 'free'" :active="true" :accounts="freeAccounts" :admin-accounts="adminAccounts" :entry-email="teamEntryEmail" @reload="reloadFreeAccounts" />
      <MailManagementView v-if="current === 'mail'" @open-team="openTeamFromMail" @pro-changed="syncProAccounts" />
      <SmsManagementView v-if="current === 'sms'" />
      <HistoryView v-if="current === 'history'" :history="history" @reload="reloadHistory" />
      <AdminAccountsView v-if="current === 'admins'" :accounts="adminAccounts" :proxies="proxies" @reload="reloadAdmins" />
      <OpenAIAccountsView v-if="current === 'openai'" :accounts="openAIAccounts" @reload="reloadOpenAI" />
      <ProxiesView v-if="current === 'proxies'" :proxies="proxies" :selected-u-r-l="settings?.proxy_url || ''" @reload="reloadProxies" @select="selectProxy" />
      <SettingsView v-if="current === 'settings'" :settings="settings" :proxies="proxies" @saved="settings = $event" />
    </main>
    <div v-if="passwordDialog" class="modal-backdrop" @click.self="passwordDialog = false"><section class="modal-panel"><div class="modal-heading"><div><span class="overline">ACCOUNT SECURITY</span><h2>修改密码</h2></div><button class="icon-button" type="button" title="关闭" @click="passwordDialog = false"><X :size="17" /></button></div><form class="modal-form" @submit.prevent="changePassword"><label class="field"><span>当前密码</span><input v-model="passwordForm.current_password" type="password" required /></label><label class="field"><span>新密码</span><input v-model="passwordForm.new_password" type="password" minlength="6" required /></label><label class="field"><span>确认新密码</span><input v-model="passwordForm.confirm_password" type="password" minlength="6" required /></label><div v-if="passwordMessage" class="login-error">{{ passwordMessage }}</div><div class="modal-actions"><button class="btn" type="button" @click="passwordDialog = false">取消</button><button class="btn primary" type="submit" :disabled="passwordBusy">{{ passwordBusy ? '保存中…' : '确认修改' }}</button></div></form></section></div>
  </div>
</template>
