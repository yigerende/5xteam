import { createApp } from 'vue'
import FreePipelineView from '../src/components/FreePipelineView.vue'
import '../src/styles.css'

// All requests stay inside this fixture, including settings saved across reloads.
const defaults = {
  provider: 'sub2',
  sub2: { url: 'https://sub2.example.com', email: 'admin@example.com', password_present: true, relogin_failure_limit: 2, enable_401_check: true, quota_enabled: true },
  cpa: { url: '', key_present: false, relogin_failure_limit: 2, enable_401_check: true, quota_enabled: true },
}
const state = JSON.parse(sessionStorage.getItem('push-settings-fixture') || 'null') || defaults
window.pushSettingsFixture = { state, writes: [] }
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  if (url.pathname === '/api/push-settings') {
    if (options.method === 'PUT') {
      const input = JSON.parse(options.body)
      window.pushSettingsFixture.writes.push(input)
      Object.assign(state, input, { sub2: { ...input.sub2, password_present: true } })
      sessionStorage.setItem('push-settings-fixture', JSON.stringify(state))
    }
    return json(state)
  }
  if (url.pathname === '/api/sub2-settings') return json(state.sub2)
  if (url.pathname === '/api/free-accounts') return json({ items: [], total: 0, summary: { all: 0, inside: 0, monitoring: 0 } })
  if (url.pathname === '/api/admin-capacity-snapshots') return json({})
  throw new Error('Unmocked request: ' + url.pathname)
}
createApp(FreePipelineView).mount('#app')
