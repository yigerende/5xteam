import { createApp, h, ref } from 'vue'
import ProManagementView from '../src/components/ProManagementView.vue'
import '../src/styles.css'

// Isolated UI test; all requests are mocked and saves survive page reloads.
const initial = {
  provider: 'sub2',
  sub2: { url: 'https://sub2.example.com', email: 'admin@example.com', password_present: true, group_ids: [7, 8], group_names: ['不降智', '备用组'], models: ['gpt-6-astra'], account_concurrency: 10, priority: 1 },
  cpa: { url: 'https://cpa.example.com', key_present: true, group_ids: [9], group_names: ['CPA 已保存'] },
}
const key = 'pro-push-preview'
const fixture = window.proPushFixture = {
  saved: JSON.parse(sessionStorage.getItem(key) || 'null') || initial,
  requests: [], accounts: [], fail: '', delay: 50,
  groups: { sub2: [{ id: 7, name: '不降智' }, { id: 8, name: '备用组' }, { id: 10, name: '其他组' }], cpa: [{ id: 9, name: 'CPA 已保存' }] },
}
const reply = (data, error = '') => new Response(JSON.stringify({ ok: !error, data, error }), { status: error ? 502 : 200, headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin), method = options.method || 'GET'
  const body = options.body ? JSON.parse(options.body) : null
  fixture.requests.push({ path: url.pathname, method, body })
  if (url.pathname === '/api/pro-settings') {
    if (method === 'PUT') {
      fixture.saved = { ...body, sub2_password: '', cpa_key: '' }
      sessionStorage.setItem(key, JSON.stringify(fixture.saved))
    }
    return reply(fixture.saved)
  }
  if (url.pathname === '/api/pro-accounts') return reply({ items: fixture.accounts, total: fixture.accounts.length })
  if (url.pathname.endsWith('/merge') && method === 'POST') {
    const account = fixture.accounts.find(a => url.pathname.includes(encodeURIComponent(a.email)))
    if (!account) throw new Error('Unknown fixture account')
    account.pro_workflow_running = true
    account.pro_post_merge_groups.status = 'running'
    account.pro_post_merge_groups.error = ''
    setTimeout(() => { account.pro_post_merge_groups.status = 'completed'; account.pro_workflow_running = false }, 800)
    return reply(account)
  }
  const match = url.pathname.match(/^\/api\/pro-settings\/(sub2|cpa)\/(groups|test)$/)
  if (match) {
    const provider = match[1], fail = fixture.fail === provider, groups = structuredClone(fixture.groups[provider])
    // Deliberately ignore abort here to verify that late replies cannot update the UI.
    await new Promise(resolve => setTimeout(resolve, fixture.delay))
    return reply(match[2] === 'test' ? { connected: true, groups } : groups, fail ? '模拟连接失败' : '')
  }
  throw new Error('Unmocked request: ' + url.pathname)
}
const visible = ref(true)
createApp({ setup: () => () => h('div', [
  h('button', { onClick: () => { visible.value = !visible.value }, 'data-testid': 'switch-page' }, visible.value ? '切换到其他页面' : '返回 Pro 管理'),
  visible.value ? h(ProManagementView) : h('p', '其他页面'),
]) }).mount('#app')
