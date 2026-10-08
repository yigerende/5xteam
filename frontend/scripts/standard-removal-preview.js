import { createApp } from 'vue'
import FreePipelineView from '../src/components/FreePipelineView.vue'
import '../src/styles.css'

const fixture = window.standardRemovalFixture = {
  requests: [],
  settings: JSON.parse(sessionStorage.getItem('standard-removal-settings') || 'null') || {
    enabled: false, threshold_percent: 50, interval_seconds: 300, team_operation_interval_seconds: 10,
    concurrency: 2, max_per_run: 0, retry_count: 1, join_method: 'mother_invite', remove_method: 'child_leave', oauth_login_mode: 'email_otp',
  },
  items: ['waiting_seat', 'switching', 'removing', 'failed', 'completed'].map((stage, i) => ({
    id: 'child-' + i, email: `child-${i}@example.com`, user_id: 'user-' + i, admin_account_id: 'mother-1', team_account_id: 'team-1',
    invite_status: 'completed', accept_status: 'completed', oauth_status: 'completed', push_status: 'completed', quota_status: 'completed', remove_status: i === 3 ? 'failed' : i === 4 ? 'completed' : 'running', remove_method: 'child_leave',
    standard_removal: { stage, message: ['普通席位暂无空位，等待释放后再切换', '母号正在将 5x 席位切为普通席位', '已确认普通席位，正在由子号退出（全局代理）', '切换失败，未执行移出', '转普通后移出完成'][i] },
  })),
}
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin), body = options.body ? JSON.parse(options.body) : null
  fixture.requests.push({ path: url.pathname, method: options.method || 'GET', body })
  if (url.pathname === '/api/auto-rotation/settings') {
    if (options.method === 'PUT') { fixture.settings = body; sessionStorage.setItem('standard-removal-settings', JSON.stringify(body)) }
    return json(fixture.settings)
  }
  if (url.pathname === '/api/auto-rotation/runs') return json({ items: [], total: 0 })
  if (url.pathname === '/api/sub2-settings') return json({ enable_401_check: false, quota_enabled: false })
  if (url.pathname === '/api/push-settings') return json({ provider: 'sub2', sub2: { enable_401_check: false, quota_enabled: false } })
  if (url.pathname === '/api/admin-capacity-snapshots') return json({})
  if (url.pathname === '/api/free-accounts') return json({ items: fixture.items, total: fixture.items.length, summary: { all: 5, inside: 4 } })
  if (url.pathname.includes('quality')) return json({ settings: { enabled: false }, runtime: {} })
  throw new Error('Unmocked request: ' + path)
}
createApp(FreePipelineView, { adminAccounts: [1, 2, 3].map(i => ({ id: 'mother-' + i, label: '母号 ' + i, email: `mother-${i}@example.com`, team_account_id: 'team-' + i })), active: true }).mount('#app')
