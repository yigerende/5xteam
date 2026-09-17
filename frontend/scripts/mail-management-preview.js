import { createApp } from 'vue'
import MailManagementView from '../src/components/MailManagementView.vue'
import ProManagementView from '../src/components/ProManagementView.vue'
import '../src/styles.css'

// Isolated UI fixture: all requests are handled here, never by the real backend.
const proPreview = new URLSearchParams(location.search).has('pro')
const items = Array.from({ length: 24 }, (_, i) => ({
  email: `fixture-${String(i + 1).padStart(2, '0')}@example.com`,
  management_scope: proPreview ? 'pro' : 'mail',
  current_plan_type: proPreview ? 'pro' : 'free',
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
fixture.mergeStepDelay = 1800
fixture.mergeFailStage = ''
fixture.mergeCalls = []
const savedCredentials = new Map()
const loginJobs = new Map()
const oauthJobs = new Map()
fixture.oauthFail = false
fixture.oauthMissingJob = false
fixture.oauthStatusMissing = false
fixture.oauthStageDelay = 1500
fixture.loginFailEmails = []
fixture.loginDelay = 2200
fixture.loginMissingJob = false
fixture.loginStatusMissing = false
const infoJobs = new Map()
fixture.infoDelay = 1500
fixture.infoMissing = false
const encoder = new TextEncoder()
const json = data => new Response(JSON.stringify({ ok: true, data }), { headers: { 'Content-Type': 'application/json' } })
window.fetch = async (path, options = {}) => {
  const url = new URL(path, location.origin)
  const body = options.body ? JSON.parse(options.body) : {}
  fixture.requests.push({ path: url.pathname, body })
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/oauth')) {
    const email = decodeURIComponent(url.pathname.split('/')[4])
    const jobID = 'codex-oauth-' + (oauthJobs.size + 1)
    oauthJobs.set(jobID, { email, started: Date.now() })
    return json({ job: fixture.oauthMissingJob ? {} : { job_id: jobID, status: 'queued', logs: [] } })
  }
  if (url.pathname.startsWith('/api/mail/oauth/')) {
    const job = oauthJobs.get(decodeURIComponent(url.pathname.split('/')[4]))
    if (!job || fixture.oauthStatusMissing) return new Response(JSON.stringify({ ok: false, error: 'Codex OAuth 任务不存在' }), { status: 404 })
    const steps = ['OAuth 登录方式：邮箱验证码', '邮箱验证码已提交，继续验证 2FA', 'OAuth 回调换取 RT / AT']
    const step = Math.min(steps.length, Math.floor((Date.now() - job.started) / fixture.oauthStageDelay))
    const done = step === steps.length
    if (done && !fixture.oauthFail) {
      const item = items.find(item => item.email === job.email)
      const stored = savedCredentials.get(job.email) || { email: job.email }
      Object.assign(stored, { revision: 'oauth-done', access_token: 'fixture-codex-at', refresh_token: 'fixture-codex-rt' })
      savedCredentials.set(job.email, stored)
      Object.assign(item, { access_token_present: true, refresh_token_present: true, oauth_status: 'completed' })
    }
    return json({ job: { status: done ? (fixture.oauthFail ? 'failed' : 'success') : 'running', error: done && fixture.oauthFail ? '模拟 OAuth 验证失败' : '', logs: steps.slice(0, step).map(message => ({ time: new Date().toISOString(), message })), result: done && !fixture.oauthFail ? { access_token: 'fixture-codex-at', refresh_token: 'fixture-codex-rt' } : null } })
  }
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/login')) {
    const email = decodeURIComponent(url.pathname.split('/')[4])
    const jobID = 'temporary-at-' + (loginJobs.size + 1)
    loginJobs.set(jobID, { email, started: Date.now() })
    return json({ job: fixture.loginMissingJob ? {} : { job_id: jobID, status: 'queued', logs: [{ time: new Date().toISOString(), message: '等待登录' }] } })
  }
  if (url.pathname.startsWith('/api/mail/login/')) {
    const job = loginJobs.get(decodeURIComponent(url.pathname.split('/')[4]))
    if (!job || fixture.loginStatusMissing) return new Response(JSON.stringify({ ok: false, error: '模拟服务重启，任务不存在' }), { status: 404 })
    const done = Date.now() - job.started >= fixture.loginDelay
    const failed = fixture.loginFailEmails.includes(job.email)
    if (done && !failed) {
      const item = items.find(item => item.email === job.email)
      const stored = savedCredentials.get(job.email) || { email: job.email, refresh_token: item.refresh_token_present ? 'fixture-rt' : '' }
      Object.assign(stored, { revision: 'logged-in', access_token: 'fixture-temporary-at', chatgpt_session: JSON.stringify({ accessToken: 'fixture-temporary-at', user: { email: job.email } }) })
      savedCredentials.set(job.email, stored)
      Object.assign(item, { access_token_present: true, at_valid: true })
    }
    return json({ job: { status: done ? (failed ? 'failed' : 'success') : 'running', error: done && failed ? '模拟验证失败' : '', logs: [{ time: new Date().toISOString(), message: '正在登录并验证' }, ...(done ? [{ time: new Date().toISOString(), message: failed ? '模拟验证失败' : 'AT 与 Session 已保存' }] : [])] } })
  }
  if (url.pathname.startsWith('/api/pro-accounts/') && url.pathname.endsWith('/merge')) {
    const item = items.find(item => item.email === decodeURIComponent(url.pathname.split('/')[3]))
    item.pro_workflow_running = true
    item.pro_last_error = ''
    for (const step of ['invite', 'accept', 'transfer', 'remove']) {
      if (item['pro_' + step + '_status'] === 'completed') continue
      item['pro_' + step + '_status'] = 'running'
      fixture.mergeCalls.push(step)
      await new Promise(resolve => setTimeout(resolve, fixture.mergeStepDelay))
      if (fixture.mergeFailStage === step) {
        item['pro_' + step + '_status'] = 'failed'
        item.pro_last_error = '模拟步骤失败，可续跑'
        item.pro_workflow_running = false
        return new Response(JSON.stringify({ ok: false, error: item.pro_last_error }), { status: 400 })
      }
      item['pro_' + step + '_status'] = 'completed'
      if (step === 'transfer') item.space_merged_once = true
    }
    item.pro_workflow_running = false
    return json(item)
  }
  if (url.pathname === '/api/pro-settings') return json({ provider: 'sub2', quota_enabled: false })
  if (url.pathname === '/api/pro-accounts') {
    const page = Number(url.searchParams.get('page') || 1), size = Number(url.searchParams.get('page_size') || 10)
    const pro = items.filter(item => item.management_scope === 'pro')
    return json({ items: pro.slice((page - 1) * size, page * size), total: pro.length, summary: { all: pro.length } })
  }
  if (url.pathname === '/api/mail/status') return json({ available: true })
  if (url.pathname === '/api/mail/accounts/refresh-info') {
    const id = `fixture-job-${infoJobs.size + 1}`
    infoJobs.set(id, { emails: body.emails, started: Date.now() })
    return json({ job_id: id })
  }
  if (url.pathname === '/api/mail/accounts/refresh-info/status') {
    const job = infoJobs.get(url.searchParams.get('job_id'))
    if (!job || fixture.infoMissing) return new Response(JSON.stringify({ ok: false, error: '刷新任务已过期或服务已重启，已保存的信息不会丢失' }), { status: 404 })
    const done = Math.min(job.emails.length, Math.floor((Date.now() - job.started) / fixture.infoDelay))
    const results = job.emails.map((email, index) => {
      const status = index < done ? (index % 3 === 0 ? 'success' : index % 3 === 1 ? 'partial' : 'failed') : 'running'
      const check = { status, checked_at: '2026-09-16T00:00:00Z', plan_source: 'entitlement', plan: { ok: status !== 'failed', http_status: status === 'failed' ? 401 : 200, attempts: 1 }, me: { ok: status === 'success', http_status: status === 'success' ? 200 : 403, attempts: 1 }, error: status === 'success' ? '' : '模拟接口未获取，保留历史数据' }
      const item = items.find(item => item.email === email)
      if (index < done) Object.assign(item, { current_plan_type: 'pro', created_at_openai: '2023-11-14T22:13:20Z', gpt_info_check: check })
      return { email, status, plan_type: 'pro', created_at_openai: '2023-11-14T22:13:20Z', check, error: check.error }
    })
    const page = Number(url.searchParams.get('page') || 1), size = Number(url.searchParams.get('page_size') || 10)
    return json({ job: { total: results.length, done, ok: results.filter(i => i.status === 'success').length, partial: results.filter(i => i.status === 'partial').length, fail: results.filter(i => i.status === 'failed').length, status: done === results.length ? 'done' : 'running', results: results.slice((page - 1) * size, page * size) } })
  }
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/totp')) {
    await new Promise(resolve => setTimeout(resolve, fixture.totpDelay))
    if (fixture.totpFail) return new Response(JSON.stringify({ ok: false, error: '模拟密钥格式无效' }), { status: 409 })
    return json({ code: fixture.totpValue, valid_for_ms: fixture.totpValidity })
  }
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/credentials')) {
    const item = items.find(item => item.email === decodeURIComponent(url.pathname.split('/')[4]))
    if (!item) throw new Error('Missing credential fixture')
    const stored = savedCredentials.get(item.email) || { revision: 'fixture-revision', email: item.email, gpt_password: item.gpt_password_present ? 'fixture-password' : '', totp_secret: item.totp_secret_present ? 'JBSWY3DPEHPK3PXP' : '', access_token: item.access_token_present ? 'fixture-at' : '', refresh_token: item.refresh_token_present ? 'fixture-rt' : '', chatgpt_session: '', chatgpt_account_id: '' }
    if (options.method === 'PATCH') {
      Object.assign(stored, body, { revision: 'updated-' + fixture.requests.length })
      savedCredentials.set(item.email, stored)
      for (const field of ['access_token', 'refresh_token', 'gpt_password', 'totp_secret']) item[field + '_present'] = !!stored[field]
      return json(item)
    }
    const format = url.searchParams.get('format')
    if (format) {
      const credentials = { access_token: stored.access_token, refresh_token: stored.refresh_token, plan_type: item.current_plan_type }
      return json(format === 'cpa' ? { type: 'codex', email: item.email, ...credentials } : { accounts: [{ name: item.email, credentials }] })
    }
    return json(stored)
  }
  if (url.pathname.startsWith('/api/mail/accounts/') && url.pathname.endsWith('/management-scope')) {
    const item = items.find(item => item.email === decodeURIComponent(url.pathname.split('/')[4]))
    item.management_scope = body.scope
    return json(item)
  }
  if (url.pathname === '/api/mail/accounts') {
    const page = Number(url.searchParams.get('page') || 1)
    const size = Number(url.searchParams.get('page_size') || 10)
    const mail = items.filter(item => item.management_scope === 'mail')
    return json({ items: mail.slice((page - 1) * size, page * size), total: mail.length, pipelines: [], counts: { all: mail.length }, space_counts: { outside: mail.length, inside: 0, removed: 0 } })
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
createApp(proPreview ? ProManagementView : MailManagementView).mount('#app')
