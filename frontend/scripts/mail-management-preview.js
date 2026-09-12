import { createApp } from 'vue'
import MailManagementView from '../src/components/MailManagementView.vue'
import '../src/styles.css'

// Isolated UI fixture: all requests are handled here, never by the real backend.
const items = Array.from({ length: 24 }, (_, i) => ({
  email: `fixture-${String(i + 1).padStart(2, '0')}@example.com`,
  management_scope: 'mail',
  login_method: 'directurl',
  entered_at: '2026-09-12T08:00:00Z',
  created_at: '2026-09-12T08:00:00Z',
  at_checked_at: '2026-09-12T08:00:00Z',
  at_valid: i % 2 === 0,
  refresh_token_present: i % 3 === 0,
  gpt_password_present: i % 4 === 0,
  totp_secret_present: i % 8 === 0,
  access_token_present: true,
  pickup_url_present: true,
}))
if (new URLSearchParams(location.search).has('totp-no-at')) items[0].access_token_present = false
window.mailExportFixture = { requests: [], fail: false, delay: 350, totpValue: '012345', totpValidity: 30000, totpFail: false, totpDelay: 0 }
const fixture = window.mailExportFixture
const encoder = new TextEncoder()
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  const body = options.body ? JSON.parse(options.body) : {}
  fixture.requests.push({ path: url.pathname, body })
  if (url.pathname === '/api/mail/status') return json({ available: true })
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/totp')) {
    await new Promise(resolve => setTimeout(resolve, fixture.totpDelay))
    if (fixture.totpFail) return new Response(JSON.stringify({ ok: false, error: '模拟密钥格式无效' }), { status: 409 })
    return json({ code: fixture.totpValue, valid_for_ms: fixture.totpValidity })
  }
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/credentials')) {
    const item = items.find(item => item.email === decodeURIComponent(url.pathname.split('/')[4]))
    if (!item) throw new Error('Missing credential fixture')
    return json({ email: item.email, gpt_password: item.gpt_password_present ? 'fixture-password' : '', totp_secret: item.totp_secret_present ? 'JBSWY3DPEHPK3PXP' : '', access_token: item.access_token_present ? 'fixture-at' : '', refresh_token: item.refresh_token_present ? 'fixture-rt' : '' })
  }
  if (url.pathname === '/api/mail/accounts') {
    const page = Number(url.searchParams.get('page') || 1)
    const size = Number(url.searchParams.get('page_size') || 10)
    return json({ items: items.slice((page - 1) * size, page * size), total: items.length, pipelines: [], counts: { all: items.length }, space_counts: { outside: items.length, inside: 0, removed: 0 } })
  }
  if (url.pathname === '/api/mail/accounts/select') {
    const matched = items.filter(item =>
      (body.scope !== 'page' || body.page_emails.includes(item.email)) &&
      (!body.at_status || item.at_valid === (body.at_status === 'valid')) &&
      (!body.require_rt || item.refresh_token_present) &&
      (!body.require_password || item.gpt_password_present) &&
      (!body.require_totp || item.totp_secret_present))
    return json({ emails: matched.map(item => item.email), total: matched.length })
  }
  if (url.pathname === '/api/mail/accounts/credentials/export-progress') {
    const file = encoder.encode(body.emails.map(email => `${email}----fixture-mail-password----https://example.com/pickup${body.include_at ? '----fixture-at' : ''}${body.include_rt ? '----fixture-rt' : ''}\r\n`).join(''))
    let cancelled = false
    return new Response(new ReadableStream({
      async start(controller) {
        const send = event => controller.enqueue(encoder.encode(JSON.stringify(event) + '\n'))
        for (let i = 0; i <= body.emails.length; i++) {
          if (cancelled) return
          send({ type: 'progress', stage: 'processing', processed: i, total: body.emails.length })
          await new Promise(resolve => setTimeout(resolve, fixture.delay))
        }
        if (cancelled) return
        if (fixture.fail) {
          send({ type: 'error', error: '模拟账号已删除，未导出任何账号' })
        } else {
          send({ type: 'progress', stage: 'generating', processed: body.emails.length, total: body.emails.length })
          await new Promise(resolve => setTimeout(resolve, fixture.delay))
          if (cancelled) return
          send({ type: 'file', size: file.length, headers: { 'Content-Type': 'text/plain', 'Content-Disposition': `attachment; filename="fixture-${body.format}.txt"` } })
          controller.enqueue(file)
        }
        controller.close()
      },
      cancel() { cancelled = true },
    }), { headers: { 'Content-Type': 'application/x-mail-export' } })
  }
  throw new Error(`Unmocked fixture request: ${url.pathname}`)
}
createApp(MailManagementView).mount('#app')
