import { createApp } from 'vue'
import FreePipelineView from '../src/components/FreePipelineView.vue'
import AdminAccountsView from '../src/components/AdminAccountsView.vue'
import '../src/styles.css'

// Isolated fixture: no request is forwarded to the real backend.
const items = [52.04, 0, null, undefined].map((cost, i) => ({
  id: `cost-${i}`, email: `cost-${i}@example.com`, user_id: `fixture-user-${i}`,
  imported_at: '2026-09-12T00:00:00Z', cost_checked_at: '2026-09-12T04:15:30Z',
  total_cost_usd: 25.1788, total_user_cost_usd: cost,
  cost_downstream_snapshot: 25.1788,
  user_cost_downstream_identity: cost == null ? '' : String(42 + i),
  user_cost_downstream_snapshot: cost == null ? 0 : cost,
  push_provider: i === 3 ? 'cpa' : 'sub2', sub2_account_id: 42 + i,
  cpa_auth_file_name: i === 3 ? 'fixture.json' : '',
  accept_status: 'completed', push_status: 'completed', quota_status: 'completed',
  quota_5h: { used_percent: 25 }, quota_7d: { used_percent: 30 },
}))
const adminItems = [52.04, 0, null].map((cost, i) => ({
  id: `admin-${i}`, label: `Team ${i + 1}`, email: `admin-${i}@example.com`,
  team_account_id: `fixture-team-${i}`, plan_type: 'team',
  team_rotation_child_cost_usd: 25.1788, team_rotation_child_user_cost_usd: cost,
}))
const removalFixture = new URLSearchParams(location.search).has('remove')
if (removalFixture) items.forEach((item, i) => Object.assign(item, {
  admin_account_id: `admin-${i === 3 ? 1 : 0}`, team_account_id: `fixture-team-${i === 3 ? 1 : 0}`,
  invite_status: 'completed', oauth_status: 'completed', remove_status: 'pending', remove_method: 'child_leave',
}))
window.pipelineCostFixture = { requests: [], items, removalFixture, removalEvents: [], failures: new Set() }
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  window.pipelineCostFixture.requests.push({ path: url.pathname, method: options.method || 'GET' })
  const removeMatch = url.pathname.match(/^\/api\/free-accounts\/(cost-\d+)\/remove$/)
  if (removalFixture && removeMatch && options.method === 'POST') {
    const item = items.find(item => item.id === removeMatch[1])
    if (!item) throw new Error('Unknown fixture account')
    const fixture = window.pipelineCostFixture
    fixture.removalEvents.push({ id: item.id, phase: 'start', time: performance.now() })
    await new Promise(resolve => setTimeout(resolve, 450))
    fixture.removalEvents.push({ id: item.id, phase: 'end', time: performance.now() })
    if (fixture.failures.has(item.id)) {
      return new Response(JSON.stringify({ ok: false, error: '模拟母号踢出失败 429' }), { status: 400, headers: { 'Content-Type': 'application/json' } })
    }
    Object.assign(item, { remove_method: 'mother_kick', remove_status: 'completed', status: 'removed' })
    return json(item)
  }
  if (url.pathname === '/api/admin-accounts') return json({ items: adminItems, total: adminItems.length })
  if (url.pathname === '/api/admin-capacity-snapshots') return json({})
  if (url.pathname === '/api/sub2-settings') return json({ enable_401_check: false, quota_enabled: false })
  if (url.pathname === '/api/push-settings') return json({ provider: 'sub2', sub2: { enable_401_check: false, quota_enabled: false } })
  if (url.pathname === '/api/free-accounts') return json({ items, total: items.length, summary: { all: 4, inside: 4, monitoring: 4 } })
  if (url.pathname === '/api/free-accounts/cost-0/quota' && options.method === 'POST') {
    items[0].total_cost_usd = 30
    items[0].total_user_cost_usd = 60
    items[0].cost_downstream_snapshot = 30
    items[0].user_cost_downstream_snapshot = 60
    return json({ account: items[0], auto_removed: false })
  }
  throw new Error(`Unmocked fixture request: ${url.pathname}`)
}
if (new URLSearchParams(location.search).has('admin')) {
  createApp(AdminAccountsView, { accounts: adminItems }).mount('#app')
} else {
  createApp(FreePipelineView).mount('#app')
}
