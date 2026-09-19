import { createApp, h } from 'vue'
import MailManagementView from '../src/components/MailManagementView.vue'
import FreePipelineView from '../src/components/FreePipelineView.vue'
import '../src/styles.css'

const params = new URLSearchParams(location.search)
const team = params.has('team')
const pageSize = Number(params.get('size') || 10)
const fixture = window.filterFixture = { delay: 0, requests: [], errors: [], paint: [] }
window.addEventListener('error', event => fixture.errors.push(event.message))
window.addEventListener('unhandledrejection', event => fixture.errors.push(String(event.reason)))
const items = Array.from({ length: 2000 }, (_, i) => {
  const state = ['outside', 'inside', 'removed', 'dead'][Math.floor(i / 500)]
  return {
    id: String(i), email: `${state}-${i}@example.invalid`, user_id: `preview-${i}`, label: `Preview ${i}`,
    management_scope: 'mail', login_method: 'directurl', state,
    imported_at: '2026-09-19T12:00:00Z', created_at: '2026-09-19T12:00:00Z',
    at_checked_at: '2026-09-19T12:00:00Z', at_valid: true, plan_type: 'team',
    refresh_token_present: true, source_token_present: true, access_token_present: true,
    oauth_access_token_present: true, oauth_refresh_token_present: true,
    invite_status: state === 'outside' ? 'pending' : 'completed',
    accept_status: state === 'outside' ? 'pending' : 'completed',
    remove_status: state === 'removed' ? 'completed' : 'pending',
    oauth_status: 'completed', push_status: 'completed', quota_status: 'completed',
    sub2_account_id: i + 1, quota_7d: { used_percent: 20 }, dead: state === 'dead',
    chatgpt_status: state === 'dead' ? 'dead' : 'valid', quality: {},
  }
})
const reply = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  if (url.pathname === '/api/mail/accounts' || url.pathname === '/api/free-accounts') {
    const record = { state: url.searchParams.get('space_state'), started: performance.now(), aborted: false }
    fixture.requests.push(record)
    options.signal?.addEventListener('abort', () => { record.aborted = true })
    if (fixture.delay) await new Promise(resolve => setTimeout(resolve, fixture.delay))
    const matching = items.filter(item => !record.state || item.state === record.state)
    const limit = Number(url.searchParams.get('page_size') || 10)
    const offset = (Number(url.searchParams.get('page') || 1) - 1) * limit
    record.responded = performance.now()
    requestAnimationFrame(() => requestAnimationFrame(() => { record.painted = performance.now() }))
    return reply({
      items: matching.slice(offset, offset + limit), pipelines: matching.slice(offset, offset + limit), total: matching.length,
      counts: { all: 2000, directurl: 2000 }, space_counts: { outside: 500, inside: 500, removed: 500, dead: 500 },
      summary: { all: 2000, inside: 500, outside: 500, removed: 500, dead: 500, pending_seats_by_admin: {}, seat_usage_by_admin: {} },
    })
  }
  if (url.pathname === '/api/push-settings') return reply({ provider: 'sub2', sub2: {}, cpa: {} })
  if (url.pathname === '/api/sub2-settings') return reply({})
  if (url.pathname === '/api/quality/settings') return reply({ settings: {}, runtime: {}, model_runtime: {} })
  if (url.pathname === '/api/mail/status') return reply({ available: true })
  if (url.pathname === '/api/mail/messages') return reply({ items: [], total: 0 })
  return reply([])
}
createApp({ render: () => h(team ? FreePipelineView : MailManagementView, { defaultPageSize: pageSize, active: true, adminAccounts: [] }) }).mount('#app')
