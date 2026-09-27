import { createApp } from 'vue'
import AdminAccountsView from '../src/components/AdminAccountsView.vue'
import '../src/styles.css'

// Local-only browser fixture. Every API call is intercepted; no real account is touched.
const accounts = [0, 1].map(i => ({ id: `mother-${i}`, label: `测试母号 ${i + 1}`, email: `mother-${i}@example.com`, team_account_id: `team-${i}`, rotation_disabled: i === 1, proxy_id: 'dedicated', current_space_count: 0 }))
const members = Array.from({ length: 27 }, (_, i) => ({ id: `child-${i}`, email: `child+${i}@example.com`, name: `成员 ${i}`, role: i ? 'standard-user' : 'account-owner', seat_type: i % 2 ? 'default' : 'prolite', created_time: '2026-09-27T09:47:53Z', active: i !== 3 }))
window.membersFixture = { requests: [], aborted: 0 }
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  const fixture = window.membersFixture
  fixture.requests.push({ path: url.pathname, query: Object.fromEntries(url.searchParams), method: options.method || 'GET' })
  if (url.pathname === '/api/admin-accounts') return json({ items: accounts, total: accounts.length })
  if (url.pathname === '/api/admin-capacity-snapshots') return json({})
  const match = url.pathname.match(/^\/api\/admin-accounts\/(mother-[01])\/members$/)
  if (!match) throw new Error(`Unexpected fixture request: ${url.pathname}`)
  const query = url.searchParams.get('query') || ''
  options.signal?.addEventListener('abort', () => { fixture.aborted++ }, { once: true })
  // Deliberately return late even if aborted, to verify stale-response protection.
  await new Promise(resolve => setTimeout(resolve, query === 'slow' ? 900 : 180))
  if (query === 'error') return new Response(JSON.stringify({ ok: false, error: '模拟 OpenAI 403，专属代理查询失败' }), { status: 502 })
  const offset = Number(url.searchParams.get('offset'))
  const limit = Number(url.searchParams.get('limit'))
  const filtered = match[1] === 'mother-1' ? [] : members.filter(m => !query || query === 'slow' || m.email.includes(query))
  return json({ items: filtered.slice(offset, offset + limit), total: filtered.length, offset, limit, has_more: offset + limit < filtered.length, queried_at: new Date().toISOString() })
}
createApp(AdminAccountsView, { accounts, proxies: [{ id: 'dedicated', name: '母号专属代理' }] }).mount('#app')
